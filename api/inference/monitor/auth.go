package monitor

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/util"
)

// MaxMetricsTokenLifetime bounds how far in the future a metrics token may
// expire. The scraper signs a fresh token every minute; a token captured in
// transit is useless after this.
const MaxMetricsTokenLifetime = 10 * time.Minute

// MetricsTokenMessage is the text the scraper signs (EIP-191 personal_sign).
func MetricsTokenMessage(expiry int64) string {
	return fmt.Sprintf("0g-metrics:%d", expiry)
}

// VerifyMetricsToken checks "Bearer <expiry>.<0x-signature>" against the one
// address allowed to read metrics.
//
// Why a signature and not a shared secret: the broker holds only the
// reader's ADDRESS, which is public, so a compromised CVM leaks nothing that
// opens any other provider's metrics. The signing key stays with the scraper
// (router-mainnet prom-agent).
//
// ponytail: the message does not name the provider, so a token one broker
// receives could be replayed against another for up to MaxMetricsTokenLifetime.
// It only reads metrics; bind the serving host into the message if that matters.
func VerifyMetricsToken(header string, allowed common.Address, now time.Time) error {
	if allowed == (common.Address{}) {
		return fmt.Errorf("no metrics reader configured (monitor.metricsReader)")
	}
	tok, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return fmt.Errorf("missing bearer token")
	}
	expStr, sigHex, ok := strings.Cut(tok, ".")
	if !ok {
		return fmt.Errorf("malformed token")
	}
	expiry, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return fmt.Errorf("malformed expiry")
	}
	exp := time.Unix(expiry, 0)
	if !exp.After(now) {
		return fmt.Errorf("token expired")
	}
	if exp.Sub(now) > MaxMetricsTokenLifetime {
		return fmt.Errorf("token lifetime exceeds %s", MaxMetricsTokenLifetime)
	}
	sig, err := hexutil.Decode(sigHex)
	if err != nil {
		return fmt.Errorf("malformed signature")
	}
	signer, err := util.RecoverSigner(accounts.TextHash([]byte(MetricsTokenMessage(expiry))), sig)
	if err != nil {
		return fmt.Errorf("bad signature")
	}
	if signer != allowed {
		return fmt.Errorf("signer not allowed")
	}
	return nil
}

// RequireMetricsReader rejects every request that does not carry a valid token
// from reader. There is no "off": with no reader configured, metrics are
// unreadable, which is the safe failure for a TEE broker.
func RequireMetricsReader(reader common.Address) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := VerifyMetricsToken(c.GetHeader("Authorization"), reader, time.Now()); err != nil {
			c.Set("ignoreError", true)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "metrics: " + err.Error()})
			return
		}
		c.Next()
	}
}
