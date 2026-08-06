package docker

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeContainerDaemon serves the container list plus a stop endpoint, and
// records which container IDs the daemon was actually asked to stop. The list
// is what both unguardedContainerID and selfContainerID read, so it is enough
// to exercise the self-guard end to end.
func fakeContainerDaemon(t *testing.T, containers []map[string]any, stopped *[]string) *Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode(containers)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/stop"), "/")
			*stopped = append(*stopped, parts[len(parts)-1])
			w.WriteHeader(http.StatusNoContent)
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

// stubHostname points the identification logic at a hostname the test chooses.
// The real one belongs to whichever machine runs the test, which is the one
// input a test cannot arrange — and without arranging it the short-hostname and
// ambiguous-prefix branches are unreachable.
func stubHostname(t *testing.T, name string) {
	t.Helper()
	prev := hostnameFn
	hostnameFn = func() (string, error) { return name, nil }
	t.Cleanup(func() { hostnameFn = prev })
}

const (
	// A full-length ID whose first shortIDLen characters are what docker would
	// hand the container as its hostname.
	selfID     = "abc123def456000000000000000000000000000000000000000000000000cafe"
	selfHost   = "abc123def456"
	otherID    = "1111111111111111111111111111111111111111111111111111111111111111"
	brokerName = "0g-serving-provider-broker"
)

// The controller container's name CONTAINS the broker's, which is what the
// substring fallback gets wrong: with the real broker absent it is the only
// match, so "stop the broker" resolves to the controller itself.
func selfAndOther() []map[string]any {
	return []map[string]any{
		{"Id": selfID, "Names": []string{"/" + brokerName + "-controller"}},
		{"Id": otherID, "Names": []string{"/0g-serving-provider-event"}},
	}
}

// One entry per getContainerID call site, so none of them can be repointed at
// unguardedContainerID with the suite still green. RecreateContainerWithEnv is
// covered through UpdateContainerEnv, which is one of its two wrappers.
func TestWritePathsRefuseSelf(t *testing.T) {
	stubHostname(t, selfHost)

	writes := map[string]func(*Client) error{
		"StartContainer":   func(c *Client) error { return c.StartContainer(context.Background(), brokerName) },
		"StopContainer":    func(c *Client) error { return c.StopContainer(context.Background(), brokerName) },
		"RestartContainer": func(c *Client) error { return c.RestartContainer(context.Background(), brokerName) },
		"RecreateContainer": func(c *Client) error {
			_, err := c.RecreateContainer(context.Background(), brokerName, "img@sha256:"+strings.Repeat("0", 64))
			return err
		},
		"ReloadNginx": func(c *Client) error { return c.ReloadNginx(context.Background(), brokerName) },
		"UpdateContainerEnv": func(c *Client) error {
			return c.UpdateContainerEnv(context.Background(), brokerName, map[string]string{"A": "B"})
		},
	}

	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			var stopped []string
			c := fakeContainerDaemon(t, selfAndOther(), &stopped)

			err := write(c)
			var selfErr *SelfOperationError
			if !errors.As(err, &selfErr) {
				t.Fatalf("%s() = %v, want *SelfOperationError", name, err)
			}
			// The daemon has no endpoints beyond list and stop, so anything
			// that got past the guard would fail on a 404 rather than here.
			// Asserting the stop specifically is what proves the refusal
			// happened before the container was touched.
			if len(stopped) != 0 {
				t.Errorf("daemon was asked to stop %v, want nothing", stopped)
			}
		})
	}
}

