package ctrl

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// EphemeralTokenId is the special token ID reserved for ephemeral tokens.
// Ephemeral tokens (tokenId=255) are not checked against the revoked bitmap,
// only generation check applies. This allows unlimited ephemeral tokens without
// consuming the 0-254 tokenId quota.
const EphemeralTokenId = 255

// EphemeralTokenMaxDuration is the maximum allowed duration for ephemeral tokens (24 hours in milliseconds).
// Ephemeral tokens must have an expiration time and cannot exceed this duration.
const EphemeralTokenMaxDuration = 24 * 60 * 60 * 1000 // 24 hours in milliseconds

// SessionToken represents the structure of a session token
// Used for both user sessions and provider authentication
type SessionToken struct {
	Address    string `json:"address"`    // User/Provider address
	Provider   string `json:"provider"`   // Target provider address
	Timestamp  int64  `json:"timestamp"`  // Token creation timestamp
	ExpiresAt  int64  `json:"expiresAt"`  // Token expiration timestamp (0 = never expires)
	Nonce      string `json:"nonce"`      // Random nonce to prevent replay attacks
	Generation uint64 `json:"generation"` // Token generation for batch revocation
	TokenId    uint8  `json:"tokenId"`    // Token ID: 0-254 for persistent tokens, 255 for ephemeral
}

// SessionValidationCache stores validated sessions to avoid repeated signature verification
// Since the cache key contains all validation data, we only need to store minimal info
type SessionValidationCache struct {
	ValidatedAt int64 // Timestamp when validation occurred
}

func (c *Ctrl) CreateRequest(req model.Request) error {
	return errors.Wrap(c.db.CreateRequest(req), "create request in db")
}

func (c *Ctrl) ListRequest(q model.RequestListOptions) ([]model.Request, string, error) {
	list, fee, err := c.db.ListRequest(q)
	if err != nil {
		return nil, "0", errors.Wrap(err, "list service from db")
	}
	return list, fee.String(), nil
}

// ValidateSession validates the session token and signature
func (c *Ctrl) ValidateSession(ctx *gin.Context) (string, error) {
	authHeader := ctx.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer app-sk-") {
		return "", errors.New("missing or invalid Authorization header, must be Bearer app-sk-<base64(rawMessage:signature)>")
	}

	enc := strings.TrimPrefix(authHeader, "Bearer app-sk-")
	decodedBytes, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", errors.Wrap(err, "invalid base64 in Authorization header")
	}
	decoded := string(decodedBytes)
	parts := strings.SplitN(decoded, "|", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid Authorization payload, expect base64(rawMessage:signature)")
	}
	tokenStr := parts[0]
	signature := parts[1]

	var token SessionToken
	if err := json.Unmarshal([]byte(tokenStr), &token); err != nil {
		return "", errors.Wrap(err, "invalid session token format in Authorization")
	}
	address := token.Address

	// Validate provider matches this provider
	if !strings.EqualFold(token.Provider, c.contract.ProviderAddress) {
		return "", errors.New("session token is for different provider")
	}

	// Validate ephemeral token constraints
	if token.TokenId == EphemeralTokenId {
		// Ephemeral tokens MUST have an expiration time
		if token.ExpiresAt == 0 {
			return "", errors.New("ephemeral token must have an expiration time")
		}
		// Ephemeral tokens cannot exceed 24 hours duration
		tokenDuration := token.ExpiresAt - token.Timestamp
		if tokenDuration > EphemeralTokenMaxDuration {
			return "", errors.New("ephemeral token duration cannot exceed 24 hours")
		}
	}

	// Check token expiration (convert milliseconds to seconds)
	// ExpiresAt == 0 means never expires (only allowed for persistent tokens)
	if token.ExpiresAt > 0 && time.Now().Unix() > token.ExpiresAt/1000 {
		return "", errors.New("session token expired")
	}

	// Validate generation and revocation status from contract
	if err := c.validateTokenRevocation(ctx, token); err != nil {
		return "", err
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
		return address, nil
	}

	// Verify signature following the same pattern as verifySignature in setup.go
	messageHash := crypto.Keccak256Hash([]byte(tokenStr))

	// Create Ethereum personal message hash (matches the client signMessage behavior)
	// Following the same pattern as getHash in setup.go
	prefixedMsg := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), messageHash.Bytes())

	// Decode signature from hex
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return "", errors.Wrap(err, "invalid signature format")
	}

	// Ethereum signatures are 65 bytes: R (32) + S (32) + V (1)
	if len(sigBytes) != 65 {
		return "", errors.New("invalid signature length")
	}

	// Adjust V value for Ethereum signature recovery (same as verifySignature)
	v1 := sigBytes[64] - 27
	pubKey, err := crypto.SigToPub(prefixedMsg.Bytes(), append(sigBytes[:64], v1))
	if err != nil {
		return "", errors.Wrap(err, "failed to recover public key from signature")
	}

	// Get address from public key
	recoveredAddr := crypto.PubkeyToAddress(*pubKey)

	// Verify recovered address matches claimed address (same as verifySignature)
	if !strings.EqualFold(recoveredAddr.Hex(), address) {
		return "", errors.New("signature verification failed: address mismatch")
	}

	// Cache the validated session
	// Cache will expire based on the cache configuration (5 minutes)
	c.sessionCache.Set(cacheKey, SessionValidationCache{
		ValidatedAt: time.Now().Unix(),
	}, cache.DefaultExpiration)

	// Session is valid
	return address, nil
}

