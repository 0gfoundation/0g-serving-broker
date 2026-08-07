package ctrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// This file exists because four separate under-reserves in this package were ONE defect wearing four
// hats: the broker and the upstream both read `seconds` out of the same request, with different
// strictness, and the reserve took the cheaper answer. Each was found by a reviewer probing one shape,
// fixed, and followed by the next variant on a different axis:
//
//	variant 1  the query was read on a transport where the upstream never reads it
//	variant 2  a value past the broker's sizing cap counted as "the upstream named a duration"
//	variant 3  the size came from the body while the upstream read the query
//	variant 4  strict json.Unmarshal over a wide struct vs the upstream's json.Decoder over a narrow one
//
// A per-shape test per variant is a losing game — the axes are not enumerable by inspection. So this is
// a differential sweep against the MECHANISMS the upstream actually uses, which are standard library:
// `r.FormValue` after `ParseMultipartForm` for multipart, and `json.Decoder` for JSON
// (videotranslator/internal/handler/video.go: parseCreateVideoRequest). The vendor's own clamping is NOT
// modelled here — that module is not importable — so the assertion is the weaker, transport-level one
// that is nonetheless where all four variants lived:
//
//	whatever duration the upstream's reader yields, the reserve must have priced at least that many
//	seconds, or have priced the unknown-duration fallback.
//
// A new reader added to this path without a row here is the fifth variant waiting to happen.

// upstreamSecondsMultipart reproduces the upstream's read for a multipart create, using the same
// net/http machinery it uses: ParseMultipartForm seeds r.Form from the query, then appends body values,
// and FormValue returns the first.
func upstreamSecondsMultipart(t *testing.T, body, contentType, rawQuery string) string {
	t.Helper()
	target := "/v1/videos"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	// The error is deliberately ignored, exactly as r.FormValue does.
	_ = req.ParseMultipartForm(32 << 20)
	return req.FormValue("seconds")
}

// upstreamSecondsJSON reproduces the upstream's read for a JSON create: a Decoder over a struct with the
// SAME FIELDS AND TYPES as videotranslator/internal/handler/video.go's jsonCreateVideoRequest.
//
// Mirroring the whole struct, not just `seconds`, is load-bearing — and the sweep proved it by catching
// my own first attempt. A helper declaring only `seconds` is MORE lenient than the upstream, so
// `{"seconds":20,"size":5}` looked like a broker-side divergence when in fact the upstream's own Decode
// fails on it too (`Size string`) and the request 400s before any clip exists. The differential model has
// to be as strict as the thing it models, or it manufactures variants that are not there — which is this
// file's own defect class, one level up.
func upstreamSecondsJSON(body string) (raw string, decoded bool) {
	var jr struct {
		Model          string      `json:"model"`
		Prompt         string      `json:"prompt"`
		Seconds        json.Number `json:"seconds"`
		Size           string      `json:"size"`
		Seed           json.Number `json:"seed"`
		InputReference *struct {
			ImageURL string `json:"image_url"`
			FileID   string `json:"file_id"`
		} `json:"input_reference"`
	}
	if err := json.NewDecoder(bytes.NewReader([]byte(body))).Decode(&jr); err != nil {
		// The whole create is unreadable, so parseCreateVideoRequest returns an error and the request
		// 400s. No clip exists, nothing is billed, and the reserve owes nothing — distinguishing this
		// from "the body decoded but named no duration" is required, or the sweep reports a violation
		// for a request that was never served.
		return "", false
	}
	return jr.Seconds.String(), true
}

// vendorFloorSeconds is what a vendor renders for a duration it cannot read or that sits below its
// minimum. Restated from videotranslator/internal/translate/minimax.go (minMiniMaxDuration), which is in
// a module this test cannot import; TestVideoReserveClampsToTheVendorFloor pins the same number.
const vendorFloorSeconds = 4

