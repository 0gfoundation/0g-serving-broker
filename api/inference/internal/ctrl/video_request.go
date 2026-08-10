package ctrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// CtxKeyVideoReserveFee carries the pre-flight reserve computed for a video
// create — the exact fee for the duration and resolution the broker itself
// wrote into the forwarded request. deferVideoBillingToPoll reads it to stamp
// the in-flight reserve onto the requests row (see #628 A4); nothing else may.
const CtxKeyVideoReserveFee = "videoReserveFee"

// videoFieldSeconds / videoFieldResolution are the two request fields the broker
// AUTHORS. seconds is the OpenAI Video API field; resolution is the explicit tier
// selector the translator honours over anything it would otherwise derive (a
// pixel-dimension "size" only sets the aspect ratio, and each vendor's own
// default tier differs).
const (
	videoFieldSeconds    = "seconds"
	videoFieldResolution = "resolution"
	videoFieldSize       = "size"
)

// maxVideoSecondsFieldBytes bounds the "seconds" value the broker will read.
// Anything longer is REFUSED rather than truncated: a prefix that parses as a
// number is exactly how the broker and the upstream came to read two different
// durations out of one field (issue #628, variant 5). No legitimate duration is
// anywhere near this long.
const maxVideoSecondsFieldBytes = 64

// ErrVideoSecondsUnreadable is returned when a create request carries a
// "seconds" the broker cannot read to a single unambiguous number. It is a
// client error: the broker refuses rather than falling back to a value the
// vendor may not agree with.
var ErrVideoSecondsUnreadable = errors.New("invalid video request: 'seconds' must be a positive number")

// ErrVideoRequestMalformed is returned when the create body itself cannot be
// parsed — no multipart boundary, a truncated form, a body that is neither
// multipart nor a JSON object. Same stance as above: a request the broker cannot
// read is a request it cannot price, so it is refused rather than forwarded.
var ErrVideoRequestMalformed = errors.New("invalid video request: body could not be parsed")

// IsClientVideoRequestError reports whether an AuthorVideoRequest failure was the
// CALLER's fault. Everything else it can return — a pricing-feed outage, a broken
// per-model config — is a broker fault that must stay visible to the broker alert
// rather than being suppressed as an ordinary bad request.
func IsClientVideoRequestError(err error) bool {
	return errors.Is(err, ErrVideoSecondsUnreadable) || errors.Is(err, ErrVideoRequestMalformed)
}

// videoRequestAuthoring holds the resolved billing inputs for one create.
type videoRequestAuthoring struct {
	// seconds is what the broker writes upstream and prices.
	seconds int64
	// resolution is the tier written upstream, "" when the model configures no
	// defaultResolution and the client named no token (nothing is authored then).
	resolution string
	// priceResolution is the token the fee is computed against: the authored
	// resolution when there is one, else the client's raw "size" (the pre-#628
	// basis, kept so an unconfigured deployment bills exactly as it did).
	priceResolution string
}

// AuthorVideoRequest rewrites a video create request so the duration and
// resolution the broker prices are the ones the vendor renders BY CONSTRUCTION,
// then returns the exact pre-flight reserve for them.
//
// This replaces predicting the upstream's reading of the client's body. Two
// parsers over one request produced five divergences, each of which under-reserved
// (see issue #628); one parser that WRITES the field cannot diverge from itself.
// The three rules are:
//
//	seconds unreadable -> ErrVideoSecondsUnreadable (400)
//	seconds absent     -> the model's configured default is written, and priced
//	seconds present    -> normalised into the model's range, written back, priced
//
// The resolution is authored the same way: a "size" that is already a resolution
// token is honoured; pixel dimensions are left alone (every supported vendor reads
// them as an aspect ratio) and the model's defaultResolution is written as an
// explicit tier alongside them.
//
// The caller must have resolved the request model onto the context first
// (ResolveModelForBilling), since both the authoring range and the price are
// per-model. Returns the body to forward and the reserve as a decimal fee string.
func (c *Ctrl) AuthorVideoRequest(ctx *gin.Context, reqBody []byte) ([]byte, string, error) {
	if len(reqBody) == 0 {
		// Nothing to author into and nothing the upstream can render: it will
		// reject the create itself, so no clip is produced and no fee is owed.
		return reqBody, "0", nil
	}

	var billing *config.BillingConfig
	if c.Service.HasMultiModelPricing() {
		if e := c.resolveModelPricing(ctx); e != nil {
			billing = e.Billing
		}
	}

	contentType := ctx.Request.Header.Get("Content-Type")
	isMultipart := strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/")

	var (
		body []byte
		auth videoRequestAuthoring
		err  error
	)
	if isMultipart {
		body, auth, err = authorMultipartVideoRequest(reqBody, contentType, billing)
	} else {
		body, auth, err = authorJSONVideoRequest(reqBody, billing)
	}
	if err != nil {
		return nil, "", err
	}

	prices, err := c.GetBillingPrices(ctx)
	if err != nil {
		return nil, "", errors.Wrap(err, "get billing prices for video reserve")
	}
	units := c.videoOutputUnits(ctx, auth.seconds, auth.priceResolution)
	fee, err := util.Multiply(prices.OutputPrice, units)
	if err != nil {
		return nil, "", errors.Wrap(err, "calculate video reserve fee")
	}

	c.logger.Debugf("video request authored: seconds=%d resolution=%q units=%d reserve=%s",
		auth.seconds, auth.resolution, units, fee.String())
	return body, fee.String(), nil
}

