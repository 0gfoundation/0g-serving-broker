package ctrl

// The speech profile's request materialization (0g-pc SPEC §5.3).
//
// /v1/audio/transcriptions carries its payload as multipart/form-data, which has
// no top-level JSON object — so §5.2's AAD has nothing to canonicalize and §8's
// binding has no defined input. §5.3's answer is to convert the request to JSON
// before it is sealed and back to multipart inside the enclave, so nothing in
// the crypto or the envelope is multipart-aware. This file is the "back to
// multipart" half, and it runs only after the AAD has been verified over the
// JSON form.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"mime/multipart"
	"slices"
	"strconv"

	"github.com/0gfoundation/0g-pc-e2ee/protocol/wire"
)

const (
	// speechFileField is the JSON-ified request's payload field: the audio as a
	// base64 string (SPEC §5.3.2).
	speechFileField = "file_base64"
	// speechFilenameField is sealed whenever present and IS forwarded upstream —
	// some backends sniff the audio container from the extension (SPEC §5.3).
	speechFilenameField = "filename"
	// speechStreamField is dropped rather than rendered; see materializeSpeechRequest.
	speechStreamField = "stream"

	// speechUpstreamFileField is the form field the upstream reads the audio from.
	// The JSON-ified name differs (`file_base64` says how it is encoded), so the
	// two are not one constant.
	speechUpstreamFileField = "file"
	// speechFallbackFilename is used when the request sealed no filename. The part
	// needs some filename to read as a file upload rather than a text field.
	speechFallbackFilename = "audio"
)

// materializeSpeechRequest converts an unsealed JSON-ified speech request back
// into the multipart/form-data body the upstream speaks, returning the body and
// the Content-Type that describes it.
//
// The boundary is generated here rather than carried from the request: no
// boundary crosses the sealed channel (SPEC §5.3), and multipart.Writer's own is
// the only one this code ever sees.
//
// `stream` is DROPPED rather than rendered, which is a deliberate choice and not
// an omission. wire.OpenRequestFor has already enforced §5.3.3 — present means
// `false`, compared by materialized rendering — so by here the only permitted
// value is the endpoint's own default, and writing it adds nothing. What it
// removes is the one place where this file disagreeing with the protocol
// package's renderer would matter: that renderer (cleartextToken) is unexported,
// so the rule "must be false" and the rendering of `false` cannot be the same
// code, and the values a form parser reads as true are an open set (§5.3.3). A
// field that is not written cannot be misread.
func materializeSpeechRequest(req wire.Request) (body []byte, contentType string, err error) {
	audio, err := speechAudioBytes(req)
	if err != nil {
		return nil, "", err
	}
	filename, err := speechFilename(req)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(speechUpstreamFileField, filename)
	if err != nil {
		return nil, "", fmt.Errorf("create the audio part: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return nil, "", fmt.Errorf("write the audio part: %w", err)
	}

	// Sorted, so the same request materializes to the same field order. Map order
	// would make the forwarded body — and anything downstream that hashes it —
	// vary run to run for one input.
	//
	// The envelope's own `_e2ee` is not skipped here because OpenRequestFor has
	// already removed it (measured), and a skip for a key that is never present is
	// a check that cannot fail. The test asserting no marker reaches the form is
	// what would catch that changing.
	for _, name := range slices.Sorted(maps.Keys(req)) {
		switch name {
		case speechFileField, speechFilenameField, speechStreamField:
			continue
		}
		field, values, err := speechFormValues(name, req[name])
		if err != nil {
			return nil, "", err
		}
		for _, v := range values {
			if err := w.WriteField(field, v); err != nil {
				return nil, "", fmt.Errorf("write field %q: %w", field, err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", fmt.Errorf("close the multipart body: %w", err)
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// speechAudioBytes decodes the payload field.
//
// STANDARD base64, RFC 4648 §4, padding required — not the base64url-without-
// padding of §3. §3 governs binary fields that travel in the clear (`enc`,
// `key_id`, `ciphertext`); this one rides inside the ciphertext, and its encoding
// is fixed by a different constraint: `file_base64` already has an unsealed
// contract on the router's JSON surface, and one field name must not have two
// decoders depending on whether the request was sealed (SPEC §5.3).
func speechAudioBytes(req wire.Request) ([]byte, error) {
	raw, ok := req[speechFileField]
	if !ok {
		// Unreachable through OpenRequestFor, which refuses a sealed set that omits
		// the profile's required payload field — but this function is the one that
		// would hand the upstream an empty audio part if that ever stopped holding.
		return nil, fmt.Errorf("unsealed speech request has no %q field (SPEC §5.3.2)", speechFileField)
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, fmt.Errorf("%q must be a base64 string: %w", speechFileField, err)
	}
	audio, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("%q is not standard base64 with padding (RFC 4648 §4, SPEC §5.3): %w", speechFileField, err)
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("%q decodes to no audio", speechFileField)
	}
	return audio, nil
}

// speechFilename resolves the filename for the audio part.
func speechFilename(req wire.Request) (string, error) {
	raw, ok := req[speechFilenameField]
	if !ok {
		return speechFallbackFilename, nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return "", fmt.Errorf("%q must be a string: %w", speechFilenameField, err)
	}
	if name == "" {
		return speechFallbackFilename, nil
	}
	return name, nil
}

// speechFormValues renders one JSON field as the form field name and values a
// materialized request carries.
//
// An array becomes repeated `name[]` fields, which is how the OpenAI surface
// spells `timestamp_granularities` in multipart. A JSON null is written as no
// field at all rather than the four letters: absence is a value the endpoint
// understands, `"null"` is a string it would try to parse.
//
// An object is refused. There is no one rendering of a nested object in a form —
// PHP-style bracket paths, a JSON-in-a-field string and a flattened dotted key
// are all in use — so guessing one would forward something the upstream may read
// as a different request than the client sealed. No field of this profile is an
// object today; a future one needs a rendering chosen deliberately, not inferred
// here.
func speechFormValues(name string, raw json.RawMessage) (string, []string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", nil, fmt.Errorf("field %q is not valid JSON: %w", name, err)
	}
	switch t := v.(type) {
	case nil:
		return name, nil, nil
	case []any:
		values := make([]string, 0, len(t))
		for i, elem := range t {
			s, ok := speechScalarToken(elem)
			if !ok {
				return "", nil, fmt.Errorf("field %q element %d is a composite value, which has no form rendering (SPEC §5.3)", name, i)
			}
			values = append(values, s)
		}
		return name + "[]", values, nil
	default:
		s, ok := speechScalarToken(t)
		if !ok {
			return "", nil, fmt.Errorf("field %q is a composite value, which has no form rendering (SPEC §5.3)", name)
		}
		return name, []string{s}, nil
	}
}

// speechScalarToken renders a JSON scalar as the string a form carries. Numbers
// use the shortest representation that round-trips, so an integral value does
// not reach the upstream as `12.0` where the client wrote `12`.
func speechScalarToken(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64), true
	default:
		return "", false
	}
}
