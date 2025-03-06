package ctrl

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"golang.org/x/net/context"

	"github.com/0glabs/0g-serving-broker/common/errors"
	constant "github.com/0glabs/0g-serving-broker/inference-router/const"
)

type AttestationReport struct {
	SigningAddress string `json:"signing_address"`
	NvidiaPayload  string `json:"nvidia_payload"`
	IntelQuote     string `json:"intel_quote"`
}

func (c *Ctrl) GetSigningAddress(ctx *gin.Context, providerAddress, model string) (string, error) {
	key := providerAddress + model + "signing_address"
	value, found := c.svcCache.Get(key)
	if found {
		signingAddress, ok := value.(string)
		if !ok {
			return "", errors.New("invalid signing address in cache")
		}
		return signingAddress, nil
	}

	body, err := c.FetchAttestationReport(ctx, providerAddress, model)
	if err != nil {
		handleBrokerError(ctx, err, "fetch attestation report")
		return "", err
	}

	report, err := extractFieldsFromBody(body)
	if err != nil {
		handleBrokerError(ctx, err, "extract fields from body")
		return "", err
	}
	c.svcCache.Set(key, report.SigningAddress, cache.DefaultExpiration)
	return report.SigningAddress, nil
}

func (c *Ctrl) FetchAttestationReport(ctx *gin.Context, providerAddress, model string) ([]byte, error) {
	extractor, err := c.GetExtractor(ctx, providerAddress)
	if err != nil {
		return nil, errors.Wrap(err, "get extractor")
	}
	svc := extractor.GetSvcInfo()

	// Build the target URL with query parameters
	targetURL := svc.Url + constant.ServicePrefix
	targetURL += "/attestation/report?model=" + model
	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "prepare request")
	}

	authorization := ctx.Request.Header.Get("Authorization")
	if authorization != "" {
		req.Header.Set("Authorization", ctx.Request.Header.Get("Authorization"))
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "get response from provider")
	}
	defer resp.Body.Close()

	// Read the response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrap(err, "read from body")
	}

	return body, nil
}

func extractFieldsFromBody(body []byte) (*AttestationReport, error) {
	var data AttestationReport
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("error unmarshalling JSON: %w", err)
	}

	return &data, nil
}

type ResponseSignature struct {
	Signature string `json:"signature"`
	Text      string `json:"text"`
}

func (c *Ctrl) FetchSignatureByChatID(ctx context.Context, providerAddress, chatID, modelName, secretHeader string) (*ResponseSignature, error) {
	extractor, err := c.GetExtractor(ctx, providerAddress)
	if err != nil {
		return nil, errors.Wrap(err, "get extractor")
	}
	svc := extractor.GetSvcInfo()

	url := fmt.Sprintf("%s%s/signature/%s?model=%s", svc.Url, constant.ServicePrefix, chatID, modelName)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", secretHeader)

	client := &http.Client{}
	resp, err := client.Do(req)

	if err != nil {
		return nil, fmt.Errorf("failed to fetch signature: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("getting signature error")
	}

	var responseSignature ResponseSignature
	err = json.NewDecoder(resp.Body).Decode(&responseSignature)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &responseSignature, nil
}

func (c *Ctrl) VerifySignature(message, signature, expectedAddress string) error {
	messageHash := crypto.Keccak256Hash([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)))

	sigBytes, err := hex.DecodeString(signature[2:])
	if err != nil {
		return err
	}

	if len(sigBytes) != 65 {
		return errors.New("invalid signature length")
	}

	v := sigBytes[64] - 27
	pubKey, err := crypto.SigToPub(messageHash.Bytes(), append(sigBytes[:64], v))
	if err != nil {
		return err

	}

	recoveredAddress := crypto.PubkeyToAddress(*pubKey).Hex()
	if !bytes.EqualFold([]byte(recoveredAddress), []byte(expectedAddress)) {
		return errors.New("signature verification failed")
	}

	return nil
}
