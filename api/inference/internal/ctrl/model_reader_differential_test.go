package ctrl

import (
	"bytes"
	"encoding/json"
	"testing"
)

// upstreamModelJSON reproduces the upstream's read of `model` from a JSON create, through
// the same mechanism and the same struct it uses: a json.Decoder over
// videotranslator/internal/handler/video.go's jsonCreateVideoRequest
// (parseCreateVideoRequest, which does json.NewDecoder(r.Body).Decode(&jr)).
//
// Mirroring the WHOLE struct, not just `model`, is load-bearing. A helper declaring only
// the one field would be MORE lenient than the upstream, and would then report a
// divergence for a body whose own Decode fails too — a request that 400s before any clip
// exists, and therefore has no price to get wrong. A differential model has to be exactly
// as strict as the thing it models, or it manufactures findings that are not there.
//
// videotranslator/internal is not importable from here (one module, but `internal/` is
// only visible under videotranslator/), so the struct is restated. Restating means it can
// drift; what limits the damage is that drift in either direction shows up as a failure of
// this test rather than as a silent mispricing.
func upstreamModelJSON(body string) (model string, decoded bool) {
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
		// The whole create is unreadable, so parseCreateVideoRequest returns an error and
		// the request 400s. No clip exists and nothing is billed, so whatever the broker
		// read cannot be wrong about a price.
		return "", false
	}
	return jr.Model, true
}

// ExtractModelName is a reader on a money path, and it has to agree with the upstream's
// reader on every body the upstream accepts.
//
// It decides the PRICE: the video reserve and ResolveModelForBilling both resolve the
// pricing tier through it, and "" means "use the default model"
// (ResolveModelForBilling substitutes c.Service.ModelType). So a body whose model the
// broker cannot read is priced at the default tier while the vendor renders whatever the
// body actually named — the per-model price and ResolveRequestedModel's allowlist bypassed
// together. The video path forwards the body verbatim (PrepareHTTPRequest rewrites only
// chatbot), so nothing downstream disagrees.
//
// The assertion is EQUALITY, not an inequality. For a duration there is a conservative
// direction — reserve more than the bill — but a wrong model is a different price list,
// with no safe side to err on.
func TestExtractModelNameMatchesTheUpstreamReader(t *testing.T) {
	for _, body := range []string{
		// The plain cases, so a "fix" that returns "" for everything cannot pass.
		`{"model":"premium-4k","seconds":5}`,
		`{"model":"premium-4k"}`,
		`{"seconds":5}`,
		`{}`,
		// The shapes a Decoder accepts and a strict Unmarshal does not. Each of these is
		// read by the upstream, so each is a price the broker has to get right.
		`{"model":"premium-4k","seconds":5} `,
		"{\"model\":\"premium-4k\",\"seconds\":5}\n",
		`{"model":"premium-4k","seconds":5} x`,
		`{"model":"premium-4k","seconds":5}{"model":"cheap"}`,
		`{"model":"premium-4k","id":1}`,
		`{"model":"premium-4k","metadata":{"anything":[1,2,3]}}`,
		// And the shapes where the upstream itself fails. The broker may read these either
		// way: the request 400s and no clip exists, so there is nothing to price.
		`{"model":"premium-4k","seconds":`,
		`{"model":5}`,
		`["premium-4k"]`,
		`not json at all`,
		``,
	} {
		t.Run(body, func(t *testing.T) {
			want, decoded := upstreamModelJSON(body)
			got := ExtractModelName([]byte(body), "application/json")
			if !decoded {
				// Recorded rather than asserted, so this test cannot manufacture a finding
				// for a request that was never served.
				t.Logf("upstream rejects this body; broker read %q", got)
				return
			}
			if got != want {
				t.Errorf("broker read model %q, upstream reads %q: the reserve and the bill price a tier the vendor is not rendering", got, want)
			}
		})
	}
}
