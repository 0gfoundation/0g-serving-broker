package ctrl

import (
	"context"
	"strings"
	"testing"
)

// The literals are pinned here, not only in the code that uses them: they decide
// which container a managed operation acts on, and before this test a typo in one
// of them was killed by nothing in the suite.
//
// getContainerName reads no field of Ctrl, so a zero value is the whole fixture.
func TestGetContainerName(t *testing.T) {
	want := map[string]string{
		"broker":          "0g-serving-provider-broker",
		"event":           "0g-serving-provider-event",
		"ingress":         "broker-ingress",
		"prometheus-init": "prometheus-init",
		"prometheus":      "prometheus",

		// Not a managed alias. Callers key off the empty string, so a real name
		// here would hand an unmanaged container to a write path.
		"":                       "",
		"controller":             "",
		"0g-serving-provider-db": "",
	}

	c := &Ctrl{}
	for alias, name := range want {
		if got := c.getContainerName(alias); got != name {
			t.Errorf("getContainerName(%q) = %q, want %q", alias, got, name)
		}
	}
}

// GetAllManagedContainerAliases feeds GET /v1/containers. An alias listed there
// but unknown to getContainerName is silently dropped from the status walk, so
// the endpoint would under-report the fleet without anything failing.
func TestAllManagedAliasesResolve(t *testing.T) {
	c := &Ctrl{}
	aliases := c.GetAllManagedContainerAliases()
	if len(aliases) != 5 {
		t.Errorf("GetAllManagedContainerAliases() = %v, want 5 aliases", aliases)
	}

	seen := map[string]bool{}
	for _, alias := range aliases {
		name := c.getContainerName(alias)
		if name == "" {
			t.Errorf("alias %q is advertised as managed but resolves to no container", alias)
		}
		if seen[name] {
			t.Errorf("alias %q resolves to %q, which another alias already claimed", alias, name)
		}
		seen[name] = true
	}
}

const validDigest = "sha256:02f86cec7e827c16888e667fbcfa889aea7532a188df36ee06bd57375c9a89dd"

// ValidateDigest is what makes the upgrade entry point digest-only. Anything it
// lets through becomes half of the single reference the upgrade runs on, and —
// once RTMR3 accounting lands on top — half of a record that cannot be edited
// after the fact. So the shapes that are nearly right matter most here.
func TestValidateDigest(t *testing.T) {
	accepted := []string{
		validDigest,
		"sha256:" + strings.Repeat("0", 64),
		"sha256:" + strings.Repeat("f", 64),
	}
	for _, digest := range accepted {
		if err := ValidateDigest(digest); err != nil {
			t.Errorf("ValidateDigest(%q) = %v, want nil", digest, err)
		}
	}

	rejected := map[string]string{
		"empty":              "",
		"bare hex":           strings.Repeat("a", 64),
		"no prefix colon":    "sha256" + strings.Repeat("a", 64),
		"wrong algorithm":    "sha512:" + strings.Repeat("a", 64),
		"one char short":     "sha256:" + strings.Repeat("a", 63),
		"one char long":      "sha256:" + strings.Repeat("a", 65),
		"uppercase hex":      "sha256:" + strings.Repeat("A", 64),
		"non-hex":            "sha256:" + strings.Repeat("g", 64),
		"tag":                "latest",
		"full reference":     "ghcr.io/0gfoundation/0g-serving-broker@" + validDigest,
		"leading space":      " " + validDigest,
		"trailing newline":   validDigest + "\n",
		"trailing separator": validDigest + ":",
	}
	for name, digest := range rejected {
		err := ValidateDigest(digest)
		if err == nil {
			t.Errorf("ValidateDigest(%q) [%s] = nil, want an error", digest, name)
			continue
		}
		if _, ok := err.(*InvalidDigestError); !ok {
			t.Errorf("ValidateDigest(%q) [%s] returned %T, want *InvalidDigestError", digest, name, err)
		}
	}
}

// A repo that already carries a tag or a digest would produce imageRepo@digest
// references docker cannot resolve, and would mean something other than the
// caller's digest had a say in which image runs.
func TestValidateImageRepo(t *testing.T) {
	accepted := []string{
		"ghcr.io/0gfoundation/0g-serving-broker",
		"0g-serving-broker",
		// A port on the registry host is not a tag, and the check has to tell
		// the two apart because both are a colon.
		"localhost:5000/0g-serving-broker",
	}
	for _, repo := range accepted {
		if err := validateImageRepo(repo); err != nil {
			t.Errorf("validateImageRepo(%q) = %v, want nil", repo, err)
		}
	}

	rejected := map[string]string{
		"empty":              "",
		"tag":                "ghcr.io/0gfoundation/0g-serving-broker:latest",
		"digest":             "ghcr.io/0gfoundation/0g-serving-broker@" + validDigest,
		"tag on host port":   "localhost:5000/0g-serving-broker:latest",
		"tag without a host": "0g-serving-broker:v1",
	}
	for name, repo := range rejected {
		if err := validateImageRepo(repo); err == nil {
			t.Errorf("validateImageRepo(%q) [%s] = nil, want an error", repo, name)
		}
	}
}

// The digest is checked before anything is stopped, removed or created.
//
// The zero-value Ctrl is the assertion: its docker client is nil, so any
// container work at all panics rather than reporting a miss. A malformed digest
// reaching the pull would otherwise fail late, with the event container already
// down.
func TestUpdateImagesRejectsBadDigestBeforeTouchingContainers(t *testing.T) {
	c := &Ctrl{}
	for _, digest := range []string{"", "latest", "sha256:" + strings.Repeat("a", 63)} {
		result, err := c.UpdateImages(context.Background(), digest)
		if err == nil {
			t.Errorf("UpdateImages(%q) = nil error, want *InvalidDigestError", digest)
		}
		// The handler reports a non-nil result as partial progress; there was none.
		if result != nil {
			t.Errorf("UpdateImages(%q) returned result %+v, want nil", digest, result)
		}
	}
}
