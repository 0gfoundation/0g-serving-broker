package ctrl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// ErrLoRAUnavailable marks a LoRA ownership-check failure caused by the broker's
// own adapter lifecycle or configuration — serving disabled, the adapter still
// loading or restoring, a failed deploy, or an unknown state — rather than by
// the client. The proxy uses errors.Is(err, ErrLoRAUnavailable) to attribute
// these to the broker (not the client) in the unified failure metric, so a
// broken LoRA backend still trips the broker-fault alert instead of hiding in
// the client bucket. Client-caused branches (unknown ft model, not the owner,
// adapter downloaded-but-not-deployed) are returned unwrapped.
var ErrLoRAUnavailable = errors.New("lora unavailable")

// SetLoRAManager injects the LoRA manager into the Ctrl.
func (c *Ctrl) SetLoRAManager(m *lora.Manager) {
	c.loraManager = m
}

// GetLoRAManager returns the LoRA manager (may be nil if not enabled).
func (c *Ctrl) GetLoRAManager() *lora.Manager {
	return c.loraManager
}

// CheckLoRAOwnership verifies that userAddress is the owner of the LoRA model.
// Returns nil for non-LoRA models (no prefix "ft-").
func (c *Ctrl) CheckLoRAOwnership(modelName, userAddress string) error {
	if !lora.IsLoRAModel(modelName) {
		return nil
	}

	if c.loraManager == nil {
		// Broker config fault: LoRA serving isn't enabled on this broker.
		return fmt.Errorf("%w: LoRA serving not enabled", ErrLoRAUnavailable)
	}

	adapter := c.loraManager.GetAdapter(modelName)
	if adapter == nil {
		return fmt.Errorf("fine-tuned model not found: %s", modelName)
	}

	if !strings.EqualFold(adapter.UserAddress, userAddress) {
		return fmt.Errorf("access denied: you are not the owner of model %s", modelName)
	}

	// Check adapter state. Active/Ready are client-resolvable; the remaining
	// states are broker-side adapter lifecycle (loading/restoring/failed/unknown)
	// and are wrapped in ErrLoRAUnavailable so the proxy attributes them to the
	// broker rather than the client.
	switch adapter.State {
	case model.AdapterStateActive:
		c.loraManager.RecordAccess(modelName)
		return nil
	case model.AdapterStateReady:
		return fmt.Errorf("model %s is downloaded but not deployed; call deploy-adapter first", modelName)
	case model.AdapterStateLoading:
		return fmt.Errorf("%w: model %s is still loading, please retry later", ErrLoRAUnavailable, modelName)
	case model.AdapterStateOffloaded, model.AdapterStateArchived:
		go c.loraManager.RestoreAdapter(modelName)
		return fmt.Errorf("%w: model %s is restoring, please retry in 30 seconds", ErrLoRAUnavailable, modelName)
	case model.AdapterStateFailed:
		return fmt.Errorf("%w: model %s failed to deploy", ErrLoRAUnavailable, modelName)
	default:
		return fmt.Errorf("%w: model %s is in unknown state: %s", ErrLoRAUnavailable, modelName, adapter.State)
	}
}

// RewriteLoRARequest detects ft-* model names and rewrites the request body
// for ServerlessLLM: replaces "model" with base model name and adds "lora_adapter_name".
// Returns the modified body and the original model name.
func (c *Ctrl) RewriteLoRARequest(body []byte) ([]byte, string, error) {
	if len(body) == 0 {
		return body, "", nil
	}

	var bodyMap map[string]interface{}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return body, "", nil
	}

	modelName, _ := bodyMap["model"].(string)
	if !lora.IsLoRAModel(modelName) {
		return body, modelName, nil
	}

	if c.loraManager == nil {
		return nil, modelName, errors.New("LoRA serving not enabled")
	}

	// Rewrite: model → base model, add lora_adapter_name
	bodyMap["model"] = c.Service.ModelType
	bodyMap["lora_adapter_name"] = modelName

	modified, err := json.Marshal(bodyMap)
	if err != nil {
		return body, modelName, errors.Wrap(err, "marshal rewritten LoRA request")
	}

	c.logger.Debugf("LoRA rewrite: %s → model=%s, lora_adapter_name=%s", modelName, c.Service.ModelType, modelName)
	return modified, modelName, nil
}

// CreateAdapterKey stores a pre-pushed adapter key from the fine-tuning broker.
func (c *Ctrl) CreateAdapterKey(key *model.AdapterKey) error {
	return c.db.CreateAdapterKey(key)
}

// GetAdapterKey retrieves a pre-pushed adapter key by task ID.
func (c *Ctrl) GetAdapterKey(taskID string) (*model.AdapterKey, error) {
	return c.db.GetAdapterKeyByTaskID(taskID)
}

