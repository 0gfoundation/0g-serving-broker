// Package tapp reads and verifies the attestation evidence a 0g-tapp
// tapp-server hands out (`GetEvidence`), without any help from the party
// being verified or from a shared attestation service.
//
// The broker uses it to bootstrap the TLS pin of tappscan itself (assay repo
// docs/spml-design.md §7.6): the one key the attestation loop could not
// otherwise obtain from an independent root. The chain of trust is
//
//	TappRegistry.getNodeList(app) → the admitted signer and its teeUrl
//	GetEvidence(app, nonce) at that teeUrl → {quote, cc_eventlog, runtime_data}
//	Intel-signed TDX quote (go-tdx-guest, PCK chain to Intel's root)
//	report_data == sha512(runtime_data)  → runtime_data is what the TD said
//	runtime_data.signer == the chain's signer, nonce echoed
//	cc_eventlog replays to the quote's RTMRs → the boot chain is what the log says
//	the log's UKI digest ∈ the public reference values → genuine tapp image
//	→ runtime_data.tls_public_key is the key of a genuine tapp node for that app
//
// Only the last line is new trust; everything above it is Intel, the chain
// and public reference values, which the broker already relies on.
package tapp

import (
	"context"
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protowire"
)

// Evidence is the decoded `GetEvidenceResponse.evidence` JSON:
// {"quote": b64, "cc_eventlog": b64, "runtime_data": b64, ...}.
type Evidence struct {
	Quote       []byte
	EventLog    []byte
	RuntimeData []byte // the exact bytes report_data commits to; never re-serialised
}

// RuntimeData is what `runtime_data` says. Empty fields are absent on the
// wire, not "" (tapp-common/src/report_data.rs).
type RuntimeData struct {
	Nonce        string `json:"nonce"`
	Signer       string `json:"signer"`
	TLSPublicKey string `json:"tls_public_key"`
}

// ParseEvidence decodes the evidence JSON. A response without runtime_data
// predates the challenge field (tapp < 0.4.0) and is refused: without it a
// quote names no signer and echoes no nonce.
func ParseEvidence(raw []byte) (*Evidence, error) {
	var j struct {
		Quote       string `json:"quote"`
		EventLog    string `json:"cc_eventlog"`
		RuntimeData string `json:"runtime_data"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("evidence is not JSON: %w", err)
	}
	if j.RuntimeData == "" {
		return nil, errors.New("evidence has no runtime_data: this tapp-server predates the challenge field")
	}
	var e Evidence
	var err error
	if e.Quote, err = base64.StdEncoding.DecodeString(j.Quote); err != nil {
		return nil, fmt.Errorf("quote b64: %w", err)
	}
	if e.EventLog, err = base64.StdEncoding.DecodeString(j.EventLog); err != nil {
		return nil, fmt.Errorf("cc_eventlog b64: %w", err)
	}
	if e.RuntimeData, err = base64.StdEncoding.DecodeString(j.RuntimeData); err != nil {
		return nil, fmt.Errorf("runtime_data b64: %w", err)
	}
	if len(e.Quote) == 0 || len(e.EventLog) == 0 || len(e.RuntimeData) == 0 {
		return nil, errors.New("evidence is missing quote, cc_eventlog or runtime_data")
	}
	return &e, nil
}

// Runtime decodes the runtime_data object (for reading; the bytes are hashed as-is).
func (e *Evidence) Runtime() (RuntimeData, error) {
	var r RuntimeData
	if err := json.Unmarshal(e.RuntimeData, &r); err != nil {
		return r, fmt.Errorf("runtime_data is not JSON: %w", err)
	}
	return r, nil
}

// ReportData is what the quote's 64-byte report_data must equal: sha512 over
// the runtime_data bytes exactly as transmitted.
func ReportData(runtimeData []byte) [64]byte {
	return sha512.Sum512(runtimeData)
}

// rawCodec lets grpc carry pre-encoded protobuf bytes, so this package needs
// no generated stubs for a single two-field request.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) {
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("rawCodec: want []byte, got %T", v)
	}
	return b, nil
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("rawCodec: want *[]byte, got %T", v)
	}
	*p = append([]byte(nil), data...)
	return nil
}

func (rawCodec) Name() string { return "proto" }

// FetchEvidence calls TappService/GetEvidence at teeURL (as the TappRegistry
// lists it: http://host:50051 or https://host:50052) and returns the raw
// evidence JSON. The transport is deliberately unauthenticated: the evidence
// authenticates itself (Intel signature + nonce), so a man in the middle can
// only withhold it, and the server's own TLS key is not knowable in advance —
// pinning it here would be the same circularity this package exists to break.
func FetchEvidence(ctx context.Context, teeURL, appID string, nonce []byte) ([]byte, error) {
	u, err := url.Parse(teeURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("teeUrl %q: not a URL with a host", teeURL)
	}
	var creds credentials.TransportCredentials
	switch strings.ToLower(u.Scheme) {
	case "https":
		creds = credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}) //nolint:gosec // see the doc comment: the payload is self-authenticating
	case "http", "":
		creds = insecure.NewCredentials()
	default:
		return nil, fmt.Errorf("teeUrl %q: unsupported scheme", teeURL)
	}
	conn, err := grpc.NewClient(u.Host, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc client for %s: %w", u.Host, err)
	}
	defer conn.Close()

	// GetEvidenceRequest { string app_id = 1; bytes nonce = 2; }
	var req []byte
	req = protowire.AppendTag(req, 1, protowire.BytesType)
	req = protowire.AppendString(req, appID)
	if len(nonce) > 0 {
		req = protowire.AppendTag(req, 2, protowire.BytesType)
		req = protowire.AppendBytes(req, nonce)
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var resp []byte
	if err := conn.Invoke(cctx, "/tapp_service.TappService/GetEvidence", req, &resp, grpc.ForceCodec(rawCodec{})); err != nil {
		return nil, fmt.Errorf("GetEvidence at %s: %w", u.Host, err)
	}
	return parseEvidenceResponse(resp)
}

// parseEvidenceResponse reads GetEvidenceResponse
// { bool success = 1; string message = 2; bytes evidence = 3; ... }.
func parseEvidenceResponse(b []byte) ([]byte, error) {
	var success bool
	var message string
	var evidence []byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("GetEvidenceResponse: %w", protowire.ParseError(n))
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			success, b = v != 0, b[m:]
		case num == 2 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			message, b = v, b[m:]
		case num == 3 && typ == protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			evidence, b = append([]byte(nil), v...), b[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			b = b[m:]
		}
	}
	if !success {
		return nil, fmt.Errorf("GetEvidence refused: %s", message)
	}
	if len(evidence) == 0 {
		return nil, errors.New("GetEvidence returned no evidence")
	}
	return evidence, nil
}
