package ctrl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

const (
	runningImageID     = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runningImageDigest = "sha256:" + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	// What the tag points at NOW, after a `docker pull` that did not recreate anything.
	pulledImageID     = "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	pulledImageDigest = "sha256:" + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
)

// digestCtrl builds a Ctrl over a fake daemon.
//
// createdWith is the broker container's Config.Image (what compose asked for), runningID is
// the image it is actually on, and images maps an image reference or ID to the RepoDigests
// the daemon reports for it.
func digestCtrl(t *testing.T, createdWith, runningID string, images map[string][]string) (*Ctrl, *[]string) {
	t.Helper()

	var inspected []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.47")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id":      "brokercontainerid",
				"Names":   []string{"/" + containerBroker},
				"Image":   createdWith,
				"ImageID": runningID,
				"State":   "running",
			}})
		case strings.Contains(r.URL.Path, "/containers/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":    "brokercontainerid",
				"Image": runningID, // the image the container is on
				"Name":  "/" + containerBroker,
				"State": map[string]any{"Status": "running", "StartedAt": "now"},
				"Config": map[string]any{
					"Image": createdWith, // the reference it was created with
					"Env":   []string{},
				},
			})
		case strings.Contains(r.URL.Path, "/images/"):
			// /images/<ref>/json — record which reference was inspected, because inspecting
			// the tag rather than the running image is the defect this file exists for.
			ref := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.47/images/"), "/json")
			inspected = append(inspected, ref)
			digests, known := images[ref]
			if !known {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "No such image: " + ref})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":          ref,
				"RepoDigests": digests,
				"Created":     "2026-01-01T00:00:00Z",
				"Size":        1,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	dc, err := docker.NewClient(config.ControllerConfig{
		Docker: config.DockerConfig{Host: srv.URL, APIVersion: "1.47"},
	})
	if err != nil {
		t.Fatalf("building the docker client: %v", err)
	}
	t.Cleanup(func() { _ = dc.Close() })

	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatalf("building logger: %v", err)
	}
	return &Ctrl{dockerClient: dc, logger: logger}, &inspected
}

// The digest the signing key is derived from must be the image the broker is RUNNING, not
// whatever its tag points at now.
//
// `docker pull repo:tag` moves a tag under a live container without restarting it. Resolving
// the tag would then derive the key — and the address published in report_data — from the
// newly pulled image while the old one keeps serving. A reviewer who approved the pulled
// image would find the key, the quote and the chain all agreeing on it, and would accept
// responses from code they never saw. That is the exact substitution this whole arrangement
// exists to prevent, so it must be the running image that decides.
func TestTheDigestFollowsTheRunningImageNotTheTag(t *testing.T) {
	c, inspected := digestCtrl(t, "ghcr.io/0gfoundation/0g-serving-broker:latest", runningImageID, map[string][]string{
		runningImageID: {"ghcr.io/0gfoundation/0g-serving-broker@" + runningImageDigest},
		// The tag now resolves to a different image. Nothing must consult it.
		"ghcr.io/0gfoundation/0g-serving-broker:latest": {"ghcr.io/0gfoundation/0g-serving-broker@" + pulledImageDigest},
		pulledImageID: {"ghcr.io/0gfoundation/0g-serving-broker@" + pulledImageDigest},
	})

	got, err := c.RunningBrokerDigest(context.Background())
	if err != nil {
		t.Fatalf("RunningBrokerDigest: %v", err)
	}
	if got == pulledImageDigest {
		t.Fatal("the digest followed the tag, which a `docker pull` moves under a live container")
	}
	if got != runningImageDigest {
		t.Fatalf("digest %s, want the running image's %s", got, runningImageDigest)
	}
	for _, ref := range *inspected {
		if strings.Contains(ref, ":latest") {
			t.Errorf("the tag %q was inspected; only the running image ID may be", ref)
		}
	}
}

// A reference that already pins a digest names the image the container was created on, so it
// is used directly — no daemon lookup can improve on it, and none should happen.
func TestAPinnedReferenceNeedsNoLookup(t *testing.T) {
	c, inspected := digestCtrl(t,
		"ghcr.io/0gfoundation/0g-serving-broker@"+runningImageDigest, runningImageID, nil)

	got, err := c.RunningBrokerDigest(context.Background())
	if err != nil {
		t.Fatalf("RunningBrokerDigest: %v", err)
	}
	if got != runningImageDigest {
		t.Errorf("digest %s, want %s", got, runningImageDigest)
	}
	if len(*inspected) != 0 {
		t.Errorf("a pinned reference triggered image inspections: %v", *inspected)
	}
}

// Everything that cannot be pinned down must refuse. A signature under a key derived from a
// guess is worse than no signature, because it would still verify against something.
func TestAnUnresolvableImageRefuses(t *testing.T) {
	for _, tc := range []struct {
		name        string
		createdWith string
		runningID   string
		images      map[string][]string
	}{
		{
			// Built locally and never pushed: no RepoDigests at all.
			name:        "no digest anywhere",
			createdWith: "0g-serving-broker:dev",
			runningID:   runningImageID,
			images:      map[string][]string{runningImageID: {}},
		},
		{
			name:        "the running image is unknown to the daemon",
			createdWith: "0g-serving-broker:dev",
			runningID:   runningImageID,
			images:      nil,
		},
		{
			name:        "a malformed pinned digest",
			createdWith: "ghcr.io/0gfoundation/0g-serving-broker@sha256:abc",
			runningID:   runningImageID,
			images:      map[string][]string{runningImageID: {"ghcr.io/x@" + runningImageDigest}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := digestCtrl(t, tc.createdWith, tc.runningID, tc.images)
			if got, err := c.RunningBrokerDigest(context.Background()); err == nil {
				t.Errorf("RunningBrokerDigest = %q, want a refusal", got)
			}
		})
	}
}
