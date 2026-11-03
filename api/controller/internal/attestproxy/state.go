package attestproxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"

	"github.com/0glabs/0g-serving-broker/common/tee"
)

// report_data v2 (doc/design/report-data-v2.md): enc_pub(32) ‖ SHA-256(state document)(32).
//
// The state document says what this deployment runs now — image and keys, config, the
// permitted upstream set, engines, instances, whether RTMR3 holds a copy — and the quote
// commits to it through report_data, which is how a verifier learns any of it on dstack 0.6,
// where applications cannot append to RTMR3. The controller builds the document (it is the
// only party that knows the state); the broker fetches it here and asks for a quote over it;
// this proxy allows that quote only for the document it would build itself, at that moment,
// so a caller cannot get a quote over a document of its own.

const pathState = "/State"

// StateFunc builds the current state document and reports the key identity it describes.
type StateFunc func(ctx context.Context) ([]byte, KeyIdentity, error)

// SetStateSource enables /State and v2 report_data. Called before Serve.
func (p *Proxy) SetStateSource(fn StateFunc) { p.stateFn = fn }

// serveState returns the current state document, as text.
func (p *Proxy) serveState(w http.ResponseWriter, r *http.Request) {
	if p.stateFn == nil {
		http.NotFound(w, r)
		return
	}
	doc, _, err := p.stateFn(r.Context())
	if err != nil {
		p.fail(w, http.StatusServiceUnavailable, "building the state document: %v", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(doc)
}

// v2ReportData is the v2 report_data for the document as it is now: the enc_pub of the
// identity the document describes, then the document's hash.
//
// The enc_pub is derived here, for the document's identity, rather than taken from the
// document's image line: the proxy vouches for keys it derives, not for text.
func (p *Proxy) v2ReportData(ctx context.Context) ([]byte, error) {
	if p.stateFn == nil {
		return nil, errors.New("no state source")
	}
	doc, id, err := p.stateFn(ctx)
	if err != nil {
		return nil, err
	}
	material, err := p.deriveKey(ctx, encKeyPath(id))
	if err != nil {
		return nil, fmt.Errorf("deriving the enc key: %w", err)
	}
	encPub, err := tee.EncPublicKeyFromMaterial(material)
	if err != nil {
		return nil, err
	}
	if len(encPub) != reportDataMax/2 {
		return nil, fmt.Errorf("enc_pub is %d bytes", len(encPub))
	}
	sum := sha256.Sum256(doc)
	return append(append([]byte{}, encPub...), sum[:]...), nil
}