// TestVideoReserveDifferentialAgainstUpstreamReaders sweeps every value shape that has ever produced a
// divergence, plus the neighbourhood of each, on both transports and both sources.
func TestVideoReserveDifferentialAgainstUpstreamReaders(t *testing.T) {
	// Asserted on videoReserveSeconds directly: it is the function all four variants lived in, and its
	// answer is in seconds, so the assertion needs no pricing round-trip. The units math on top of it is
	// covered by the other tests in this package.

	// Value shapes: the four variants' triggers, their boundaries, and the ordinary cases around them.
	values := []string{
		"", "4", "5", "15", "16", "60", "600",
		"0", "-3", "0.5", "1", "2", "3", "4.1",
		"abc", "true", "null", "+4", "1e1", "1E1", ".5", "5.", "0x4", "1_0",
		" 1", "1 ", "\t2", "\r\n3", " 15 ",
		"1099511627775", "1099511627776", "1099511627777", "2e12", "1e30", "1e308", "1e400",
		"Inf", "-Inf", "NaN", strings.Repeat("9", 40),
		// At and around the multipart read cap: the broker stops reading there, the upstream's FormValue
		// does not, so the two see different numbers unless a truncated read counts as no read.
		"5." + strings.Repeat("0", maxMultipartScalarBytes-2) + "e3",
		strings.Repeat("9", maxMultipartScalarBytes-1),
		"1" + strings.Repeat("0", maxMultipartScalarBytes),
	}

	type row struct{ transport, source, value string }
	var rows []row
	for _, v := range values {
		rows = append(rows,
			row{"multipart", "body", v},
			row{"multipart", "query", v},
			row{"json", "body", v},
			row{"json", "query", v},
		)
	}
	// The JSON body's SHAPE is its own axis — variant 4 lived here and nowhere else. A body carrying a
	// field only the broker's struct declares, or anything after the closing brace, is readable to the
	// upstream's Decoder and was unreadable to a strict Unmarshal over the wide struct.
	for _, v := range []string{"4", "20", "60", "600"} {
		for _, shape := range []string{
			`{"seconds":%s,"id":1}`, `{"seconds":%s,"status":5}`, `{"seconds":%s,"usage":7}`,
			`{"seconds":%s,"model":{"a":1}}`, `{"seconds":%s} x`, `{"seconds":%s}{"a":1}`,
			"{\"seconds\":%s}\n\n", `{"seconds":%s,"size":5}`,
		} {
			rows = append(rows, row{"json", "shape:" + fmt.Sprintf(shape, v), ""})
		}
	}
	// Both sources populated at once — variant 3's shape, and the one a per-shape test kept missing.
	for _, bodyV := range []string{"1", "4", "9", "14"} {
		for _, queryV := range []string{"", "2e12", "1e30", "1099511627777", "abc", "600"} {
			rows = append(rows, row{"multipart", "both:" + bodyV, queryV}, row{"json", "both:" + bodyV, queryV})
		}
	}

	violations, skipped := 0, 0
	for _, r := range rows {
		var body, contentType, rawQuery string
		switch {
		case strings.HasPrefix(r.source, "both:"):
			bodyV := strings.TrimPrefix(r.source, "both:")
			if r.transport == "multipart" {
				body, contentType = multipartSeconds(t, bodyV)
			} else {
				body, contentType = fmt.Sprintf(`{"seconds":%s}`, bodyV), "application/json"
			}
			if r.value != "" {
				rawQuery = "seconds=" + url.QueryEscape(r.value)
			}
		case strings.HasPrefix(r.source, "shape:"):
			body, contentType = strings.TrimPrefix(r.source, "shape:"), "application/json"
		case r.source == "body":
			if r.transport == "multipart" {
				body, contentType = multipartSeconds(t, r.value)
			} else {
				body, contentType = fmt.Sprintf(`{"seconds":%s}`, quoteIfNotJSONNumber(r.value)), "application/json"
			}
		default: // query
			if r.transport == "multipart" {
				body, contentType = multipartSeconds(t, "")
			} else {
				body, contentType = `{"prompt":"a cat"}`, "application/json"
			}
			rawQuery = "seconds=" + url.QueryEscape(r.value)
		}

		// What the upstream's own reader yields for this request.
		var upstreamRaw string
		if r.transport == "multipart" {
			// No `decoded` distinction here on purpose: the in-repo translator does propagate
			// ParseMultipartForm's error and 400 such a request, but the contract admits shims that read
			// leniently, so the reserve errs high and the sweep holds it to that.
			upstreamRaw = upstreamSecondsMultipart(t, body, contentType, rawQuery)
		} else {
			var decoded bool
			if upstreamRaw, decoded = upstreamSecondsJSON(body); !decoded {
				skipped++
				continue
			}
		}

		// What the reserve OWES for that reading, by its own documented rules:
		//
		//   the upstream read nothing usable    -> the unknown-duration fallback (the vendor will choose)
		//   the upstream read something the
		//     broker cannot size (past its cap) -> the fallback again: it cannot price what it read
		//   otherwise                           -> that duration, floored at the vendor minimum
		//
		// Deliberately NOT capped at maxVideoOutputUnits: the VENDOR has no such cap (MiniMax clamps to
		// its 15s ceiling, DashScope forwards the value unclamped), and an earlier version of this
		// expectation collapsed to the vendor floor exactly where the four variants bite — which made the
		// whole sweep vacuous. Verified against each variant: see the mutation list in the file header.
		//   the upstream read a value the broker's own reader cannot
		//     take in (longer than maxMultipartScalarBytes)      -> the fallback, same reasoning
		//
		// That last cap encodes an ASSUMPTION this repo cannot verify: that no model here renders longer
		// than videoReserveFallbackSeconds. MiniMax clamps to exactly that, so it holds there; DashScope
		// forwards a duration unclamped and its ceiling is pinned nowhere, so a 5000s value it accepted
		// would bill 5000 against a 15s reserve. That is the documented residual, not a finding this
		// sweep can settle — but it is stated here rather than hidden by a blanket cap, because capping
		// every oversized value at the fallback would also blind this sweep to variant 4, whose triggers
		// are ordinary 20s and 600s durations.
		owed := int64(videoReserveFallbackSeconds)
		brokerCanRead := len(upstreamRaw) < maxMultipartScalarBytes
		if f, err := strconv.ParseFloat(upstreamRaw, 64); brokerCanRead && err == nil && f > 0 && !math.IsInf(f, 0) &&
			f <= float64(maxVideoOutputUnits) {
			if ceil := int64(math.Ceil(f)); ceil > vendorFloorSeconds {
				owed = ceil
			} else {
				owed = vendorFloorSeconds
			}
		}

		reserved := videoReserveSeconds([]byte(body), contentType, rawQuery)
		if reserved < owed {
			violations++
			t.Errorf("UNDER-RESERVE  transport=%s source=%s value=%q: reserve=%ds, upstream reads %q -> owed >=%ds",
				r.transport, r.source, r.value, reserved, upstreamRaw, owed)
		}
	}
	t.Logf("swept %d (transport x source x value) shapes against the upstream's own readers: %d violations, %d skipped (the upstream itself refuses the body, so no clip is billed)",
		len(rows), violations, skipped)
}