// ValidateProviderAuth validates that the request is from the provider itself
func (c *Ctrl) ValidateProviderAuth(ctx *gin.Context) error {
	// Get headers (exactly the same as ValidateSession for consistency)
	address := ctx.GetHeader("Address")
	tokenStr := ctx.GetHeader("Session-Token")
	signature := ctx.GetHeader("Session-Signature")

	// Check if all required headers are present
	if address == "" || tokenStr == "" || signature == "" {
		return errors.New("missing provider authentication headers, please make sure your client includes Address, Session-Token, and Session-Signature headers")
	}

	// Parse provider token (reuse SessionToken structure)
	var token SessionToken
	if err := json.Unmarshal([]byte(tokenStr), &token); err != nil {
		return errors.Wrap(err, "invalid provider token format")
	}

	// Validate address matches token address
	if !strings.EqualFold(token.Address, address) {
		return errors.New("address mismatch in provider token")
	}

	// Validate that both address and provider field match this provider's address
	if !strings.EqualFold(token.Address, c.contract.ProviderAddress) {
		return errors.New("unauthorized: not the provider")
	}

	if !strings.EqualFold(token.Provider, c.contract.ProviderAddress) {
		return errors.New("provider field mismatch")
	}

	// Check token expiration (convert milliseconds to seconds)
	if time.Now().Unix() > token.ExpiresAt/1000 {
		return errors.New("provider token expired")
	}

	// Create hash values for secure caching
	tokenHashValue := crypto.Keccak256Hash([]byte(tokenStr)).Hex()
	signatureHashValue := crypto.Keccak256Hash([]byte(signature)).Hex()

	// Check session cache to avoid repeated signature verification
	cacheKey := fmt.Sprintf("provider:%s:%s:%s:%s", address, token.Nonce, tokenHashValue, signatureHashValue)
	if _, found := c.sessionCache.Get(cacheKey); found {
		return nil
	}

	// Verify signature following the same pattern as ValidateSession
	messageHash := crypto.Keccak256Hash([]byte(tokenStr))

	// Create Ethereum personal message hash
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

	// Adjust V value for Ethereum signature recovery
	v1 := sigBytes[64] - 27
	pubKey, err := crypto.SigToPub(prefixedMsg.Bytes(), append(sigBytes[:64], v1))
	if err != nil {
		return errors.Wrap(err, "failed to recover public key from signature")
	}

	// Get address from public key
	recoveredAddr := crypto.PubkeyToAddress(*pubKey)

	// Verify recovered address matches claimed address
	if !strings.EqualFold(recoveredAddr.Hex(), address) {
		return errors.New("signature verification failed: address mismatch")
	}

	// Cache the validated provider authentication
	c.sessionCache.Set(cacheKey, SessionValidationCache{
		ValidatedAt: time.Now().Unix(),
	}, cache.DefaultExpiration)

	// Provider authentication successful
	return nil
}