// resolveVideoAuthoring turns the (seconds, size) a client sent into the values
// the broker will write and price. requestedSeconds is 0 when the client named
// none; present-but-unreadable is refused by the caller before reaching here.
func resolveVideoAuthoring(requestedSeconds int64, size string, billing *config.BillingConfig) videoRequestAuthoring {
	var defaultResolution string
	if billing != nil {
		defaultResolution = strings.TrimSpace(billing.DefaultResolution)
	}
	auth := videoRequestAuthoring{
		seconds:    billing.NormalizeVideoSeconds(requestedSeconds),
		resolution: authoredVideoResolution(size, defaultResolution),
	}
	auth.priceResolution = auth.resolution
	if auth.priceResolution == "" {
		auth.priceResolution = size
	}
	return auth
}

// authoredVideoResolution picks the resolution token to write upstream.
//
// A "size" that is not WIDTHxHEIGHT is already a resolution token — MiniMax
// ("2K", "1080P", …) and DashScope ("720P"/"1080P") both honour one — so it is
// echoed back as the authored tier. Pixel dimensions are NOT a tier anywhere:
// MiniMax reads them only as an aspect ratio and renders its deployment default,
// DashScope snaps them to its own two-tier enum. Both cases therefore get the
// model's configured defaultResolution written explicitly, which is what makes
// the rendered tier knowable at gate time.
//
// Returns "" when there is nothing to author (no token from the client, no
// configured default) — the caller then leaves the request alone.
func authoredVideoResolution(size, defaultResolution string) string {
	s := strings.TrimSpace(size)
	if s == "" {
		return defaultResolution
	}
	if _, _, ok := parsePixelSize(s); ok {
		return defaultResolution
	}
	return s
}

// parsePixelSize reports whether a "size" is a WIDTHxHEIGHT pixel dimension
// (e.g. "1280x720"), matching how the translator's own parseSize reads it.
func parsePixelSize(size string) (width, height int, ok bool) {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// parseRequestedSeconds reads a client-supplied duration. An empty value means
// "not named" (0, nil) — both transports treat a blank field that way, matching
// what the upstream does with one. Anything else that is not a finite positive
// number is refused rather than reinterpreted.
func parseRequestedSeconds(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	if len(raw) > maxVideoSecondsFieldBytes {
		return 0, ErrVideoSecondsUnreadable
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || !(f > 0) || math.IsInf(f, 0) || f > float64(maxVideoOutputUnits) {
		return 0, ErrVideoSecondsUnreadable
	}
	return int64(math.Ceil(f)), nil
}

// authorJSONVideoRequest rewrites a JSON create body. Decoded with UseNumber so
// every field the broker does not touch survives the round trip unmangled.
func authorJSONVideoRequest(reqBody []byte, billing *config.BillingConfig) ([]byte, videoRequestAuthoring, error) {
	var bodyMap map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(reqBody))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil || bodyMap == nil {
		// Neither multipart nor a JSON object: there is no field to author into,
		// and the upstream (which decodes this body into a struct) will reject it
		// too. Refuse here rather than forward a request whose fee is unknowable.
		return nil, videoRequestAuthoring{}, ErrVideoRequestMalformed
	}

	rawSeconds, err := jsonFieldAsNumberString(bodyMap[videoFieldSeconds])
	if err != nil {
		return nil, videoRequestAuthoring{}, err
	}
	requested, err := parseRequestedSeconds(rawSeconds)
	if err != nil {
		return nil, videoRequestAuthoring{}, err
	}
	size, _ := bodyMap[videoFieldSize].(string)

	auth := resolveVideoAuthoring(requested, size, billing)
	bodyMap[videoFieldSeconds] = json.Number(strconv.FormatInt(auth.seconds, 10))
	if auth.resolution != "" {
		bodyMap[videoFieldResolution] = auth.resolution
	}

	out, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, videoRequestAuthoring{}, errors.Wrap(err, "re-marshal video request body")
	}
	return out, auth, nil
}

// jsonFieldAsNumberString renders a decoded JSON value as the raw duration
// string. Only a JSON number (or an absent/null field) is accepted — the
// upstream decodes this field into a json.Number, which rejects every other
// shape, so accepting more here would be the broker reading a request the
// upstream will not.
func jsonFieldAsNumberString(v interface{}) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case json.Number:
		return t.String(), nil
	default:
		return "", ErrVideoSecondsUnreadable
	}
}

