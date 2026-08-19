package ctrl

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// bodyFingerprintForLog describes a request or response body without reproducing any of
// it: a length and the first four bytes of its SHA-256.
//
// Bodies must never reach the logs, at any level. Logs are readable by the provider —
// GET /v1/logs authenticates with the provider's own key — and the provider is the party
// the deployment's confidentiality promise is made against. A log level is a runtime
// value delivered outside what compose_hash covers, so "we only log bodies at debug" is
// not something a user can verify; making it impossible is what can be verified, by
// reading the code for the image digest they accepted.
//
// The fingerprint keeps what the removed dumps were actually used for. Length separates
// "empty" from "unparseable", and the digest prefix makes a failure that repeats
// identically distinguishable from one that varies — which is what a retry loop needs to
// tell apart. Truncating instead would have kept the leak and only bounded it.
//
// Not for short operator-set scalars: a model name, a bucket size or an upstream host is
// the operator's own configuration, and truncateForLog is right for those.
// maxUpstreamErrorBodyLog bounds a redacted upstream error body. Generous enough for a
// vendor's message and its surrounding JSON, short enough that a body echoing a whole
// request cannot be reconstructed from the logs.
const maxUpstreamErrorBodyLog = 512

func bodyFingerprintForLog(b []byte) string {
	if len(b) == 0 {
		return "len=0"
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("len=%d sha256=%x", len(b), sum[:4])
}

// redactUpstreamSecrets replaces every upstream credential this broker holds with a
// marker, wherever it appears in s.
//
// Upstream error bodies are the one place a credential reliably comes back: vendors
// quote the key they rejected ("Incorrect API key provided: sk-proj-…"), and some echo
// the request that carried it. That body is worth logging — the vendor's message is
// often the only account of why a request failed — so this redacts rather than
// fingerprints, unlike bodyFingerprintForLog.
//
// Redaction is by value, against the secrets configured for this service, so it cannot
// miss a form it did not anticipate the way a pattern for "things that look like keys"
// can. It only covers what this deployment can leak: a credential belonging to someone
// else, echoed by an upstream, is not something this function can know about.
//
// Applied before any truncation. Cutting first can leave half a key, which is still a
// key's worth of a key.
func (c *Ctrl) redactUpstreamSecrets(s string) string {
	if s == "" {
		return s
	}
	seen := make(map[string]struct{})
	redact := func(m map[string]string) {
		for _, v := range m {
			if len(v) < 8 {
				// Too short to be a credential, and short strings collide with ordinary
				// words — redacting those would destroy the message this exists to keep.
				continue
			}
			if _, dup := seen[v]; dup {
				continue
			}
			seen[v] = struct{}{}
			s = strings.ReplaceAll(s, v, "[redacted-upstream-secret]")
		}
	}
	redact(c.Service.AdditionalSecret)
	for _, e := range c.Service.ModelPricing {
		redact(e.AdditionalSecret)
	}
	return s
}