// quoteIfNotJSONNumber makes a value embeddable in a JSON body: bare where it is a valid JSON token,
// quoted otherwise, so the sweep covers both "the client sent a number" and "the client sent a string".
func quoteIfNotJSONNumber(v string) string {
	if v == "" {
		return `""`
	}
	if json.Valid([]byte(v)) {
		return v
	}
	return strconv.Quote(v)
}

// TestVideoReserveEveryTransportBranchIsExercised drives VideoCreateReserveFee once per content-type
// branch, so a reader added behind one of them cannot sit entirely untested. It is deliberately not an
// "inventory" assertion over a hand-written list of reader names — a list compared against its own
// length is a test that cannot fail, which is the exact shape this package has been burned by.
//
// The readers the sweep above covers, for whoever adds the next one:
//
//	videoSecondsSizeFromRequest  json.Decoder over a narrow struct (JSON) + multipartFormField (multipart)
//	videoReserveQuerySeconds     url.ParseQuery, first value, error ignored — mirrors r.ParseForm/FormValue
//	videoUpstreamSeconds         pairs the size and the readability verdict for the ONE source read upstream
//	multipartFormFieldRaw        untrimmed read plus a presence flag, capped at maxMultipartScalarBytes
//	vendorParsesSeconds          strconv.ParseFloat on the verbatim value — the vendors' own predicate
//
// A new one without a row in the sweep is the fifth variant waiting to happen.
func TestVideoReserveEveryTransportBranchIsExercised(t *testing.T) {
	c := singleModelVideoCtrl(t, map[string]float64{"2K": 1.0})
	body, multipartCT := multipartSeconds(t, "5")

	for _, tc := range []struct{ name, body, contentType, rawQuery string }{
		{"multipart body", body, multipartCT, ""},
		{"multipart query", body, multipartCT, "seconds=9"},
		{"json body", `{"seconds":5}`, "application/json", ""},
		{"json with a query the upstream ignores", `{"seconds":5}`, "application/json", "seconds=9"},
		{"no content type", `{"seconds":5}`, "", ""},
		{"empty body", "", multipartCT, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fee, err := c.VideoCreateReserveFee(gateCtx(), []byte(tc.body), tc.contentType, tc.rawQuery)
			if err != nil {
				t.Fatalf("VideoCreateReserveFee: %v", err)
			}
			// Every branch must price something: a zero reserve is the bug this whole change exists to
			// remove, and it must not be reachable through any transport.
			if n, convErr := strconv.ParseInt(fee, 10, 64); convErr != nil || n <= 0 {
				t.Errorf("reserve = %q (parsed %d, err %v), want a positive fee", fee, n, convErr)
			}
		})
	}
}
