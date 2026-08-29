package handler

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

type adapterKeyRequest struct {
	TaskID         string `json:"taskId" binding:"required"`
	StorageHash    string `json:"storageHash" binding:"required"`
	ProviderEncKey string `json:"providerEncKey" binding:"required"`
	// TeeSignerAddress is the producing enclave's signer address, required so the
	// inference broker can verify the artifact's TEE tag signature before it
	// decrypts and deploys the adapter.
	TeeSignerAddress string `json:"teeSignerAddress" binding:"required"`
}

// adapterKeyErrorCode is a machine-readable identifier sent alongside the
// human-readable error message so the fine-tuning broker (and any SDK
// surfacing pushAdapterKey errors via getLog) can decide whether to retry,
// surface to the user, or open an alert.
//
// The exact strings are part of the inference-broker ↔ fine-tuning-broker
// internal contract. Only ever add new values here; existing values must
// keep their semantics for backwards compatibility.
const (
	adapterKeyErrInvalidPayload  = "invalid_payload"   // bad JSON / missing fields → caller bug, never retry
	adapterKeyErrInvalidHashSize = "invalid_hash_size" // storageHash length wrong → caller bug, never retry
	adapterKeyErrInvalidHashHex  = "invalid_hash_hex"  // storageHash not hex → caller bug, never retry
	adapterKeyErrInvalidEncKey   = "invalid_enc_key"   // providerEncKey not hex → caller bug, never retry
	adapterKeyErrInvalidSigner   = "invalid_signer"    // teeSignerAddress not a 20-byte hex address → caller bug, never retry
	adapterKeyErrPersist         = "persist_failed"    // db error during upsert → caller MAY retry with backoff
)

// ReceiveAdapterKey handles POST /internal/v1/adapter-keys from the fine-tuning broker.
// It validates and stores the provider-encrypted AES key used to decrypt LoRA adapters.
//
// The handler is idempotent: pushing the same TaskID twice (e.g. because the
// caller retried on a transient HTTP timeout) returns 200 in both cases. The
// underlying upsert (see db.CreateAdapterKey) ensures the row's StorageHash
// and ProviderEncKey are kept in sync with the latest push. This fixes the
// production failure mode reported by hackathon teams in May 2026 where the
// retry loop dead-locked on a unique-key violation and returned 500 forever.
//
// Error responses always carry a machine-readable `code` field so callers can
// distinguish caller bugs (4xx, never retry) from transient server issues
// (5xx, retry with backoff).
func (h *Handler) ReceiveAdapterKey(c *gin.Context) {
	var req adapterKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": err.Error(),
			"code":  adapterKeyErrInvalidPayload,
		})
		return
	}

	if strings.TrimSpace(req.TaskID) == "" || strings.TrimSpace(req.StorageHash) == "" || strings.TrimSpace(req.ProviderEncKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "fields cannot be empty or whitespace-only",
			"code":  adapterKeyErrInvalidPayload,
		})
		return
	}

	if len(req.TaskID) > 128 || len(req.StorageHash) > 128 || len(req.ProviderEncKey) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "field value exceeds maximum allowed length",
			"code":  adapterKeyErrInvalidPayload,
		})
		return
	}

	// Validate StorageHash format: must be 0x-prefixed 32-byte hex (66 chars total)
	if !strings.HasPrefix(req.StorageHash, "0x") || len(req.StorageHash) != 66 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "storageHash must be 0x-prefixed 32-byte hex (66 characters)",
			"code":  adapterKeyErrInvalidHashSize,
		})
		return
	}
	if _, err := hex.DecodeString(req.StorageHash[2:]); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "storageHash contains invalid hex characters",
			"code":  adapterKeyErrInvalidHashHex,
		})
		return
	}

	// Validate ProviderEncKey is valid hex (it's hex-encoded by hexutil.Encode on the sender side)
	encKeyHex := strings.TrimPrefix(req.ProviderEncKey, "0x")
	if _, err := hex.DecodeString(encKeyHex); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "providerEncKey must be valid hex",
			"code":  adapterKeyErrInvalidEncKey,
		})
		return
	}

	// The signer address gates TEE tag-signature verification on the consumption
	// path, so reject anything that is not a well-formed address rather than
	// storing a value that would later fail verification for the wrong reason.
	if !common.IsHexAddress(req.TeeSignerAddress) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "teeSignerAddress must be a 20-byte hex address",
			"code":  adapterKeyErrInvalidSigner,
		})
		return
	}

	key := &model.AdapterKey{
		TaskID:         req.TaskID,
		StorageHash:    req.StorageHash,
		ProviderEncKey: req.ProviderEncKey,
		// Checksummed so a later string comparison is not tripped by case drift.
		TeeSignerAddress: common.HexToAddress(req.TeeSignerAddress).Hex(),
	}

	if err := h.ctrl.CreateAdapterKey(key); err != nil {
		// Log the underlying error with a stable structure so on-call can
		// correlate by task id; keep the public response stripped down.
		h.logger.Errorf(
			"adapter-key upsert failed task=%s storageHash=%s err=%v",
			req.TaskID,
			truncateHash(req.StorageHash),
			err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to store adapter key (transient db error, please retry)",
			"code":  adapterKeyErrPersist,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"taskId": req.TaskID,
	})
}

// truncateHash returns the first 10 chars of a 0x-prefixed hex hash (or the
// whole string if shorter), suitable for log correlation without dumping the
// full 32-byte hash on every request.
func truncateHash(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:10] + "…"
}
