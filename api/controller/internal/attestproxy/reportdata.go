package attestproxy

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/common/tee"
)

// What a GetQuote may be asked over.
//
// A quote is a genuine, Intel-signed statement that this CVM produced report_data. Whoever
// chooses those 64 bytes chooses what the statement says, so forwarding the caller's bytes
// unread — which is what this proxy did — let anything holding the socket obtain a quote
// over `attacker_enc_pub ‖ real_signer ‖ 1 ‖ 0`. DCAP passes, the signer matches the chain,
// and a client that seals to the enc_pub in a verified quote seals its prompt to the
// attacker. The broker's socket is held by the broker and event images; the ingress's by
// dstack-ingress, which is Phala's image and sits on the TLS path. On dstack 0.5 a reader
// comparing the quote's enc_pub against the RTMR3 image record catches it; on 0.6 there is
// no record, so nothing does (doc/design/report-data-v2.md §4.1).
//
// So the proxy decides the bytes, not the caller:
//
//   - The broker's socket forwards only the layouts an honest broker asks for, computed here
//     from the keys this proxy derives for the current identity: the legacy layout (the
//     signer's address as ASCII) and the v1 layout (enc_pub ‖ signer ‖ version 1). Those
//     are the only two tee.TeeService ever requests.
//   - The ingress's socket forwards only report_data whose second half is zero. Its
//     evidence is sha256(sha256sum.txt) padded with zeros; no layout of ours has a zero
//     second half (legacy's ASCII address runs past byte 32, v1 has the signer there), so
//     it can produce none of them.
//
// Anything else is refused with 403 and logged at warning, like a request for /EmitEvent:
// on a correctly deployed CVM nothing should ever ask.
//
// The request dstack receives is rebuilt from the validated bytes rather than passed on, so
// what was judged and what is quoted cannot differ — no second report_data field, no
// prefix one parser reads and the other skips.

const (
	pathGetQuote = "/GetQuote"

	// reportDataMax is the TDX report_data size. dstack zero-pads shorter values, so two
	// requests that differ only in trailing zeros produce the same quote and are compared
	// padded.
	reportDataMax = 64

	// quoteRequestMax bounds the body read before judging it: a JSON object with one hex
	// field of at most 128 characters, with room to spare.
	quoteRequestMax = 4 << 10
)

// errNotAllowed marks a report_data that parsed but is not one this socket may quote.
type errNotAllowed struct{ reason string }

func (e errNotAllowed) Error() string { return e.reason }

