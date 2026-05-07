package providercontract

import "testing"

// TestIsServiceNotFoundMessage verifies that the anchored match correctly
// classifies the RPC "service not found" sentinel and rejects sibling
// messages where the phrase appears embedded inside a longer, unrelated
// error.  False-positive classification here would cause GetService to
// return ErrServiceNotFound for a transient RPC failure, which in turn
// would drive SyncServicePrices into an unintended first-time
// registration path.
func TestIsServiceNotFoundMessage(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"bare sentinel", "service not found", true},
		{"revert wrapped", "execution reverted: service not found", true},
		{"multi-prefix wrapped", "contract call failed: execution reverted: service not found", true},
		{"empty", "", false},
		{"unrelated", "internal server error", false},
		{"embedded substring at start", "service not found path is invalid", false},
		{"embedded substring in middle", "nested service not found path", false},
		{"embedded substring no colon prefix", "the service not found", false},
		{"trailing space is not the sentinel", "service not found ", false},
		{"similar but different phrase", "service not funded", false},
		{"colon without space prefix", "x:service not found", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isServiceNotFoundMessage(tc.msg)
			if got != tc.want {
				t.Errorf("isServiceNotFoundMessage(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}
