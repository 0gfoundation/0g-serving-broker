package ctrl

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/internal/lora"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

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
		return errors.New("LoRA serving not enabled")
	}

	adapter := c.loraManager.GetAdapter(modelName)
	if adapter == nil {
		return fmt.Errorf("fine-tuned model not found: %s", modelName)
	}

	if !strings.EqualFold(adapter.UserAddress, userAddress) {
		return fmt.Errorf("access denied: you are not the owner of model %s", modelName)
	}

	// Check adapter state
	switch adapter.State {
	case model.AdapterStateActive:
		c.loraManager.RecordAccess(modelName)
		return nil
	case model.AdapterStateReady:
		return fmt.Errorf("model %s is downloaded but not deployed; call deploy-adapter first", modelName)
	case model.AdapterStateLoading:
		return fmt.Errorf("model %s is still loading, please retry later", modelName)
	case model.AdapterStateOffloaded, model.AdapterStateArchived:
		go c.loraManager.RestoreAdapter(modelName)
		return fmt.Errorf("model %s is restoring, please retry in 30 seconds", modelName)
	case model.AdapterStateFailed:
		return fmt.Errorf("model %s failed to deploy", modelName)
	default:
		return fmt.Errorf("model %s is in unknown state: %s", modelName, adapter.State)
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

// ExtractModelName extracts the "model" field from a JSON request body.
func ExtractModelName(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		return ""
	}
	modelName, _ := bodyMap["model"].(string)
	return modelName
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
