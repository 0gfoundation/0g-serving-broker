package config

import (
	"strings"
	"testing"
)

// ServiceFromYAML has to resolve service.targetUrl the way loadConfig does, because the
// controller derives the recorded upstream set from it. A copy that resolved it
// differently would record a set naming a destination the broker does not use — a bound
// that reads as verified and is not the deployment's.
//
// TARGET_URL wins over the file, and that is the whole reason it exists: the compose is
// the only place a user can read the value from an attested source, so the half a user
// cannot verify must not be able to override it.
func TestServiceFromYAMLAppliesTargetURLPrecedence(t *testing.T) {
	const withFileURL = "service:\n  targetUrl: http://from-file:8000/v1\n"

	t.Run("the file decides when the env is unset", func(t *testing.T) {
		t.Setenv(targetURLEnvVar, "")
		svc, err := ServiceFromYAML([]byte(withFileURL))
		if err != nil {
			t.Fatalf("ServiceFromYAML() = %v", err)
		}
		if svc.TargetURL != "http://from-file:8000/v1" {
			t.Errorf("TargetURL = %q, want the file's value", svc.TargetURL)
		}
	})

	t.Run("the env wins when both are set", func(t *testing.T) {
		t.Setenv(targetURLEnvVar, "http://from-env:9000/v1")
		svc, err := ServiceFromYAML([]byte(withFileURL))
		if err != nil {
			t.Fatalf("ServiceFromYAML() = %v", err)
		}
		if svc.TargetURL != "http://from-env:9000/v1" {
			t.Errorf("TargetURL = %q, want the env's value: precedence, not fallback", svc.TargetURL)
		}
	})

	t.Run("the env fills in when the file names none", func(t *testing.T) {
		t.Setenv(targetURLEnvVar, "http://from-env:9000/v1")
		svc, err := ServiceFromYAML([]byte("service:\n  model: m\n"))
		if err != nil {
			t.Fatalf("ServiceFromYAML() = %v", err)
		}
		if svc.TargetURL != "http://from-env:9000/v1" {
			t.Errorf("TargetURL = %q, want the env's value", svc.TargetURL)
		}
	})

	// Whitespace-only is not a value, matching loadConfig's TrimSpace: an empty env var
	// must not blank out a URL the file names.
	t.Run("a blank env does not erase the file's value", func(t *testing.T) {
		t.Setenv(targetURLEnvVar, "   ")
		svc, err := ServiceFromYAML([]byte(withFileURL))
		if err != nil {
			t.Fatalf("ServiceFromYAML() = %v", err)
		}
		if svc.TargetURL != "http://from-file:8000/v1" {
			t.Errorf("TargetURL = %q, want the file's value", svc.TargetURL)
		}
	})
}

// It reads per-model upstreams too, because a model resolving to either destination
// means plaintext can reach either — and modelPricing is where a deployment fans one
// provider out to several.
func TestServiceFromYAMLReadsPerModelUpstreams(t *testing.T) {
	t.Setenv(targetURLEnvVar, "")
	svc, err := ServiceFromYAML([]byte(`
service:
  targetUrl: https://openrouter.ai/api/v1
  providerIdentity: openrouter
  modelPricing:
    - model: a
      targetUrl: https://tokenhub.tencentcloudmaas.com/v1
      providerIdentity: tencent
    - model: b
      targetUrl: https://api.minimax.io/v1
      providerIdentity: minimax
`))
	if err != nil {
		t.Fatalf("ServiceFromYAML() = %v", err)
	}
	if svc.ProviderIdentity != "openrouter" {
		t.Errorf("ProviderIdentity = %q", svc.ProviderIdentity)
	}
	if len(svc.ModelPricing) != 2 {
		t.Fatalf("ModelPricing has %d entries, want 2", len(svc.ModelPricing))
	}
	for i, want := range []struct{ url, identity string }{
		{"https://tokenhub.tencentcloudmaas.com/v1", "tencent"},
		{"https://api.minimax.io/v1", "minimax"},
	} {
		if svc.ModelPricing[i].TargetURL != want.url || svc.ModelPricing[i].ProviderIdentity != want.identity {
			t.Errorf("ModelPricing[%d] = %q/%q, want %q/%q", i,
				svc.ModelPricing[i].TargetURL, svc.ModelPricing[i].ProviderIdentity, want.url, want.identity)
		}
	}
}

