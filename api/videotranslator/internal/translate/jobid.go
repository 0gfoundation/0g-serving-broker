package translate

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// The job id the broker hands a client is a published contract, not an opaque
// vendor string: consumers persist it and key on it. The 0G Router folds it into
// its billing idempotency key (usage_logs.request_id, varchar(64) UNIQUE), which
// leaves a hard ceiling — see the "Job id contract" section of
// docs/design/video-generation-async-billing.md.
//
//	id ≤ 36 characters from [A-Za-z0-9_-]
//
// Vendors do not honour that, and passing their task_id through verbatim makes
// their id-shaping decision our published API. Shaping the id is protocol
// translation, so it belongs here rather than being enforced by rejection further
// down, where a vendor whose ids are too long would simply be undeployable.
//
// What this does NOT buy: encoding runs after the vendor's create call has already
// returned, so when EncodeJobID cannot map an id, the vendor has already accepted
// the job and will bill for it. The id survives only in the error log. The win is
// that the failure is immediate, local, and names the id — not that the clip is
// saved. Nor is "it fails on the vendor's first request" a guarantee: a vendor
// whose id shape varies by model, region, or API version can pass staging and fail
// in production.
const (
	// MaxJobIDLen is the contract's ceiling on a PUBLISHED id. It is not the budget
	// for a vendor id: a tag costs three of the 36, so a passthrough carries 33 and
	// an arbitrary-byte id only 24 once base64'd. Ask EncodeJobID rather than
	// comparing a vendor id against this.
	MaxJobIDLen = 36

	// Tags are self-describing so DecodeJobID needs no state and no guessing. Three
	// characters keeps ids readable (v0_425080991981768) at the cost of three of the
	// 36; the alternative — passing short ids through untagged — would make a
	// transformed id indistinguishable from a raw one that happened to look like it.
	tagRaw    = "v0_" // payload is the vendor id verbatim
	tagUUID   = "v1_" // payload is a canonical UUID with its hyphens removed
	tagBase64 = "v2_" // payload is base64url(vendor id), unpadded
	// Adding a tag is a TWO-PHASE deploy: ship the DecodeJobID case to every replica
	// first, and only enable it in EncodeJobID once all of them can decode it.
	// Otherwise a job created by an upgraded replica is unpollable on one that is
	// not, and a rollback strands every id in flight — after the vendor has billed
	// for the clip. Never remove or reuse a tag for the same reason.

	maxPayloadLen = MaxJobIDLen - 3
)

// EncodeJobID maps a vendor task id into the published contract, reversibly and
// without state. It tries, in order: pass the id through (most vendors, most
// readable), compact a canonical UUID by dropping its hyphens, then base64url.
//
// It returns an error when no encoding fits. That is a real limit, not an
// oversight: a stateless reversible mapping into 33 payload characters can carry
// at most 24 arbitrary bytes, so a vendor with longer unstructured ids needs
// either structure this function can exploit (add a case here when onboarding it)
// or a stateful mapping in the broker. Failing here means failing at that vendor's
// FIRST request, with the offending id named — not silently in production.
func EncodeJobID(vendorID string) (string, error) {
	if vendorID == "" {
		return "", fmt.Errorf("vendor returned an empty job id")
	}
	if vendorID == "." || vendorID == ".." {
		// Encodes fine (v2_Lg / v2_Li4) but DecodeJobID rejects it, so without this
		// the create would succeed and every poll 4xx until MaxPollDuration. Fail at
		// create, like every other id this function cannot carry.
		return "", fmt.Errorf("vendor job id %q is a path segment, not a task id", vendorID)
	}

	if len(vendorID) <= maxPayloadLen && isContractCharset(vendorID) {
		return tagRaw + vendorID, nil
	}

	if hex32, ok := compactUUID(vendorID); ok {
		return tagUUID + hex32, nil
	}

	if encoded := base64.RawURLEncoding.EncodeToString([]byte(vendorID)); len(encoded) <= maxPayloadLen {
		return tagBase64 + encoded, nil
	}

	return "", fmt.Errorf("vendor job id %q (%d bytes) cannot be encoded into the %d-character contract; "+
		"onboarding this vendor needs an id-specific case in EncodeJobID or a stateful mapping",
		vendorID, len(vendorID), MaxJobIDLen)
}

