// Command gentoken generates a signed session token (the "Authorization: Bearer
// app-sk-..." header) accepted by the inference broker's ValidateSession check.
//
// It exists purely as a local-testing helper — e.g. for manually exercising a
// whitelisted wallet address against a broker without pulling in the SDK. It
// reproduces exactly the signing scheme implemented in
// internal/ctrl/request.go (ValidateSession) and mirrored in
// integration_test/helpers_test.go (createAuthHeader).
//
// Example:
//
//	go run ./cmd/gentoken \
//	  --private-key 0xabc123... \
//	  --provider 0xProviderBrokerAddress \
//	  --broker-url http://localhost:8080
package main

import (
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
)

func main() {
	privateKeyHex := flag.String("private-key", "", "wallet private key (hex, with or without 0x prefix). Can also be set via PRIVATE_KEY env var to keep it out of shell history")
	provider := flag.String("provider", "", "provider's on-chain address, i.e. the broker's own signing address (must match the target broker's configured providerAddress)")
	brokerURL := flag.String("broker-url", "", "optional: broker base URL, only used to print a ready-to-run curl example")
	tokenID := flag.Uint("token-id", 255, "token ID: 0-254 for a persistent token, 255 (default) for an ephemeral token")
	ttlSeconds := flag.Int64("ttl", 3600, "expiry, in seconds from now. Required (and capped at 24h) for ephemeral tokens (token-id=255). Use --ttl 0 with a persistent token-id for a never-expiring token")
	generation := flag.Uint64("generation", 0, "token generation, must match the account's on-chain generation counter (0 for a fresh/unregistered account)")
	flag.Parse()

	if *privateKeyHex == "" {
		*privateKeyHex = os.Getenv("PRIVATE_KEY")
	}
	if *privateKeyHex == "" || *provider == "" {
		fmt.Fprintln(os.Stderr, "usage: gentoken --private-key 0x... --provider 0x... [--broker-url http://...] [--token-id 255] [--ttl 3600]")
		os.Exit(1)
	}
	if *tokenID > 255 {
		fmt.Fprintln(os.Stderr, "error: --token-id must be between 0 and 255")
		os.Exit(1)
	}
	if *tokenID == ctrl.EphemeralTokenId && *ttlSeconds <= 0 {
		fmt.Fprintln(os.Stderr, "error: ephemeral tokens (token-id=255) require --ttl > 0")
		os.Exit(1)
	}

	privateKey, err := crypto.HexToECDSA(strings.TrimPrefix(*privateKeyHex, "0x"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid private key: %v\n", err)
		os.Exit(1)
	}

	header, address, err := generateAuthHeader(privateKey, *provider, uint8(*tokenID), *ttlSeconds, *generation)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Address:       %s\n", address)
	fmt.Printf("Authorization: %s\n", header)

	if *brokerURL != "" {
		url := strings.TrimSuffix(*brokerURL, "/")
		fmt.Printf("\nExample:\ncurl -H 'Authorization: %s' \\\n     -H 'Content-Type: application/json' \\\n     %s/v1/proxy/<path>\n", header, url)
	}
}

// generateAuthHeader mirrors ValidateSession's expected signing scheme:
// EIP-191-prefixed Keccak256 hash of the JSON-encoded SessionToken, signed
// with the given private key.
func generateAuthHeader(privateKey *ecdsa.PrivateKey, provider string, tokenID uint8, ttlSeconds int64, generation uint64) (string, string, error) {
	address := crypto.PubkeyToAddress(privateKey.PublicKey)

	now := time.Now()
	var expiresAt int64
	if ttlSeconds > 0 {
		expiresAt = now.Add(time.Duration(ttlSeconds) * time.Second).UnixMilli()
	}

	token := ctrl.SessionToken{
		Address:    address.Hex(),
		Provider:   provider,
		Timestamp:  now.UnixMilli(),
		ExpiresAt:  expiresAt,
		Nonce:      fmt.Sprintf("gentoken-%d", now.UnixNano()),
		Generation: generation,
		TokenId:    tokenID,
	}

	tokenJSON, err := json.Marshal(token)
	if err != nil {
		return "", "", fmt.Errorf("marshal session token: %w", err)
	}

	messageHash := crypto.Keccak256Hash(tokenJSON)
	prefixedMsg := crypto.Keccak256Hash(
		[]byte("\x19Ethereum Signed Message:\n32"),
		messageHash.Bytes(),
	)

	sig, err := crypto.Sign(prefixedMsg.Bytes(), privateKey)
	if err != nil {
		return "", "", fmt.Errorf("sign session token: %w", err)
	}
	sig[64] += 27 // adjust V for Ethereum recovery, matches ValidateSession's expectation

	payload := string(tokenJSON) + "|" + hexutil.Encode(sig)
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	return "Bearer app-sk-" + encoded, address.Hex(), nil
}
