package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/client"
)

// Frames copied from real `docker pull` streams. The failure cases are the
// point: once the daemon has flushed any progress it reports the failure as a
// frame in the body and returns success at the HTTP level, so ImagePull hands
// back a nil error and the only signal left is inside the stream. (A failure
// raised before the first flush is returned by ImagePull instead, and never
// reaches this function.)
func TestDrainPullProgress(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		wantErr string // substring; empty means the pull succeeded
	}{
		{
			name: "successful pull",
			stream: `{"status":"Pulling from 0gfoundation/0g-serving-broker","id":"latest"}
{"status":"Pulling fs layer","progressDetail":{},"id":"a1b2c3"}
{"status":"Downloading","progressDetail":{"current":1024,"total":4096},"progress":"[====>  ]","id":"a1b2c3"}
{"status":"Pull complete","progressDetail":{},"id":"a1b2c3"}
{"status":"Digest: sha256:aaaa"}
{"status":"Status: Downloaded newer image for 0g-serving-broker:latest"}`,
		},
		{
			// No error frame, but no frames at all — a proxy or LB answering 200
			// with nothing. (Not the daemon itself: it only reaches a 200 after
			// its first flush, and a pre-flush failure comes back as an HTTP
			// error.) Reporting this as a success is how the original bug
			// gets back in.
			name:    "empty stream",
			stream:  "",
			wantErr: "carried no frames",
		},
		{
			// The deprecated spelling on its own. Docker populates both today,
			// so without this fixture the fallback branch never executes.
			name: "deprecated error field only",
			stream: `{"status":"Pulling from 0gfoundation/0g-serving-broker","id":"latest"}
{"error":"denied: requested access to the resource is denied"}`,
			wantErr: "denied: requested access",
		},
		{
			name: "auth failure",
			stream: `{"status":"Pulling from 0gfoundation/0g-serving-broker","id":"latest"}
{"errorDetail":{"message":"unauthorized: authentication required"},"error":"unauthorized: authentication required"}`,
			wantErr: "unauthorized",
		},
		{
			// The failure arrives after layers have already been reported as
			// complete, so "we saw progress" is not evidence of success.
			name: "error frame after progress",
			stream: `{"status":"Pulling fs layer","id":"a1b2c3"}
{"status":"Pull complete","id":"a1b2c3"}
{"errorDetail":{"message":"failed to register layer: no space left on device"},"error":"failed to register layer: no space left on device"}`,
			wantErr: "no space left on device",
		},
		{
			// What a daemon that has dropped the deprecated "error" field
			// sends. Reading only "error" would call this pull a success.
			name: "errorDetail only",
			stream: `{"status":"Pulling from 0gfoundation/0g-serving-broker","id":"latest"}
{"errorDetail":{"message":"toomanyrequests: retry later"}}`,
			wantErr: "toomanyrequests",
		},
		{
			// errorDetail is declared omitempty on a pointer, so its presence
			// is the failure signal; message is omitempty too. Keying off a
			// non-empty message would walk straight past this frame.
			name: "errorDetail with only a code",
			stream: `{"status":"Pulling from 0gfoundation/0g-serving-broker","id":"latest"}
{"errorDetail":{"code":1}}`,
			wantErr: "code 1",
		},
		{
			// A connection dropped mid-frame. Silently treating this as EOF
			// would report a half-finished pull as a finished one.
			name:    "truncated stream",
			stream:  `{"status":"Pulling fs layer","id":"a1b2c3"}` + "\n" + `{"status":"Downl`,
			wantErr: "decoding pull progress",
		},
		{
			name:    "garbage stream",
			stream:  "not json at all",
			wantErr: "decoding pull progress",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := drainPullProgress(strings.NewReader(tt.stream))

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("drainPullProgress() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("drainPullProgress() = nil, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("drainPullProgress() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

// fakeDaemon serves the two endpoints PullImage touches, so the wiring between
// them can be tested: that a failing stream stops the function before it ever
// reads a digest, and that a pinned reference wins over the daemon's
// lexically-first RepoDigests entry.
func fakeDaemon(t *testing.T, pullBody string, repoDigests []string) *Client {
	return fakeDaemonWithInspect(t, pullBody, repoDigests, true)
}

func fakeDaemonWithInspect(t *testing.T, pullBody string, repoDigests []string, inspectOK bool) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.Contains(r.URL.Path, "/images/create"):
			_, _ = w.Write([]byte(pullBody))
		case strings.HasSuffix(r.URL.Path, "/json"):
			if !inspectOK {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "No such image"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				"RepoDigests": repoDigests,
				"Created":     "2026-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithVersion("1.47"))
	if err != nil {
		t.Fatalf("building docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	return &Client{cli: cli}
}

const (
	// Real-shaped digests: the docker client validates reference format before
	// the request leaves the process, so a placeholder like "sha256:PINNED"
	// fails with "invalid reference format" instead of exercising anything.
	digestWanted = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	digestOther  = "sha256:0222222222222222222222222222222222222222222222222222222222222222"
	okStream     = `{"status":"Pulling from 0gfoundation/0g-serving-broker","id":"latest"}
{"status":"Status: Downloaded newer image for 0g-serving-broker"}`
)

func TestPullImage(t *testing.T) {
	const repo = "ghcr.io/0gfoundation/0g-serving-broker"

	t.Run("pinned digest beats the daemon's first repo digest", func(t *testing.T) {
		// The daemon sorts by the NORMALIZED reference ("docker.io/0gfoundation/
		// mirror@…") and emits the familiar one, so a mirror whose name sorts
		// below "ghcr.io" takes entry zero — the multi-repo hazard the doc
		// comment argues about, in the shape a real daemon produces it.
		c := fakeDaemon(t, okStream, []string{
			"0gfoundation/mirror@" + digestOther,
			repo + "@" + digestWanted,
		})

		info, err := c.PullImage(context.Background(), repo+"@"+digestWanted)
		if err != nil {
			t.Fatalf("PullImage() = %v, want nil", err)
		}
		if info.Digest != digestWanted {
			t.Errorf("Digest = %q, want %q", info.Digest, digestWanted)
		}
	})

	// Pins a KNOWN LIMITATION rather than endorsing the behaviour. A tag has no
	// digest to prefer, so the answer still comes from RepoDigests[0] — here the
	// mirror's, not the repo that was asked for. Fixing it means changing
	// api/common/docker.GetImageInfo, which inference also calls, and spec
	// invariant 1 forbids altering inference behaviour in this slice; it is
	// deferred to S5, which removes that caller. Asserting the wrong-looking
	// value keeps the gap visible until then, and this test flips to
	// digestWanted the moment it is closed.
	t.Run("tag inherits the daemon's first repo digest, mirror included", func(t *testing.T) {
		c := fakeDaemon(t, okStream, []string{
			"0gfoundation/mirror@" + digestOther,
			repo + "@" + digestWanted,
		})

		info, err := c.PullImage(context.Background(), repo+":latest")
		if err != nil {
			t.Fatalf("PullImage() = %v, want nil", err)
		}
		if info.Digest != digestOther {
			t.Errorf("Digest = %q, want %q (the mirror's — see comment)", info.Digest, digestOther)
		}
	})

	t.Run("inspect failing after a good pull is an error", func(t *testing.T) {
		c := fakeDaemonWithInspect(t, okStream, nil, false)

		if _, err := c.PullImage(context.Background(), repo+"@"+digestWanted); err == nil {
			t.Fatal("PullImage() = nil, want error when the post-pull inspect fails")
		}
	})

	t.Run("failed stream returns an error and no digest", func(t *testing.T) {
		// The daemon would still happily inspect a stale local image. The point
		// of the fix is that PullImage never gets that far.
		c := fakeDaemon(t, `{"errorDetail":{"message":"unauthorized: authentication required"}}`,
			[]string{repo + "@" + digestOther})

		info, err := c.PullImage(context.Background(), repo+"@"+digestWanted)
		if err == nil {
			t.Fatalf("PullImage() = %+v, want error", info)
		}
		if info != nil {
			t.Errorf("PullImage() returned %+v alongside an error, want nil", info)
		}
		if !strings.Contains(err.Error(), "unauthorized") {
			t.Errorf("PullImage() = %v, want error mentioning the daemon's reason", err)
		}
	})
}
