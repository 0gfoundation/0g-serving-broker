package ctrl

import "testing"

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