func TestSelfIdentification(t *testing.T) {
	t.Run("another container is still writable", func(t *testing.T) {
		stubHostname(t, selfHost)
		var stopped []string
		c := fakeContainerDaemon(t, selfAndOther(), &stopped)

		if err := c.StopContainer(context.Background(), "0g-serving-provider-event"); err != nil {
			t.Fatalf("StopContainer() = %v, want nil", err)
		}
		if len(stopped) != 1 || stopped[0] != otherID {
			t.Errorf("daemon stopped %v, want [%s]", stopped, otherID)
		}
	})

	t.Run("the hostname must be a prefix, not a substring", func(t *testing.T) {
		// An ID that merely contains the hostname is a different container.
		// Reading the match as "contains" would refuse writes to it forever.
		stubHostname(t, "cafe000000000000")
		var stopped []string
		c := fakeContainerDaemon(t, []map[string]any{
			{"Id": "0000cafe0000000000000000000000000000000000000000000000000000beef",
				"Names": []string{"/0g-serving-provider-event"}},
			{"Id": "cafe000000000000000000000000000000000000000000000000000000000000",
				"Names": []string{"/0g-controller"}},
		}, &stopped)

		if err := c.StopContainer(context.Background(), "0g-serving-provider-event"); err != nil {
			t.Fatalf("StopContainer() = %v, want nil", err)
		}
		if len(stopped) != 1 {
			t.Errorf("daemon stopped %v, want the event container", stopped)
		}
	})

	t.Run("self absent blocks the write", func(t *testing.T) {
		// Nothing in the list carries the hostname, so nothing can be ruled out
		// as self and the write is refused rather than proceeding blind.
		stubHostname(t, selfHost)
		var stopped []string
		c := fakeContainerDaemon(t, []map[string]any{
			{"Id": otherID, "Names": []string{"/0g-serving-provider-event"}},
		}, &stopped)

		err := c.StopContainer(context.Background(), "0g-serving-provider-event")
		var unknownErr *SelfUnidentifiedError
		if !errors.As(err, &unknownErr) {
			t.Fatalf("StopContainer() = %v, want *SelfUnidentifiedError", err)
		}
		if unknownErr.Ambiguous {
			t.Errorf("Ambiguous = true, want false for an absent self")
		}
		if len(stopped) != 0 {
			t.Errorf("daemon was asked to stop %v, want nothing", stopped)
		}
	})

	t.Run("a hostname too short to be an ID blocks the write", func(t *testing.T) {
		// Without the length guard neither hostname refuses for the reason it
		// should: "" prefixes both containers and reaches the ambiguity branch,
		// while "1" prefixes only the event container and names it as self,
		// refusing a write that should have been allowed. The Ambiguous
		// assertion below is what separates those from the length refusal.
		for _, host := range []string{"", "1"} {
			stubHostname(t, host)
			var stopped []string
			c := fakeContainerDaemon(t, selfAndOther(), &stopped)

			err := c.StopContainer(context.Background(), "0g-serving-provider-event")
			var unknownErr *SelfUnidentifiedError
			if !errors.As(err, &unknownErr) {
				t.Fatalf("hostname %q: StopContainer() = %v, want *SelfUnidentifiedError", host, err)
			}
			if unknownErr.Ambiguous {
				t.Errorf("hostname %q: Ambiguous = true, want the length to be the stated reason", host)
			}
			if len(stopped) != 0 {
				t.Errorf("hostname %q: daemon was asked to stop %v, want nothing", host, stopped)
			}
		}
	})

	t.Run("an ambiguous prefix blocks the write", func(t *testing.T) {
		// Two IDs carry the hostname. Picking either would be a guess decided by
		// the daemon's list order.
		stubHostname(t, selfHost)
		var stopped []string
		c := fakeContainerDaemon(t, []map[string]any{
			{"Id": selfHost + strings.Repeat("a", 52), "Names": []string{"/0g-controller"}},
			{"Id": selfHost + strings.Repeat("b", 52), "Names": []string{"/0g-serving-provider-event"}},
		}, &stopped)

		err := c.StopContainer(context.Background(), "0g-serving-provider-event")
		var unknownErr *SelfUnidentifiedError
		if !errors.As(err, &unknownErr) {
			t.Fatalf("StopContainer() = %v, want *SelfUnidentifiedError", err)
		}
		if !unknownErr.Ambiguous {
			t.Errorf("Ambiguous = false, want true when two IDs share the prefix")
		}
		if len(stopped) != 0 {
			t.Errorf("daemon was asked to stop %v, want nothing", stopped)
		}
	})
}

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
