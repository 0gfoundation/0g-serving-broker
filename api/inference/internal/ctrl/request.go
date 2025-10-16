package ctrl

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SessionToken represents the structure of a session token
type SessionToken struct {
	Address   string `json:"address"`
	Provider  string `json:"provider"`
	Timestamp int64  `json:"timestamp"`
	ExpiresAt int64  `json:"expiresAt"`
	Nonce     string `json:"nonce"`
}

// SessionValidationCache stores validated sessions to avoid repeated signature verification
// Since the cache key contains all validation data, we only need to store minimal info
type SessionValidationCache struct {
	ValidatedAt int64  // Timestamp when validation occurred
}

func (c *Ctrl) CreateRequest(req model.Request) error {
	return errors.Wrap(c.db.CreateRequest(req), "create request in db")
}

func (c *Ctrl) ListRequest(q model.RequestListOptions) ([]model.Request, int, error) {
	list, fee, err := c.db.ListRequest(q)
	if err != nil {
		return nil, 0, errors.Wrap(err, "list service from db")
	}
	return list, fee, nil
}

func (c *Ctrl) GetFromHTTPRequest(ctx *gin.Context) (model.Request, error) {
	var req model.Request
	headerMap := ctx.Request.Header

	for k := range constant.RequestMetaData {
		values := headerMap.Values(k)
		if len(values) == 0 && k != "VLLM-Proxy" {
			return req, errors.Wrapf(errors.New("missing Header"), "%s", k)
		}
		value := values[0]

		if err := updateRequestField(&req, k, value); err != nil {
			return req, err
		}
	}

	return req, nil
}

// ValidateSession validates the session token and signature
func (c *Ctrl) ValidateSession(ctx *gin.Context) error {
	// Get headers
	address := ctx.GetHeader("Address")
	tokenStr := ctx.GetHeader("Session-Token")
	signature := ctx.GetHeader("Session-Signature")
	
	// Check if all required headers are present
	if address == "" || tokenStr == "" || signature == "" {
		return errors.New("missing session authentication headers, please make sure your client includes Address, Session-Token, and Session-Signature headers")
	}
	
	// Parse session token
	var token SessionToken
	if err := json.Unmarshal([]byte(tokenStr), &token); err != nil {
		return errors.Wrap(err, "invalid session token format")
	}
	
	// Validate address matches
	if !strings.EqualFold(token.Address, address) {
		return errors.New("address mismatch in session token")
	}
	
	// Validate provider matches this provider
	if !strings.EqualFold(token.Provider, c.contract.ProviderAddress) {
		return errors.New("session token is for different provider")
	}
	
	// Check token expiration (convert milliseconds to seconds)
	if time.Now().Unix() > token.ExpiresAt/1000 {
		return errors.New("session token expired")
	}
	
	// Create hash values for secure caching
	tokenHashValue := crypto.Keccak256Hash([]byte(tokenStr)).Hex()
	signatureHashValue := crypto.Keccak256Hash([]byte(signature)).Hex()
	
	// Check session cache to avoid repeated signature verification
	// Use a more secure cache key that includes token and signature hashes
	cacheKey := fmt.Sprintf("%s:%s:%s:%s", address, token.Nonce, tokenHashValue, signatureHashValue)
	if _, found := c.sessionCache.Get(cacheKey); found {
		// Cache key already contains all verification data (address, nonce, token hash, signature hash)
		// If found, it means this exact combination was already validated
		return nil
	}
	
	// Verify signature following the same pattern as verifySignature in setup.go
	messageHash := crypto.Keccak256Hash([]byte(tokenStr))
	
	// Create Ethereum personal message hash (matches the client signMessage behavior)
	// Following the same pattern as getHash in setup.go
	prefixedMsg := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), messageHash.Bytes())
	
	// Decode signature from hex
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return errors.Wrap(err, "invalid signature format")
	}
	
	// Ethereum signatures are 65 bytes: R (32) + S (32) + V (1)
	if len(sigBytes) != 65 {
		return errors.New("invalid signature length")
	}
	
	// Adjust V value for Ethereum signature recovery (same as verifySignature)
	v1 := sigBytes[64] - 27
	pubKey, err := crypto.SigToPub(prefixedMsg.Bytes(), append(sigBytes[:64], v1))
	if err != nil {
		return errors.Wrap(err, "failed to recover public key from signature")
	}
	
	// Get address from public key
	recoveredAddr := crypto.PubkeyToAddress(*pubKey)
	
	// Verify recovered address matches claimed address (same as verifySignature)
	if !strings.EqualFold(recoveredAddr.Hex(), address) {
		return errors.New("signature verification failed: address mismatch")
	}
	
	// Cache the validated session
	// Cache will expire based on the cache configuration (5 minutes)
	c.sessionCache.Set(cacheKey, SessionValidationCache{
		ValidatedAt: time.Now().Unix(),
	}, cache.DefaultExpiration)
	
	// Session is valid
	return nil
}