// ValidateRequestWithEstimatedFee validates the request using an estimated fee
// This is used before the actual token count is known from the LLM
func (c *Ctrl) ValidateRequestWithEstimatedFee(ctx *gin.Context, req model.Request, estimatedFee string) error {
	// Try to get contract account from cache first
	userAddress := common.HexToAddress(req.UserAddress)
	accountCacheKey := userAddress.Hex()

	var contractAccount *contract.Account
	if cachedAccount, found := c.contractAccountCache.Get(accountCacheKey); found {
		// Use cached account data
		if acc, ok := cachedAccount.(*contract.Account); ok {
			contractAccount = acc
		}
	}

	// If not in cache or invalid, fetch from contract
	if contractAccount == nil {
		fetchedAccount, err := c.contract.GetUserAccount(ctx, userAddress)
		if err != nil {
			ctx.Set("ignoreError", true)
			return errors.Wrap(err, "get account from contract, account not exist")
		}
		contractAccount = &fetchedAccount

		// Cache the account data
		c.contractAccountCache.Set(accountCacheKey, contractAccount, cache.DefaultExpiration)
	}

	if contractAccount.Acknowledged == false {
		// User-caused error: user hasn't acknowledged the provider
		ctx.Set("ignoreError", true)
		return errors.New("user not acknowledge the provider, please use acknowledgeProviderSigner function in sdk first, it will take effect in 2 minutes")
	}

	// Try to get service from cache first
	serviceCacheKey := "current_service"
	var service model.Service

	if cachedService, found := c.serviceCache.Get(serviceCacheKey); found {
		// Use cached service data
		if svc, ok := cachedService.(model.Service); ok {
			service = svc
		} else {
			// Cache data is invalid, fetch from contract
			service, err := c.GetService(ctx)
			if err != nil {
				return errors.Wrap(err, "get service from context")
			}
			// Update cache with fresh data
			c.serviceCache.Set(serviceCacheKey, service, cache.DefaultExpiration)
		}
	} else {
		// Not in cache, fetch from contract
		var err error
		service, err = c.GetService(ctx)
		if err != nil {
			return errors.Wrap(err, "get service from context")
		}
		// Cache the service data
		c.serviceCache.Set(serviceCacheKey, service, cache.DefaultExpiration)
	}

	if service.TeeSignerAcknowledged == false {
		return errors.New("service not acknowledge the tee signer")
	}

	account, err := c.GetOrCreateAccount(ctx, req.UserAddress)
	if err != nil {
		return err
	}

	// Cross-check DB lockBalance against the contract account balance we already fetched.
	// The DB lockBalance can become stale (e.g., after settlement deducts fees on-chain
	// but DB isn't synced yet). Use the lesser of the two to prevent over-spending.
	contractLockBalance := new(big.Int).Sub(contractAccount.Balance, contractAccount.PendingRefund)
	if account.LockBalance != nil {
		dbBalance, ok := new(big.Int).SetString(*account.LockBalance, 10)
		if ok && dbBalance.Cmp(contractLockBalance) > 0 {
			// DB balance is higher than contract balance — DB is stale, use contract value
			corrected := contractLockBalance.String()
			account.LockBalance = &corrected
		}
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

	// Use fixed minimum locked balance for all service types (5 0G)
	responseFeeReservation, ok := new(big.Int).SetString(constant.MinimumLockedBalance, 10)
	if !ok {
		return errors.New("invalid MinimumLockedBalance constant")
	}

	feeBI, ok := new(big.Int).SetString(fee, 10)
	if !ok {
		return errors.New("invalid fee value")
	}

	lockBalance, ok := new(big.Int).SetString(*account.LockBalance, 10)
	if !ok {
		return errors.New("invalid lock balance value")
	}

	// Use optimized calculation for unsettled fee using database aggregation
	unsettledFee, err := c.db.CalculateUnsettledFee(account.User)
	if err != nil {
		return errors.Wrap(err, "calculate unsettled fee")
	}

	if balanceSufficient(lockBalance, feeBI, unsettledFee, responseFeeReservation) {
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
	unsettledFeeNew, err := c.db.CalculateUnsettledFee(account.User)
	if err != nil {
		return errors.Wrap(err, "recalculate unsettled fee")
	}

	if newAccount.LockBalance == nil {
		return errors.New("nil lockBalance after sync")
	}

	newLockBalance, ok := new(big.Int).SetString(*newAccount.LockBalance, 10)
	if !ok {
		return errors.New("invalid lock balance after sync")
	}

	if balanceSufficient(newLockBalance, feeBI, unsettledFeeNew, responseFeeReservation) {
		return nil
	}
	ctx.Set("ignoreError", true)

	// Convert neuron to 0G for display (1 0G = 10^18 neuron)
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	toZG := func(s string) string {
		v := new(big.Int)
		v.SetString(s, 10)
		return new(big.Float).Quo(new(big.Float).SetInt(v), new(big.Float).SetInt(divisor)).Text('f', 6)
	}

	totalNew := new(big.Int).Add(feeBI, unsettledFeeNew)
	totalNew.Add(totalNew, responseFeeReservation)

	balanceZG := toZG(*newAccount.LockBalance)
	unsettledZG := toZG(unsettledFeeNew.String())
	currentFeeZG := toZG(fee)
	minLockedZG := toZG(responseFeeReservation.String())

	return fmt.Errorf("insufficient balance: your locked balance is %s 0G, but the required minimum is %s 0G "+
		"(breakdown: minimum reserve %s 0G + unsettled fees %s 0G + current request fee %s 0G). "+
		"Please add more funds with: 0g-compute-cli transfer-fund --provider %s --amount <amount>",
		balanceZG, toZG(totalNew.String()), minLockedZG, unsettledZG, currentFeeZG, c.contract.ProviderAddress)
}

// balanceSufficient reports whether lockBalance is sufficient to cover
// inputFee + unsettledFee + minReserve. All values are in neuron units.
// Returns true when lockBalance >= inputFee + unsettledFee + minReserve.
func balanceSufficient(lockBalance, inputFee, unsettledFee, minReserve *big.Int) bool {
	total := new(big.Int).Add(inputFee, unsettledFee)
	total.Add(total, minReserve)
	return lockBalance.Cmp(total) >= 0
}

// GetUnsettledFee returns the total unsettled fee for a given user address.
// This is used by clients to determine how much balance the provider requires
// beyond the minimum locked balance.
func (c *Ctrl) GetUnsettledFee(userAddress string) (*big.Int, error) {
	return c.db.CalculateUnsettledFee(userAddress)
}

// validateTokenRevocation checks if the session token has been revoked
// by comparing generation and checking the revoked bitmap from the contract.
//
// For ephemeral tokens (tokenId=255), only generation check is performed.
// For persistent tokens (tokenId=0-254), both generation and bitmap are checked.
func (c *Ctrl) validateTokenRevocation(ctx *gin.Context, token SessionToken) error {
	// Get account from contract cache
	userAddress := common.HexToAddress(token.Address)
	accountCacheKey := userAddress.Hex()

	var contractAccount *contract.Account
	if cachedAccount, found := c.contractAccountCache.Get(accountCacheKey); found {
		if acc, ok := cachedAccount.(*contract.Account); ok {
			contractAccount = acc
		}
	}

	// If not in cache, fetch from contract
	if contractAccount == nil {
		fetchedAccount, err := c.contract.GetUserAccount(ctx, userAddress)
		if err != nil {
			// If account doesn't exist, generation and bitmap are 0, which is valid
			// for a new token with generation=0
			return nil
		}
		contractAccount = &fetchedAccount
		c.contractAccountCache.Set(accountCacheKey, contractAccount, cache.DefaultExpiration)
	}

	// Check generation (batch revocation) - applies to ALL token types
	contractGeneration := uint64(0)
	if contractAccount.Generation != nil {
		contractGeneration = contractAccount.Generation.Uint64()
	}

	if token.Generation < contractGeneration {
		// Token's generation is older than current, it has been batch revoked
		return errors.New("session token has been revoked (generation expired)")
	}
	if token.Generation > contractGeneration {
		// Token's generation is from the future, invalid
		return errors.New("invalid session token (future generation)")
	}

	// Ephemeral tokens (tokenId=255) skip bitmap check
	// They can only be revoked via revokeAllTokens() which increments generation
	if token.TokenId == EphemeralTokenId {
		return nil
	}

	// Check bitmap (precise revocation) - only for persistent tokens (tokenId=0-254)
	// token.Generation == contractGeneration
	if contractAccount.RevokedBitmap != nil {
		// Check if the bit at position tokenId is set
		bitmap := contractAccount.RevokedBitmap
		tokenIdBit := new(big.Int).Lsh(big.NewInt(1), uint(token.TokenId))
		if new(big.Int).And(bitmap, tokenIdBit).Cmp(big.NewInt(0)) != 0 {
			return errors.New("session token has been revoked")
		}
	}

	return nil
}
