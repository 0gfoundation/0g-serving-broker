package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/controller/internal/ctrl"
)

// SessionToken represents the structure of a session token for controller auth
type SessionToken struct {
	Address   string `json:"address"`   // Admin wallet address
	Timestamp int64  `json:"timestamp"` // Token creation timestamp (milliseconds)
	ExpiresAt int64  `json:"expiresAt"` // Token expiration timestamp (milliseconds), 0 = never expires
	Nonce     string `json:"nonce"`     // Random nonce to prevent replay attacks
}

// AuthMiddleware creates a middleware that validates admin authentication
func AuthMiddleware(controller *ctrl.Ctrl) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		authHeader := ctx.GetHeader("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer app-sk-") {
			ctx.JSON(http.StatusUnauthorized, gin.H{
				"error": "missing or invalid Authorization header, must be Bearer app-sk-<base64(rawMessage|signature)>",
			})
			ctx.Abort()
			return
		}

		// Decode base64 payload
		enc := strings.TrimPrefix(authHeader, "Bearer app-sk-")
		decodedBytes, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "invalid base64 in Authorization header"})
			ctx.Abort()
			return
		}

		decoded := string(decodedBytes)
		parts := strings.SplitN(decoded, "|", 2)
		if len(parts) != 2 {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "invalid Authorization payload, expect base64(rawMessage|signature)"})
			ctx.Abort()
			return
		}

		tokenStr := parts[0]
		signature := parts[1]

		// Parse session token
		var token SessionToken
		if err := json.Unmarshal([]byte(tokenStr), &token); err != nil {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "invalid session token format"})
			ctx.Abort()
			return
		}

		// Check token expiration
		if token.ExpiresAt > 0 && time.Now().UnixMilli() > token.ExpiresAt {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "session token expired"})
			ctx.Abort()
			return
		}

		// Verify signature and recover address
		recoveredAddr, err := recoverAddress(tokenStr, signature)
		if err != nil {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "signature verification failed: " + err.Error()})
			ctx.Abort()
			return
		}

		// Check if recovered address matches claimed address
		if !strings.EqualFold(recoveredAddr, token.Address) {
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "signature verification failed: address mismatch"})
			ctx.Abort()
			return
		}

		// Check if address is in admin whitelist
		if !controller.IsAdminAddress(recoveredAddr) {
			ctx.JSON(http.StatusForbidden, gin.H{"error": "unauthorized: address not in admin list"})
			ctx.Abort()
			return
		}

		// Store admin address in context for later use
		ctx.Set("adminAddress", recoveredAddr)
		ctx.Next()
	}
}

// recoverAddress recovers the signer address from message and signature
func recoverAddress(message string, signature string) (string, error) {
	// Calculate message hash
	messageHash := crypto.Keccak256Hash([]byte(message))

	// Create Ethereum personal message hash (EIP-191)
	prefixedMsg := crypto.Keccak256Hash(
		[]byte("\x19Ethereum Signed Message:\n32"),
		messageHash.Bytes(),
	)

	// Decode signature from hex
	sigBytes, err := hexutil.Decode(signature)
	if err != nil {
		return "", err
	}

	// Ethereum signatures are 65 bytes: R (32) + S (32) + V (1)
	if len(sigBytes) != 65 {
		return "", err
	}

	// Adjust V value for Ethereum signature recovery
	v := sigBytes[64]
	if v >= 27 {
		v -= 27
	}

	// Recover public key from signature
	pubKey, err := crypto.SigToPub(prefixedMsg.Bytes(), append(sigBytes[:64], v))
	if err != nil {
		return "", err
	}

	// Get address from public key
	recoveredAddr := crypto.PubkeyToAddress(*pubKey)

	return recoveredAddr.Hex(), nil
}