// DecodeJobID recovers the vendor task id from a published id. Every request after
// create arrives in the published form (the broker stores and replays exactly what
// it was given), so this runs on every poll and every content fetch.
//
// An UNTAGGED id is treated as a pre-EncodeJobID vendor id and passed through. That
// is deliberate, not lax: this translator shipped before tagging existed, so ids it
// already handed out carry no tag, and rejecting them would strand every in-flight
// job — the broker's poller treats the resulting 4xx as retryable, so such a job
// would spin until MaxPollDuration, never bill, and lose the signature its client
// holds a key for. Passing them through is exactly what this code did before, and
// the vendor rejects the id if it is junk.
//
// Every path validates what it recovers, because the decoded value is spliced into
// a vendor URL carrying our account's credentials. The clients PathEscape it, which
// handles separators — but not a bare "..", which stays a live path segment, nor an
// empty id, which turns a vendor's item endpoint into its collection endpoint.
func DecodeJobID(publicID string) (string, error) {
	switch {
	case strings.HasPrefix(publicID, tagRaw):
		// EncodeJobID only ever emits a contract-charset payload here, so requiring
		// one costs nothing and rejects a hand-crafted "v0_..".
		return checkedVendorID(publicID, strings.TrimPrefix(publicID, tagRaw), true)

	case strings.HasPrefix(publicID, tagUUID):
		return expandUUID(strings.TrimPrefix(publicID, tagUUID))

	case strings.HasPrefix(publicID, tagBase64):
		// Strict: without it, non-zero trailing bits are accepted, so v2_Lg and
		// v2_Lh both decode to ".". Harmless today (the ownership row is keyed on
		// the exact published id and fails closed on a miss), but "one published id
		// per vendor id" is cheaper to keep than to re-derive later.
		raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(publicID, tagBase64))
		if err != nil {
			return "", fmt.Errorf("job id %q has a malformed payload: %w", publicID, err)
		}
		// NOT charset-checked: the base64 tag exists precisely because the vendor id
		// is not contract-clean. Only the shapes that would change the vendor URL's
		// meaning are rejected.
		return checkedVendorID(publicID, string(raw), false)
	}

	// A vN_ shape we do not know is NOT a legacy id — it is one this build cannot
	// decode, from a replica that is ahead of us (or from a tag we rolled back).
	// Passing it through would hand the vendor a task id it never issued: a 404 that
	// the broker's poller treats as retryable, so the job spins to MaxPollDuration,
	// never bills, and drops the signature its client holds a key for — exactly the
	// failure the legacy passthrough exists to prevent, relocated to the rollback
	// direction. Fail here instead, where the message names the cause. No vendor
	// mints ids of this shape (numeric, canonical UUID).
	if unknownTag.MatchString(publicID) {
		return "", fmt.Errorf("job id %q carries a tag this build cannot decode — it was issued by a newer translator; see the two-phase deploy rule above", publicID)
	}

	return checkedVendorID(publicID, publicID, true)
}

// unknownTag matches the tag shape this package mints, so a tag from a future build
// is distinguishable from a pre-tagging vendor id rather than silently forwarded.
var unknownTag = regexp.MustCompile(`^v[0-9]+_`)

// checkedVendorID rejects a recovered id that must never reach a vendor URL. It is
// the one gate between a client-shaped path segment and a request carrying our
// account's credentials, so it runs on every decode path.
func checkedVendorID(publicID, vendorID string, requireContractCharset bool) (string, error) {
	if vendorID == "" {
		return "", fmt.Errorf("job id %q recovers an empty vendor id", publicID)
	}
	if vendorID == "." || vendorID == ".." {
		// url.PathEscape leaves these intact and they remain live path segments, so
		// they would walk the vendor's URL rather than name a task under it.
		return "", fmt.Errorf("job id %q recovers a path segment (%q), not a task id", publicID, vendorID)
	}
	if len(vendorID) > MaxJobIDLen {
		// Bounded here rather than trusting the caller's schema: this is a decoder for
		// client-shaped input, and it should be self-contained.
		return "", fmt.Errorf("job id %q recovers a %d-character vendor id, over the contract's %d", publicID, len(vendorID), MaxJobIDLen)
	}
	if requireContractCharset && !isContractCharset(vendorID) {
		return "", fmt.Errorf("job id %q is not a well-formed job id", publicID)
	}
	return vendorID, nil
}

// isContractCharset reports whether s is drawn from the contract's [A-Za-z0-9_-].
func isContractCharset(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// compactUUID turns "8-4-4-4-12" hex into its 32 hex characters. DashScope's task
// ids are canonical UUIDs and land at exactly 36 characters — over budget once the
// tag is added — so this is what keeps them representable at all.
func compactUUID(s string) (string, bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return "", false
	}
	stripped := s[:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	if _, err := hex.DecodeString(stripped); err != nil {
		return "", false
	}
	return stripped, true
}

// expandUUID is compactUUID's inverse.
func expandUUID(hex32 string) (string, error) {
	if len(hex32) != 32 {
		return "", fmt.Errorf("uuid payload %q is not 32 hex characters", hex32)
	}
	if _, err := hex.DecodeString(hex32); err != nil {
		return "", fmt.Errorf("uuid payload %q is not hex: %w", hex32, err)
	}
	return hex32[:8] + "-" + hex32[8:12] + "-" + hex32[12:16] + "-" + hex32[16:20] + "-" + hex32[20:], nil
}