// ExtractModelName extracts the requested "model" from a request body. Handles
// JSON bodies and multipart/form-data (used by audio endpoints, where "model" is
// a form field). Returns "" when the model is absent or the body is unparseable.
// contentType is the request Content-Type header; an empty/non-multipart value
// is treated as JSON.
func ExtractModelName(body []byte, contentType string) string {
	if len(body) == 0 {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/") {
		return extractModelFromMultipart(body, contentType)
	}
	// Read the way the upstream reads it, on both axes, because everything keyed on this answer
	// (the video pre-flight reserve's price, ResolveModelForBilling's allowlist, settlement's
	// per-model price, the metric label) otherwise describes a different request than the one
	// served — the upstream renders the model the body names while the broker substitutes the
	// configured default.
	//
	//   - json.Decoder, not json.Unmarshal: the translator decodes with a Decoder, which ignores
	//     trailing data, so Unmarshal's whole-input validation made one appended byte return "".
	//   - key-wise and CASE-INSENSITIVE: the translator decodes into a struct, and encoding/json
	//     matches object keys onto struct fields regardless of case, so an exact-key read missed
	//     `{"Model":"expensive"}` entirely.
	//
	// More than one spelling is reported absent rather than guessed at — Go resolves competing
	// variants by document order, which an unordered map cannot see. Callers substitute the
	// configured model; the video reserve refuses such a body outright.
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&fields); err != nil {
		return ""
	}
	raw, matched := jsonFieldFolded(fields, "model")
	if exact, ok := fields["model"]; ok {
		// The exact spelling wins when the body carries it. This answer feeds AUTHORIZATION
		// gates — CheckLoRAOwnership, which short-circuits on IsLoRAModel("") and is the only
		// ownership check on a private fine-tuned adapter, and the model-expiry 410 — so
		// answering "no model named" for a body that plainly names one skips them. Which is
		// exactly what folding alone did: `{"model":"ft-victim","Model":"ft-victim"}` reported
		// two variants and returned "", while ValidateModelAllowlist read the exact key and
		// admitted the adapter.
		//
		// The money path does not rely on this tie-break: the video reserve refuses a body with
		// competing spellings of any price-setting field outright, rather than picking a reading
		// the upstream may not share.
		raw, matched = exact, 1
	}
	if matched != 1 {
		return ""
	}
	var modelName string
	_ = json.Unmarshal(raw, &modelName)
	return modelName
}

// extractModelFromMultipart reads the "model" form field from a multipart/form-data
// body. Returns "" if the boundary can't be parsed, the field is missing, or any
// read error occurs — callers treat "" as "use the default model".
func extractModelFromMultipart(body []byte, contentType string) string {
	return multipartFormField(body, contentType, "model")
}

// multipartFormField returns the value of a named non-file form field from a
// multipart/form-data body using a real MIME reader (NOT a substring scan), so
// adversarial content in another field — e.g. a prompt body containing the
// literal name="seconds" — cannot be mistaken for the field. Returns "" when the
// content type isn't multipart, the boundary is missing, or the field is absent.
// The read is capped so a mislabeled file part can't pull unbounded memory; used
// for short scalar fields (model, seconds, size).
// Deliberately NOT delegating to multipartFormFields: that reader answers a money-grade
// question (was this field repeated, was its value truncated, did the body walk cleanly) and to
// do so it must always advance to io.EOF — and multipart.Reader.NextPart streams every part it
// skips. This wrapper's callers want one short scalar from bodies that can be tens of
// megabytes of audio or image, and they want it before the upload. Delegating cost 34ms of CPU
// per speech-to-text request at 25MB (measured ~2,600x) for an answer none of them read.
func multipartFormField(body []byte, contentType, name string) string {
	reader, ok := multipartReader(body, contentType)
	if !ok {
		return ""
	}
	for {
		part, err := reader.NextPart()
		if err != nil {
			return "" // io.EOF (field absent) or a malformed body
		}
		if part.FormName() == name && part.FileName() == "" {
			val, _ := io.ReadAll(io.LimitReader(part, maxMultipartFieldBytes))
			part.Close()
			return strings.TrimSpace(string(val))
		}
		part.Close()
	}
}

// multipartReader builds a MIME reader for a multipart body, reporting false when the content
// type is not multipart or carries no boundary.
func multipartReader(body []byte, contentType string) (*multipart.Reader, bool) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, false
	}
	boundary, ok := params["boundary"]
	if !ok {
		return nil, false
	}
	return multipart.NewReader(bytes.NewReader(body), boundary), true
}

// maxMultipartFieldBytes caps how much of a scalar part is read, so a mislabeled file
// part cannot pull unbounded memory. A value longer than this is reported truncated
// rather than silently shortened — see multipartField.Truncated.
const maxMultipartFieldBytes = 1024