// Strict, like the loader.
//
// The controller uses this on content it is about to write, and the broker will read that
// content with UnmarshalStrict at its next start. Accepting a key the broker refuses
// would let the controller derive a set from content the broker cannot even parse — and
// report it as the deployment's bound.
func TestServiceFromYAMLIsStrictLikeTheLoader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"a key the Service struct does not have", "service:\n  name: whatever\n"},
		{"a key no struct has", "notASection:\n  x: 1\n"},
		{"a value of the wrong type", "service:\n  targetUrl:\n    nested: yes\n"},
		{"not YAML at all", "\tthis: is: not: yaml\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(targetURLEnvVar, "")
			if svc, err := ServiceFromYAML([]byte(tc.content)); err == nil {
				t.Fatalf("accepted %q as %+v, want a refusal", tc.content, svc)
			} else if !strings.Contains(err.Error(), "parsing config content") {
				t.Errorf("error %q does not say what failed", err)
			}
		})
	}
}

// Empty content is the empty Service, not an error: a config that names no upstream is a
// config permitting none, which is a bound of zero and a thing a deployment may say.
func TestServiceFromYAMLAcceptsContentWithNoService(t *testing.T) {
	t.Setenv(targetURLEnvVar, "")
	svc, err := ServiceFromYAML([]byte(""))
	if err != nil {
		t.Fatalf("ServiceFromYAML(\"\") = %v", err)
	}
	if svc.TargetURL != "" || len(svc.ModelPricing) != 0 {
		t.Errorf("empty content gave %+v, want a zero Service", svc)
	}
}

// It must not disturb the running process's configuration. GetConfig is a once.Do
// singleton and the controller calls this while holding it, so a version that wrote into
// that instance would rewrite the running config from content that has not been applied
// yet — and on a parse failure would leave it half-written.
func TestServiceFromYAMLLeavesTheSingletonAlone(t *testing.T) {
	t.Setenv(targetURLEnvVar, "")
	live := GetConfig()
	before := live.Service.TargetURL

	if _, err := ServiceFromYAML([]byte("service:\n  targetUrl: http://somewhere-else:1/v1\n")); err != nil {
		t.Fatalf("ServiceFromYAML() = %v", err)
	}
	if live.Service.TargetURL != before {
		t.Errorf("the running config's TargetURL changed from %q to %q", before, live.Service.TargetURL)
	}
}

// The answer must depend on the CONTENT ALONE, and not merely leave the singleton
// unmodified.
//
// A version starting from a copy of the running config — `cfg := *GetConfig()` rather
// than `var cfg Config` — passes the test above, because a copy is not the singleton.
// What it gets wrong is everything the content does not mention: the overlay keeps the
// running values, so content that REMOVES a per-model upstream would still report it.
//
// That is fail-open in the direction that matters. Withdrawing a vendor is the change a
// reader most needs to see, and inheriting it from the config being replaced would
// record a set naming a destination the new config does not permit.
func TestServiceFromYAMLDependsOnTheContentAlone(t *testing.T) {
	t.Setenv(targetURLEnvVar, "")
	live := GetConfig()
	restore := live.Service
	t.Cleanup(func() { live.Service = restore })

	// A running config with more in it than the content below names.
	live.Service = Service{
		TargetURL:        "https://running.example/v1",
		ProviderIdentity: "running",
		ModelPricing: []ModelPricingEntry{
			{Model: "gone", TargetURL: "https://withdrawn.example/v1", ProviderIdentity: "withdrawn"},
		},
	}

	svc, err := ServiceFromYAML([]byte("service:\n  targetUrl: http://only-this:8000/v1\n"))
	if err != nil {
		t.Fatalf("ServiceFromYAML() = %v", err)
	}
	if svc.TargetURL != "http://only-this:8000/v1" {
		t.Errorf("TargetURL = %q, want the content's value", svc.TargetURL)
	}
	if svc.ProviderIdentity != "" {
		t.Errorf("ProviderIdentity = %q, inherited from the running config the content replaces", svc.ProviderIdentity)
	}
	if len(svc.ModelPricing) != 0 {
		t.Errorf("ModelPricing = %+v, inherited from the running config: a withdrawn upstream would still be recorded", svc.ModelPricing)
	}
}