// ValidateRequestWithEstimatedFee validates the request using an estimated fee
// This is used before the actual token count is known from the LLM
func (c *Ctrl) ValidateRequestWithEstimatedFee(ctx *gin.Context, req model.Request, estimatedFee string) error {
	// First validate the session token
	if err := c.ValidateSession(ctx); err != nil {
		return errors.Wrap(err, "session validation failed")
	}
	
	contractAccount, err := c.contract.GetUserAccount(ctx, common.HexToAddress(req.UserAddress))
	if err != nil {
		return errors.Wrap(err, "get account from contract")
	}

	if c.teeService.Address != contractAccount.TeeSignerAddress {
		return errors.New("user not acknowledge the provider")
	}

	account, err := c.GetOrCreateAccount(ctx, req.UserAddress)
	if err != nil {
		return err
	}

	// Use estimated fee for validation
	err = c.validateBalanceAdequacy(ctx, account, estimatedFee)
	if err != nil {
		return err
	}
	return nil
}


func (c *Ctrl) validateBalanceAdequacy(ctx *gin.Context, account model.User, fee string) error {
	if account.LockBalance == nil {
		return errors.New("nil lockBalance in account")
	}

	// Calculate response fee reservation
	responseFeeReservation, err := util.Multiply(c.Service.OutputPrice, constant.ResponseFeeReservationFactor)
	if err != nil {
		return errors.Wrap(err, "calculate response fee reservation")
	}

	// Use optimized calculation for unsettled fee using database aggregation
	unsettledFee, err := c.db.CalculateUnsettledFee(account.User, c.Service.InputPrice, c.Service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "calculate unsettled fee")
	}

	// Add input fee, unsettled fee, and response fee reservation
	totalWithInput, err := util.Add(fee, unsettledFee.String())
	if err != nil {
		return err
	}
	total, err := util.Add(totalWithInput, responseFeeReservation)
	if err != nil {
		return err
	}

	cmp1, err := util.Compare(total, account.LockBalance)
	if err != nil {
		return err
	}
	if cmp1 <= 0 {
		return nil
	}

	// reload account and repeat the check
	if err := c.SyncUserAccount(ctx, common.HexToAddress(account.User)); err != nil {
		return err
	}
	newAccount, err := c.GetOrCreateAccount(ctx, account.User)
	if err != nil {
		return err
	}
	
	// Recalculate unsettled fee after sync using optimized method
	unsettledFeeNew, err := c.db.CalculateUnsettledFee(account.User, c.Service.InputPrice, c.Service.OutputPrice)
	if err != nil {
		return errors.Wrap(err, "recalculate unsettled fee")
	}
	
	totalWithInputNew, err := util.Add(fee, unsettledFeeNew.String())
	if err != nil {
		return err
	}
	totalNew, err := util.Add(totalWithInputNew, responseFeeReservation)
	if err != nil {
		return err
	}
	cmp2, err := util.Compare(totalNew, newAccount.LockBalance)
	if err != nil {
		return err
	}
	if cmp2 <= 0 {
		return nil
	}
	ctx.Set("ignoreError", true)
	return fmt.Errorf("insufficient balance, total fee of %s (including response reservation) exceeds the available balance of %s", totalNew.String(), *newAccount.LockBalance)
}

func updateRequestField(req *model.Request, key, value string) error {
	switch key {
	case "Address":
		req.UserAddress = value
	case "VLLM-Proxy":
		v, err := strconv.ParseBool(value)
		if err != nil {
			v = false
		}
		req.VLLMProxy = v
	case "Session-Token", "Session-Signature":
		// These headers are used for validation only, not stored in the request
		// They are processed in ValidateSession method
		return nil
	default:
		return errors.Wrapf(errors.New("unexpected Header"), "%s", key)
	}
	return nil
}