// multipartField is what one walk learned about a single field name. The flags beyond the
// value exist because a money path has to tell "the client did not send this" apart from "the
// client sent something we could not read the same way the upstream will":
//
//   - Values holds EVERY non-file part with this name, in order, so a caller can refuse a
//     repeated field instead of guessing. The broker reads the first; Starlette / FastAPI form
//     parsers return the LAST — so silently taking one of them lets a caller price
//     `seconds=1` and be rendered `seconds=15`.
//   - Truncated says a value hit maxMultipartFieldBytes. Padding a value past the cap makes it
//     read as empty here while the upstream's own parser (no per-field cap, and it trims)
//     reads the real value.
type multipartField struct {
	Values    []string
	Truncated bool
}

// multipartFormFields walks a multipart/form-data body ONCE and reports what it found for each
// requested field name, using a real MIME reader (NOT a substring scan) so adversarial content
// in another field — a prompt containing the literal name="seconds" — cannot be mistaken for
// the field.
//
// walkedOK is a property of the BODY, not of any field: false when the content type is not
// multipart, carries no boundary, or the body could not be walked to the end. It is returned
// separately rather than stamped onto every entry so a caller cannot check it on one field and
// miss it on another — and so a lookup of a name that was never requested (a zero
// multipartField) cannot be mistaken for "the body is unwalkable".
//
// One walk for all names on purpose: an image-to-video create can carry megabytes of reference
// image, and advancing a multipart reader streams every part it skips, so a walk per field
// multiplied that cost by the number of fields.
func multipartFormFields(body []byte, contentType string, names ...string) (fields map[string]multipartField, walkedOK bool) {
	found := make(map[string]multipartField, len(names))
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		found[n] = multipartField{}
		wanted[n] = true
	}
	reader, ok := multipartReader(body, contentType)
	if !ok {
		return found, false
	}
	for {
		part, err := reader.NextPart()
		if err != nil {
			// errors.Is rather than ==: NextPart returns the bare sentinel today, and if that
			// ever becomes wrapped, an == check would report every body unwalkable and turn
			// every multipart video create into a 400.
			return found, errors.Is(err, io.EOF)
		}
		if name := part.FormName(); wanted[name] && part.FileName() == "" {
			val, _ := io.ReadAll(io.LimitReader(part, maxMultipartFieldBytes+1))
			f := found[name]
			if len(val) > maxMultipartFieldBytes {
				f.Truncated = true
				val = val[:maxMultipartFieldBytes]
			}
			f.Values = append(f.Values, strings.TrimSpace(string(val)))
			found[name] = f
		}
		part.Close()
	}
}

// rewriteResponseModel patches the "model" field in a non-streaming JSON response
// to return the original ft-* model name instead of the base model name that vLLM returns.
// Uses JSON unmarshal/marshal to handle any JSON whitespace formatting.
func (c *Ctrl) rewriteResponseModel(ctx *gin.Context, body []byte) []byte {
	originalModel, exists := ctx.Get("loraOriginalModel")
	if !exists {
		return body
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}

	modelRaw, ok := resp["model"]
	if !ok {
		return body
	}

	var modelVal string
	if err := json.Unmarshal(modelRaw, &modelVal); err != nil {
		return body
	}

	target, ok := originalModel.(string)
	if !ok {
		return body
	}
	for _, candidate := range c.vllmModelNames() {
		if modelVal == candidate {
			quoted, err := json.Marshal(target)
			if err != nil {
				return body
			}
			resp["model"] = quoted
			out, err := json.Marshal(resp)
			if err != nil {
				return body
			}
			return out
		}
	}
	return body
}

// rewriteResponseModelLine patches the "model" field in a single SSE streaming line.
// Handles both compact ("model":"...") and spaced ("model": "...") JSON formatting.
func (c *Ctrl) rewriteResponseModelLine(ctx *gin.Context, line string) string {
	originalModel, exists := ctx.Get("loraOriginalModel")
	if !exists {
		return line
	}
	target, ok := originalModel.(string)
	if !ok {
		return line
	}
	for _, candidate := range c.vllmModelNames() {
		// Try both compact and spaced JSON formats
		for _, pattern := range []string{
			fmt.Sprintf(`"model":"%s"`, candidate),
			fmt.Sprintf(`"model": "%s"`, candidate),
		} {
			if strings.Contains(line, pattern) {
				return strings.Replace(line, pattern, fmt.Sprintf(`"model": "%s"`, target), 1)
			}
		}
	}
	return line
}

// vllmModelNames returns candidate model names that vLLM may use in responses.
// vLLM returns the model path it was started with (e.g., "/models/Qwen2.5-0.5B-Instruct"),
// which may differ from service.model config. We try both the lora baseModel and service model.
func (c *Ctrl) vllmModelNames() []string {
	candidates := []string{c.Service.ModelType}
	if c.loraManager != nil {
		base := c.loraManager.GetBaseModel()
		if base != "" && base != c.Service.ModelType {
			candidates = append([]string{base}, candidates...)
		}
	}
	return candidates
}