// guardQuote judges a GetQuote and, if it is allowed, rewrites r into the canonical request
// carrying exactly the validated bytes. It answers the caller itself and returns false
// when the request must not be forwarded.
func (p *Proxy) guardQuote(w http.ResponseWriter, r *http.Request) bool {
	rd, err := readQuoteReportData(r)
	if err != nil {
		p.logger.Warnf("[attestproxy] refused %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "malformed GetQuote", http.StatusBadRequest)
		return false
	}

	if p.signing {
		err = p.checkBrokerReportData(r.Context(), rd)
	} else {
		err = checkIngressReportData(rd)
	}
	if err != nil {
		if _, refused := err.(errNotAllowed); refused {
			p.logger.Warnf("[attestproxy] refused %s %s over report_data %s: %v", r.Method, r.URL.Path, hex.EncodeToString(rd), err)
			http.Error(w, "report_data not allowed", http.StatusForbidden)
			return false
		}
		// The allowed set could not be computed. Refuse rather than forward: a quote over
		// bytes nobody checked is the thing this exists to prevent.
		p.fail(w, http.StatusServiceUnavailable, "judging GetQuote: %v", err)
		return false
	}

	canonical := hex.EncodeToString(rd)
	if r.Method == http.MethodGet {
		r.URL.RawQuery = url.Values{"report_data": {canonical}}.Encode()
		// Nothing else travels: dstack's GET handler reads only the query today, and this
		// should not depend on that staying true. The caller's body is drained first, so
		// the connection it arrived on can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, quoteRequestMax))
		_ = r.Body.Close()
		r.Body = http.NoBody
		r.ContentLength = 0
		r.Header.Del("Content-Type")
		r.Header.Del("Transfer-Encoding")
		return true
	}
	body, _ := json.Marshal(map[string]string{"report_data": canonical})
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Del("Transfer-Encoding")
	r.URL.RawQuery = ""
	return true
}

// readQuoteReportData extracts report_data from either shape dstack accepts — GET with a
// query string (dstack-ingress, via curl) or POST with a JSON body (the broker) — and
// refuses anything carrying more than that one field.
func readQuoteReportData(r *http.Request) ([]byte, error) {
	var raw string
	switch r.Method {
	case http.MethodGet:
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return nil, fmt.Errorf("query: %w", err)
		}
		if len(q) != 1 || len(q["report_data"]) != 1 {
			return nil, fmt.Errorf("query must carry exactly one report_data and nothing else")
		}
		raw = q.Get("report_data")
	case http.MethodPost:
		if r.URL.RawQuery != "" {
			return nil, fmt.Errorf("POST must not carry a query string")
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, quoteRequestMax+1))
		if err != nil {
			return nil, fmt.Errorf("body: %w", err)
		}
		if len(body) > quoteRequestMax {
			return nil, fmt.Errorf("body over %d bytes", quoteRequestMax)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			return nil, fmt.Errorf("body is not a JSON object: %w", err)
		}
		if len(fields) != 1 || fields["report_data"] == nil {
			return nil, fmt.Errorf("body must carry exactly one report_data and nothing else")
		}
		if err := json.Unmarshal(fields["report_data"], &raw); err != nil {
			return nil, fmt.Errorf("report_data is not a string: %w", err)
		}
	default:
		return nil, fmt.Errorf("method %s", r.Method)
	}

	// Plain hex only. dstack's own parsing of a "0x" prefix or odd length is not something
	// this should have to agree with.
	if strings.HasPrefix(raw, "0x") || strings.HasPrefix(raw, "0X") {
		return nil, fmt.Errorf("report_data must be plain hex, without 0x")
	}
	rd, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("report_data is not hex: %w", err)
	}
	if len(rd) == 0 || len(rd) > reportDataMax {
		return nil, fmt.Errorf("report_data is %d bytes, want 1..%d", len(rd), reportDataMax)
	}
	return rd, nil
}

// checkBrokerReportData allows exactly the legacy and v1 layouts for the keys of the
// current identity.
//
// One identity snapshot for both keys, for the reason KeyIdentity is one value: a signer
// and an enc_pub from two different identities would be a pair no deployment ever had.
// A change landing between the broker computing its report_data and this check makes the
// check fail; the broker's quote sync fails with it and runs again on the new keys, which
// is the ordering a key rebind already imposes.
func (p *Proxy) checkBrokerReportData(ctx context.Context, rd []byte) error {
	id, err := p.currentKeyIdentity(ctx)
	if err != nil {
		return err
	}
	signerMaterial, err := p.deriveKey(ctx, signerKeyPath(id))
	if err != nil {
		return fmt.Errorf("deriving the signing key: %w", err)
	}
	key, err := SignerKeyFromMaterial(signerMaterial)
	if err != nil {
		return err
	}
	encMaterial, err := p.deriveKey(ctx, encKeyPath(id))
	if err != nil {
		return fmt.Errorf("deriving the enc key: %w", err)
	}
	encPub, err := tee.EncPublicKeyFromMaterial(encMaterial)
	if err != nil {
		return fmt.Errorf("deriving the enc public key: %w", err)
	}

	addr := crypto.PubkeyToAddress(key.PublicKey)
	v1, err := tee.ReportDataV1(encPub, addr)
	if err != nil {
		return err
	}
	got := padReportData(rd)
	for _, allowed := range [][]byte{tee.LegacyReportData(addr), v1} {
		if got == padReportData(allowed) {
			return nil
		}
	}
	return errNotAllowed{"not the legacy or v1 layout for the current keys"}
}

// checkIngressReportData allows any first half and a zero second half.
func checkIngressReportData(rd []byte) error {
	padded := padReportData(rd)
	for _, b := range padded[reportDataMax/2:] {
		if b != 0 {
			return errNotAllowed{"the second half of an ingress report_data must be zero"}
		}
	}
	return nil
}

func padReportData(rd []byte) [reportDataMax]byte {
	var out [reportDataMax]byte
	copy(out[:], rd)
	return out
}
