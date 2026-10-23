package attest

import (
	"strings"
	"testing"
)

// Sets that name providers: one CVM serving several providers from the same
// processes, each member tagged with the provider whose config permits it.

const (
	provA = "0x00000000000000000000000000000000000000a1"
	provB = "0x00000000000000000000000000000000000000b2"
)

func TestParseUpstreamSetWithProviders(t *testing.T) {
	got, err := parseUpstreamSet("count=3\n" +
		"engine http://engine:8000/v1 provider=" + provA + "\n" +
		"engine http://engine:8000/v1 provider=" + provB + "\n" +
		"vendor https://api.vendor.example/v1 vendor-id provider=" + provB)
	if err != nil {
		t.Fatalf("parseUpstreamSet() = %v", err)
	}
	want := []Upstream{
		{Name: "engine", URL: "http://engine:8000/v1", Provider: provA},
		{Name: "engine", URL: "http://engine:8000/v1", Provider: provB},
		{Name: "vendor", URL: "https://api.vendor.example/v1", Identity: "vendor-id", Provider: provB},
	}
	if len(got) != len(want) {
		t.Fatalf("members = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("member %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseUpstreamSetRefusesBadProviderSets(t *testing.T) {
	for name, payload := range map[string]string{
		"a member without a provider among ones with": "count=2\na http://a:1/v1 provider=" + provA + "\nb http://b:1/v1",
		"a member with a provider among ones without": "count=2\na http://a:1/v1\nb http://b:1/v1 provider=" + provA,
		"one name twice for one provider":             "count=2\na http://a:1/v1 provider=" + provA + "\na http://b:1/v1 provider=" + provA,
		"an uppercase address":                        "count=1\na http://a:1/v1 provider=0x00000000000000000000000000000000000000A1",
		"an address of the wrong length":              "count=1\na http://a:1/v1 provider=0xa1",
		"the provider before the identity":            "count=1\na http://a:1/v1 provider=" + provA + " vendor-id",
		"a provider and too many fields":              "count=1\na http://a:1/v1 x y provider=" + provA,
		"a provider with no URL":                      "count=1\na provider=" + provA,
	} {
		if _, err := parseUpstreamSet(payload); err == nil {
			t.Errorf("%s: parseUpstreamSet(%q) accepted", name, payload)
		}
	}
}

func TestRenderUpstreamSetWithProvidersRoundTrips(t *testing.T) {
	members := []Upstream{
		{Name: "vendor", URL: "https://api.vendor.example/v1", Identity: "vendor-id", Provider: provB},
		{Name: "engine", URL: "http://engine:8000/v1", Provider: provB},
		{Name: "engine", URL: "http://engine:8000/v1", Provider: provA},
	}
	payload, err := RenderUpstreamSet(members)
	if err != nil {
		t.Fatalf("RenderUpstreamSet() = %v", err)
	}
	want := "count=3\n" +
		"engine http://engine:8000/v1 provider=" + provA + "\n" +
		"engine http://engine:8000/v1 provider=" + provB + "\n" +
		"vendor https://api.vendor.example/v1 vendor-id provider=" + provB
	if payload != want {
		t.Fatalf("payload =\n%s\nwant\n%s", payload, want)
	}

	if _, err := RenderUpstreamSet([]Upstream{{Name: "a", URL: "http://a:1/v1", Provider: provA}, {Name: "b", URL: "http://b:1/v1"}}); err == nil {
		t.Error("RenderUpstreamSet accepted a set naming the provider of some members only")
	}
}

func TestUpstreamSetHashWithProviders(t *testing.T) {
	hash := func(t *testing.T, members ...Upstream) string {
		t.Helper()
		h, err := (&RunningState{Upstreams: members, UpstreamsState: UpstreamsKnown}).UpstreamSetHash()
		if err != nil {
			t.Fatalf("UpstreamSetHash() = %v", err)
		}
		return h
	}
	plain := hash(t, Upstream{Name: "engine", URL: "http://engine:8000/v1"})
	a := hash(t, Upstream{Name: "engine", URL: "http://engine:8000/v1", Provider: provA})
	b := hash(t, Upstream{Name: "engine", URL: "http://engine:8000/v1", Provider: provB})
	if plain == a || a == b {
		t.Fatalf("the provider does not enter the hash: plain=%s a=%s b=%s", plain, a, b)
	}

	// Moving a destination from one provider to another changes what each may reach,
	// so it must change the set's identity even though the union of URLs is the same.
	ab := hash(t,
		Upstream{Name: "x", URL: "http://x:1/v1", Provider: provA},
		Upstream{Name: "y", URL: "http://y:1/v1", Provider: provB})
	ba := hash(t,
		Upstream{Name: "x", URL: "http://x:1/v1", Provider: provB},
		Upstream{Name: "y", URL: "http://y:1/v1", Provider: provA})
	if ab == ba {
		t.Fatal("swapping which provider permits which destination kept the hash")
	}

	_, err := (&RunningState{UpstreamsState: UpstreamsKnown, Upstreams: []Upstream{
		{Name: "a", URL: "http://a:1/v1", Provider: provA},
		{Name: "b", URL: "http://b:1/v1"},
	}}).UpstreamSetHash()
	if err == nil || !strings.Contains(err.Error(), "every member's or none") {
		t.Errorf("hash of a partly tagged set = %v, want it refused", err)
	}
}

func TestUpstreamChangesKeyByProvider(t *testing.T) {
	prev := []Upstream{{Name: "engine", URL: "http://engine:8000/v1", Provider: provA}}
	next := []Upstream{
		{Name: "engine", URL: "http://engine:8000/v1", Provider: provA},
		{Name: "engine", URL: "http://other:8000/v1", Provider: provB},
	}
	got := upstreamChanges(prev, next)
	want := provB + "/engine: added as http://other:8000/v1 (no identity)"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("changes = %q, want [%q]", got, want)
	}
}

// A set without providers must hash exactly as it did before the field existed:
// the hash is in every single-provider deployment's key derivation path, so a
// change here would rotate their signers and void their on-chain
// acknowledgements. The value was computed by the code before providers.
func TestUpstreamSetHashWithoutProvidersIsUnchanged(t *testing.T) {
	h, err := (&RunningState{UpstreamsState: UpstreamsKnown, Upstreams: []Upstream{
		{Name: "engine", URL: "http://engine:8000/v1"},
		{Name: "vendor", URL: "https://api.vendor.example/v1", Identity: "vendor-id"},
	}}).UpstreamSetHash()
	if err != nil {
		t.Fatalf("UpstreamSetHash() = %v", err)
	}
	if want := "8b2feb481f9b9ae54059e652a85e6b1959d250a0561d27b6f418cfcbd5fea534"; h != want {
		t.Fatalf("hash = %s, want %s", h, want)
	}
}
