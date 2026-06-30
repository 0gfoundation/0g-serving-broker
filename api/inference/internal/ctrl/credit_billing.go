package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"

	"github.com/0glabs/0g-serving-broker/inference/internal/creditbilling"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// creditDeductTimeout bounds the post-response deduct call to the credit
// service. Billing runs on its own context (not the request context) so a
// client disconnect after the response cannot cancel the charge.
const creditDeductTimeout = 5 * time.Second

// creditEnabled reports whether off-chain credit billing is active for this
// provider (set when config.Service.CreditBilling.Enable and a client built).
func (c *Ctrl) creditEnabled() bool { return c.creditClient != nil }

// checkCreditBalance is the admission gate for credit providers. It queries the
// credit service for the user's balance and rejects fail-closed: an
// insufficient balance is a client-caused rejection (ignoreError), while a
// service/transport error is left unflagged so the broker-fault alert fires.
func (c *Ctrl) checkCreditBalance(ctx *gin.Context, userAddress string) error {
	balance, _, err := c.creditClient.Balance(ctx.Request.Context(), userAddress, c.contract.ProviderAddress)
	if err != nil {
		return errors.Wrap(err, "credit balance check failed")
	}
	if balance <= c.Service.CreditBilling.MinBalanceMicroUsd {
		ctx.Set("ignoreError", true)
		return errors.New("insufficient credit balance")
	}
	return nil
}

// billCreditChat builds, TEE-signs, and submits a billing receipt for a chatbot
// request to the credit service. It is called post-response (the response is
// already delivered to the client), so any failure — including insufficient
// balance — is logged as a billing loss rather than surfaced to the client; the
// admission gate (checkCreditBalance) is what prevents serving the unpayable.
func (c *Ctrl) billCreditChat(reqBody, respData []byte, promptTokens, completionTokens int64, reqModel model.Request) {
	feeMicro, err := creditbilling.FeeMicroUsd(promptTokens, completionTokens, c.creditInMicro, c.creditOutMicro)
	if err != nil {
		c.logger.Errorf("credit billing: fee computation failed for %s: %v", reqModel.RequestHash, err)
		return
	}

	reqHash := sha256.Sum256(reqBody)
	respHash := sha256.Sum256(respData)
	// Derive a per-request nonce from the unique request id so two byte-identical
	// requests (same reqHash/respHash) still hash to distinct receiptIDs.
	nonce := binary.BigEndian.Uint64(crypto.Keccak256([]byte(reqModel.RequestHash))[:8])

	rec := creditbilling.Receipt{
		User:        reqModel.UserAddress,
		Provider:    c.contract.ProviderAddress,
		ReqHash:     hex.EncodeToString(reqHash[:]),
		RespHash:    hex.EncodeToString(respHash[:]),
		InputCount:  promptTokens,
		OutputCount: completionTokens,
		FeeMicroUsd: strconv.FormatInt(feeMicro, 10),
		Nonce:       nonce,
		Timestamp:   time.Now().Unix(),
	}

	sig, err := creditbilling.Sign(c.teeService, rec)
	if err != nil {
		c.logger.Errorf("credit billing: sign receipt failed for %s: %v", reqModel.RequestHash, err)
		return
	}

	dctx, cancel := context.WithTimeout(context.Background(), creditDeductTimeout)
	defer cancel()
	res, err := c.creditClient.Deduct(dctx, rec, sig)
	if err != nil {
		c.logger.Errorf("credit billing: deduct failed for %s (user %s, fee %d micro-USD): %v",
			reqModel.RequestHash, reqModel.UserAddress, feeMicro, err)
		return
	}
	c.logger.Debugf("credit billing: charged %d micro-USD for %s (balance %d, replay=%v)",
		res.SettledMicroUsd, reqModel.RequestHash, res.BalanceMicroUsd, res.Replay)
}
