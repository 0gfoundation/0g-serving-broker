package ctrl

import (
	"strings"
	"testing"
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
