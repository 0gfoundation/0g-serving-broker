package ctrl

import (
	"crypto/sha256"
	"fmt"
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
func bodyFingerprintForLog(b []byte) string {
	if len(b) == 0 {
		return "len=0"
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("len=%d sha256=%x", len(b), sum[:4])
}
