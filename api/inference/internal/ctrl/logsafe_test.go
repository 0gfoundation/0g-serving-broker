package ctrl

import (
	"strings"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// The invariant: no run of input bytes survives into the output. Asserted rather than
// eyeballed, because the whole point of the helper is that a reader of the code can be
// sure of it without checking every call site.
func TestBodyFingerprintForLogEchoesNothing(t *testing.T) {
	for _, body := range []string{
		`{"text":"my medical history is confidential"}`,
		"Bearer sk-secret-token",
		strings.Repeat("A", 5000),
		"短中文内容也不能回显",
		"\x00\x01\x02binary",
	} {
		got := bodyFingerprintForLog([]byte(body))
		// Every substring of length 4 or more, so a partial echo fails too.
		for i := 0; i+4 <= len(body); i++ {
			if frag := body[i : i+4]; strings.Contains(got, frag) {
				t.Fatalf("output %q echoes input fragment %q", got, frag)
			}
		}
		if !strings.HasPrefix(got, "len=") {
			t.Errorf("output %q does not start with the length", got)
		}
	}
}

// Length and digest are the two things the removed dumps were used for: telling empty
// from unparseable, and telling a repeating failure from a varying one.
func TestBodyFingerprintForLogDistinguishes(t *testing.T) {
	if got := bodyFingerprintForLog(nil); got != "len=0" {
		t.Errorf("empty body = %q, want len=0", got)
	}
	same := bodyFingerprintForLog([]byte("identical failure"))
	if bodyFingerprintForLog([]byte("identical failure")) != same {
		t.Error("the same body fingerprinted differently; a retry loop cannot tell repeats apart")
	}
	if bodyFingerprintForLog([]byte("a different failure")) == same {
		t.Error("different bodies share a fingerprint")
	}
	// Same length, different content: the digest has to be what separates them.
	if bodyFingerprintForLog([]byte("aaaa")) == bodyFingerprintForLog([]byte("bbbb")) {
		t.Error("equal-length bodies are indistinguishable, so only the length is being reported")
	}
}

// The upstream-error path keeps the vendor's message, so redaction has to be by value
// and has to survive the key appearing anywhere in the body — including quoted back
// inside a sentence, which is exactly how vendors return it.
func TestRedactUpstreamSecrets(t *testing.T) {
	const key = "sk-proj-abcdefghijklmnop"
	const perModel = "Bearer per-model-key-1234567"
	c := &Ctrl{Service: config.Service{
		AdditionalSecret: map[string]string{"Authorization": key, "X-Short": "xyz"},
		ModelPricing:     []config.ModelPricingEntry{{AdditionalSecret: map[string]string{"X-Key": perModel}}},
	}}

	// "xyz" appears on its own, so the too-short check is about that value and not
	// about a fragment of a longer secret that was redacted with it.
	body := `{"error":{"message":"Incorrect API key provided: ` + key + `. Header was ` + perModel + `. model=xyz-turbo"}}`
	got := c.redactUpstreamSecrets(body)

	for name, secret := range map[string]string{"service secret": key, "per-model secret": perModel} {
		if strings.Contains(got, secret) {
			t.Errorf("%s survived redaction: %q", name, got)
		}
	}
	// The diagnostic half has to survive, or the log is not worth keeping.
	if !strings.Contains(got, "Incorrect API key provided") {
		t.Errorf("the vendor message was destroyed: %q", got)
	}
	// A three-character value would redact ordinary words out of the message.
	if !strings.Contains(got, "xyz-turbo") {
		t.Errorf("a too-short secret was redacted, damaging the message: %q", got)
	}
}
