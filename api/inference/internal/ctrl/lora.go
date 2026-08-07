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
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return ""
	}
	modelName, _ := bodyMap["model"].(string)
	return modelName
}

// extractModelFromMultipart reads the "model" form field from a multipart/form-data
// body. Returns "" if the boundary can't be parsed, the field is missing, or any
// read error occurs — callers treat "" as "use the default model".
func extractModelFromMultipart(body []byte, contentType string) string {
	return multipartFormField(body, contentType, "model")
}

// maxMultipartScalarBytes caps what the scalar form readers here will pull from one part, so a
// mislabeled file part cannot stream unbounded memory. Callers on a money path must reason about it
// rather than treat it as an implementation detail: the vendor's own r.FormValue has no such cap, so a
// value at or past this length is one the broker cannot claim to have read the way the upstream will.
const maxMultipartScalarBytes = 1024

// multipartFormField returns the value of a named non-file form field from a
// multipart/form-data body using a real MIME reader (NOT a substring scan), so
// adversarial content in another field — e.g. a prompt body containing the
// literal name="seconds" — cannot be mistaken for the field. Returns "" when the
// content type isn't multipart, the boundary is missing, or the field is absent.
// The read is capped so a mislabeled file part can't pull unbounded memory; used
// for short scalar fields (model, seconds, size).
func multipartFormField(body []byte, contentType, name string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	boundary, ok := params["boundary"]
	if !ok {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			return "" // io.EOF (field absent) or a malformed body
		}
		if part.FormName() == name && part.FileName() == "" {
			val, _ := io.ReadAll(io.LimitReader(part, maxMultipartScalarBytes))
			part.Close()
			return strings.TrimSpace(string(val))
		}
		part.Close()
	}
}

// multipartFormFieldRaw is multipartFormField without the trim, plus whether the field was present at
// all. Both matter to one caller only: the video reserve has to know whether the VENDOR can read a
// `seconds` value, and the vendor translators call strconv.ParseFloat on the verbatim form value — so a
// padded " 1" that this package trims into a usable 1 is unreadable to them, and they fall back to a
// vendor default the broker cannot see.
func multipartFormFieldRaw(body []byte, contentType, name string) (value string, present bool) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", false
	}
	boundary, ok := params["boundary"]
	if !ok {
		return "", false
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := reader.NextPart()
		if err != nil {
			return "", false
		}
		if part.FormName() == name && part.FileName() == "" {
			val, _ := io.ReadAll(io.LimitReader(part, maxMultipartScalarBytes))
			part.Close()
			return string(val), true
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