// multipartPart is one part of a create request, captured verbatim so parts the
// broker does not author (prompt, model, an input_reference file) are re-encoded
// byte-for-byte.
type multipartPart struct {
	header textproto.MIMEHeader
	data   []byte
	name   string
	isFile bool
}

// authorMultipartVideoRequest rewrites a multipart/form-data create body: it
// drops every existing "seconds"/"resolution" part and appends the broker's own,
// leaving all other parts and the boundary untouched.
//
// It parses with a real MIME reader rather than scanning for the field name.
// A scan cannot tell a form field from the same bytes appearing inside a prompt
// value, and these two fields decide the fee.
func authorMultipartVideoRequest(reqBody []byte, contentType string, billing *config.BillingConfig) ([]byte, videoRequestAuthoring, error) {
	boundary, err := multipartBoundary(contentType)
	if err != nil {
		return nil, videoRequestAuthoring{}, err
	}
	parts, err := readMultipartParts(reqBody, boundary)
	if err != nil {
		return nil, videoRequestAuthoring{}, errors.Wrapf(ErrVideoRequestMalformed, "parse multipart video request: %v", err)
	}

	var (
		rawSeconds string
		seenSecs   bool
		size       string
		kept       = make([]multipartPart, 0, len(parts)+1)
	)
	for _, p := range parts {
		switch p.name {
		case videoFieldSeconds:
			// A file part named "seconds", or a value longer than any real
			// duration, is not something the broker can read to its end and agree
			// with the upstream about. Refuse.
			if p.isFile || len(p.data) > maxVideoSecondsFieldBytes {
				return nil, videoRequestAuthoring{}, ErrVideoSecondsUnreadable
			}
			// First value wins, matching r.FormValue on the upstream side — though
			// every one of them is dropped and replaced below, so the ambiguity does
			// not survive into the forwarded request.
			if !seenSecs {
				rawSeconds, seenSecs = string(p.data), true
			}
		case videoFieldResolution:
			// Broker-authored: a client-supplied value would select a tier the
			// reserve did not price. Dropped, never forwarded.
		default:
			if p.name == videoFieldSize && !p.isFile && size == "" {
				size = string(p.data)
			}
			kept = append(kept, p)
		}
	}

	requested, err := parseRequestedSeconds(rawSeconds)
	if err != nil {
		return nil, videoRequestAuthoring{}, err
	}
	auth := resolveVideoAuthoring(requested, size, billing)

	kept = append(kept, multipartPart{
		header: formValueHeader(videoFieldSeconds),
		data:   []byte(strconv.FormatInt(auth.seconds, 10)),
		name:   videoFieldSeconds,
	})
	if auth.resolution != "" {
		kept = append(kept, multipartPart{
			header: formValueHeader(videoFieldResolution),
			data:   []byte(auth.resolution),
			name:   videoFieldResolution,
		})
	}

	out, err := encodeMultipartParts(kept, boundary)
	if err != nil {
		return nil, videoRequestAuthoring{}, errors.Wrap(err, "re-encode video request body")
	}
	return out, auth, nil
}

// multipartBoundary extracts the boundary from a multipart Content-Type.
func multipartBoundary(contentType string) (string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", errors.Wrapf(ErrVideoRequestMalformed, "parse multipart content type: %v", err)
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", errors.Wrap(ErrVideoRequestMalformed, "multipart video request has no boundary")
	}
	return boundary, nil
}

// readMultipartParts reads every part of a multipart body into memory. It uses
// NextRawPart, not NextPart, so a part declaring a transfer encoding is captured
// as the bytes actually on the wire and re-encodes identically.
func readMultipartParts(body []byte, boundary string) ([]multipartPart, error) {
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var parts []multipartPart
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			return parts, nil
		}
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(p)
		closeErr := p.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		parts = append(parts, multipartPart{
			header: p.Header,
			data:   data,
			name:   p.FormName(),
			isFile: p.FileName() != "",
		})
	}
}

// encodeMultipartParts re-encodes parts under the ORIGINAL boundary, so the
// request's Content-Type header (which the proxy copies from the client) still
// describes the body.
func encodeMultipartParts(parts []multipartPart, boundary string) ([]byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(boundary); err != nil {
		return nil, err
	}
	for _, p := range parts {
		pw, err := w.CreatePart(p.header)
		if err != nil {
			return nil, err
		}
		if _, err := pw.Write(p.data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// formValueHeader builds the header of a plain (non-file) form value part. The
// names it is called with are package constants, so no quote escaping is needed.
func formValueHeader(name string) textproto.MIMEHeader {
	h := make(textproto.MIMEHeader, 1)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, name))
	return h
}
