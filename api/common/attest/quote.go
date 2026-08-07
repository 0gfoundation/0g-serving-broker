package attest

import (
	"bytes"
	"encoding/hex"
	"fmt"
)

// Byte offsets of the measurements inside a raw TDX v4 quote, into the TD report
// body that follows the quote header.
//
// Read off a real production quote and checked field by field against the same
// report's tcb_info, rather than derived from the structure definitions — see the
// golden test. They are fixed by the v4 quote format; a v5 quote would need its
// own offsets, and would fail the tcb_info comparison rather than mis-read
// quietly, since nothing else lands on these boundaries.
const (
	offsetMRTD        = 184
	offsetMRConfigID  = 232
	offsetRTMR0       = 376
	measurementLen    = 48 // SHA-384, the digest size TDX measurement registers hold
	rtmrStride        = 48 // rtmr0..rtmr3 are adjacent
	rtmrCount         = 4
	composeHashLen    = 32 // SHA-256, what mr_config_id carries in a 48-byte field
	mrConfigVersionV1 = 1
	mrConfigVersionV2 = 2
)

// measurement copies measurementLen bytes out of a quote.
//
// A copy, not a subslice: these are handed to callers who compare and print them,
// and aliasing the quote would let one of them corrupt the buffer every other
// reading comes from.
func measurement(quote []byte, offset int, name string) ([]byte, error) {
	if len(quote) < offset+measurementLen {
		return nil, fmt.Errorf("quote is %d bytes, too short to hold %s at offset %d", len(quote), name, offset)
	}
	return bytes.Clone(quote[offset : offset+measurementLen]), nil
}

// MRTD returns the measurement of the virtual firmware.
func MRTD(quote []byte) ([]byte, error) {
	return measurement(quote, offsetMRTD, "mrtd")
}

// MRConfigID returns the 48-byte configuration identity the host supplied when
// the TD was built. For dstack this is where the compose hash lives; see
// ComposeHashFromMRConfigID.
func MRConfigID(quote []byte) ([]byte, error) {
	return measurement(quote, offsetMRConfigID, "mr_config_id")
}

// RTMR returns runtime measurement register i, for i in [0, 3].
//
// RTMR0-2 hold what the firmware and kernel measured at boot. RTMR3 is the one an
// application extends: see ReplayRTMR3.
func RTMR(quote []byte, i int) ([]byte, error) {
	if i < 0 || i >= rtmrCount {
		return nil, fmt.Errorf("rtmr index %d out of range [0, %d]", i, rtmrCount-1)
	}
	return measurement(quote, offsetRTMR0+i*rtmrStride, fmt.Sprintf("rtmr%d", i))
}

// ComposeHashFromMRConfigID returns the hex compose hash a dstack mr_config_id
// carries.
//
// This is the reason compose_hash can be trusted without reading the event log:
// mr_config_id is part of the TD report the quote signs, so the hash comes out of
// hardware-bound, signed bytes. dstack also writes a compose-hash event into
// RTMR3, but an application can emit an event by that name too — the register is
// the statement that cannot be forged.
//
// The layout is a version byte, the hash, then zero padding. V2 replaces the hash
// with keccak256(compose_hash ‖ app_id ‖ key_provider_kind ‖ key_provider_id),
// which cannot be inverted: recognising a V2 report needs those three values from
// the caller, so it is refused rather than guessed at. Production is V1 today.
func ComposeHashFromMRConfigID(mrConfigID []byte) (string, error) {
	if len(mrConfigID) != measurementLen {
		return "", fmt.Errorf("mr_config_id is %d bytes, want %d", len(mrConfigID), measurementLen)
	}

	switch version := mrConfigID[0]; version {
	case mrConfigVersionV1:
		// The padding is checked because a nonzero tail means the field is not
		// laid out the way this assumes, and the 32 bytes read as a compose hash
		// would then be part of something else entirely.
		if padding := mrConfigID[1+composeHashLen:]; !allZero(padding) {
			return "", fmt.Errorf("mr_config_id v1 has %x after the compose hash, want zero padding", padding)
		}
		return hex.EncodeToString(mrConfigID[1 : 1+composeHashLen]), nil
	case mrConfigVersionV2:
		return "", fmt.Errorf("mr_config_id is v%d, which commits to the compose hash through keccak256(compose_hash‖app_id‖kp_kind‖kp_id); the compose hash cannot be recovered from the quote alone", version)
	default:
		return "", fmt.Errorf("mr_config_id version %d is not recognised", version)
	}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
