package attest

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// report_data, and only report_data.
//
// Everything else a verifier needs out of a quote — the DCAP signature, the measurement
// registers, mr_config_id's compose hash, the RTMR3 replay, the OS image hash, the TCB status —
// comes from dstack-verifier, whose /verify endpoint runs the same process the dstack KMS runs
// before it releases a CVM's keys. Reimplementing any of it here would mean keeping a second,
// narrower copy correct in parallel with theirs.
//
// report_data is the exception because its 64 bytes carry a layout dstack knows nothing about:
// the enclave encryption key and the response signer, packed by this project (0g-pc SPEC §4.2).
// The verifier hands the bytes back untouched, and reading them is ours.

// reportDataLen is the fixed size of report_data. A hardware limit, not a buffer.
const reportDataLen = 64

// report_data field offsets in the §4.2 layout:
//
//	0   32  enc_pub      X25519 recipient key
//	32  20  signer_addr  secp256k1 Ethereum address, raw bytes
//	52   4  version      uint32 big-endian, 1
//	56   8  reserved     zero
const (
	reportDataSignerOffset  = 32
	reportDataVersionOffset = 52
	reportDataLayoutVersion = 1
)

// addressPattern is a lowercase 20-byte hex address.
var addressPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// EncPubFromReportData returns the enclave encryption public key the quote binds, hex.
//
// Only the §4.2 layout carries one; the older layout is the ASCII signer address and nothing
// else, so it answers "". A caller that intends to SEAL a request must have a non-empty value
// here and must have checked it against the ledger — sealing to a key the ledger does not
// vouch for hands the plaintext to whatever chose report_data, and no later signature check
// can undo that.
func EncPubFromReportData(reportData []byte) (string, error) {
	if len(reportData) != reportDataLen {
		return "", fmt.Errorf("report_data is %d bytes, want %d", len(reportData), reportDataLen)
	}
	if binary.BigEndian.Uint32(reportData[reportDataVersionOffset:reportDataVersionOffset+4]) != reportDataLayoutVersion {
		return "", nil
	}
	pub := reportData[:reportDataSignerOffset]
	if bytes.Equal(pub, make([]byte, len(pub))) {
		return "", fmt.Errorf("report_data names an all-zero enc_pub")
	}
	return hex.EncodeToString(pub), nil
}

// SignerFromReportData returns the signer address the quote binds, lowercase "0x…".
//
// The hardware signed over it — but the enclave chose what to put there, so on its own it says
// only "this TD asked for this address", not "this address belongs to the code that is
// running". Binding it to an image is what ResolveRunningState does with it.
//
// Two layouts exist because the broker publishes two quotes. The §4.2 layout is recognised by
// its version field; anything else is read as the older form, where report_data is the ASCII
// address zero-padded to 64 bytes.
func SignerFromReportData(reportData []byte) (string, error) {
	if len(reportData) != reportDataLen {
		return "", fmt.Errorf("report_data is %d bytes, want %d", len(reportData), reportDataLen)
	}

	if binary.BigEndian.Uint32(reportData[reportDataVersionOffset:reportDataVersionOffset+4]) == reportDataLayoutVersion {
		addr := reportData[reportDataSignerOffset : reportDataSignerOffset+20]
		if bytes.Equal(addr, make([]byte, 20)) {
			return "", fmt.Errorf("report_data names the zero address")
		}
		return "0x" + hex.EncodeToString(addr), nil
	}

	// Legacy: the ASCII hex address, which the hardware zero-pads.
	ascii := strings.ToLower(string(bytes.TrimRight(reportData, "\x00")))
	if !addressPattern.MatchString(ascii) {
		return "", fmt.Errorf("report_data %q is neither the §4.2 layout nor an ASCII address", ascii)
	}
	return ascii, nil
}
