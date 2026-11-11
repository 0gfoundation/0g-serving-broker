package attestproxy

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// What the ingress's socket may quote. Backported from main (0046a41b, e54b649d, 909c6ea5),
// ingress half only.
//
// A quote is a genuine, Intel-signed statement that this CVM produced report_data, and
// whoever chooses those 64 bytes chooses what it says. Forwarding the caller's bytes unread
// let the ingress — dstack-ingress, Phala's image, on the TLS path — obtain a quote over
// `attacker_enc_pub ‖ real_signer ‖ 1 ‖ 0`: DCAP passes, the signer matches the chain, and
// a client sealing to the enc_pub in a verified quote seals its prompt to the ingress.
//
// The ingress's evidence is sha256(sha256sum.txt) padded with zeros, and no layout of ours
// has a zero second half (legacy's ASCII address runs past byte 32, v1 has the signer
// there), so the socket forwards only report_data whose second half is zero. The request
// dstack receives is rebuilt from the validated bytes, so what was judged and what is quoted
// cannot differ.

const (
	pathGetQuote = "/GetQuote"

	// reportDataMax is the TDX report_data size; dstack zero-pads shorter values.
	reportDataMax = 64

	// quoteRequestMax bounds the body read before judging it.
	quoteRequestMax = 4 << 10
)

// guardIngressQuote judges a GetQuote on the read-only socket and, if allowed, rewrites r
// into the canonical request. It answers the caller itself and returns false when the
// request must not be forwarded.
func (p *Proxy) guardIngressQuote(w http.ResponseWriter, r *http.Request) bool {
	rd, err := readQuoteReportData(r)
	if err != nil {
		p.logger.Warnf("[attestproxy] refused %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "malformed GetQuote", http.StatusBadRequest)
		return false
	}
	if err := checkIngressReportData(rd); err != nil {
		p.logger.Warnf("[attestproxy] refused %s %s over report_data %s: %v", r.Method, r.URL.Path, hex.EncodeToString(rd), err)
		http.Error(w, "report_data not allowed", http.StatusForbidden)
		return false
	}
	canonical := hex.EncodeToString(rd)
	if r.Method == http.MethodGet {
		r.URL.RawQuery = url.Values{"report_data": {canonical}}.Encode()
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
// query string (dstack-ingress, via curl) or POST with a JSON body — and refuses anything
// carrying more than that one field.
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

// checkIngressReportData allows any first half and a zero second half.
func checkIngressReportData(rd []byte) error {
	var padded [reportDataMax]byte
	copy(padded[:], rd)
	for _, b := range padded[reportDataMax/2:] {
		if b != 0 {
			return fmt.Errorf("the second half of an ingress report_data must be zero")
		}
	}
	return nil
}
