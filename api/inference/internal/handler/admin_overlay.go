package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// The admin overlay endpoint: the one way to change a running broker's configuration once
// the controller is gone. It writes the config overlay (inference/config/overlay.go), so it
// can change exactly what the overlay allowlist admits — prices, limits, the whitelist,
// settlement timing — and nothing that decides where a prompt goes. The broker and the
// event service notice the new file and restart on it (config.WatchOverlay).
//
// It replaces the controller's PUT /v1/config/core, and is built against the ways that
// one failed (security-relaunch-plan-20261106): it is served on the broker's own port, so
// there is no extra port to publish; it never returns the main config, so it cannot leak
// the private key in it; the admin addresses are a compose literal, so whoever can edit the
// encrypted config cannot add themselves; and every token is short-lived, single-use and
// bound to the exact request it authorizes.

const (
	// AdminAddressesEnvVar lists the addresses allowed to call the endpoint, comma
	// separated. Set it in the compose file, not in the encrypted config: the compose
	// file is what the quote's compose hash covers. Unset, the endpoint does not exist.
	AdminAddressesEnvVar = "ZG_ADMIN_ADDRESSES"

	adminTokenHeader     = "X-ZG-Admin-Token"
	adminSignatureHeader = "X-ZG-Admin-Signature"

	// adminTokenMaxLifetime bounds expiresAt - issuedAt. A token is useful for one
	// request, so anything longer only widens the window a stolen one is good for.
	adminTokenMaxLifetime = 5 * time.Minute
	// adminClockSkew tolerates an issuer clock slightly ahead of this one.
	adminClockSkew = time.Minute
	// adminMaxBody bounds the overlay. A real one is a few kilobytes.
	adminMaxBody = 256 << 10
)

// AdminToken is what an admin signs (EIP-191 personal_sign over the token's JSON bytes, as
// sent base64url-encoded in X-ZG-Admin-Token). It names the request it authorizes, so a
// captured token cannot be replayed against another method, path or body.
type AdminToken struct {
	Address    string `json:"address"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	BodySHA256 string `json:"bodySha256"`
	IssuedAt   int64  `json:"issuedAt"`  // unix milliseconds
	ExpiresAt  int64  `json:"expiresAt"` // unix milliseconds
	Nonce      string `json:"nonce"`
}

type adminAuth struct {
	allowed map[string]bool // lowercase 0x addresses
	now     func() time.Time

	mu   sync.Mutex
	seen map[string]int64 // nonce -> expiresAt
}

func newAdminAuth(list string) *adminAuth {
	a := &adminAuth{allowed: map[string]bool{}, now: time.Now, seen: map[string]int64{}}
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if common.IsHexAddress(s) {
			a.allowed[strings.ToLower(common.HexToAddress(s).Hex())] = true
		}
	}
	return a
}

var errAdminUnauthorized = errors.New("unauthorized")

// verify checks the token and signature on a request whose body has already been read.
// Every failure is reported as one error to the caller and with its reason to the log, so
// the endpoint does not tell a prober which check it failed.
func (a *adminAuth) verify(method, path string, body []byte, tokenB64, sigHex string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(tokenB64, "="))
	if err != nil {
		return "", fmt.Errorf("%w: token is not base64url", errAdminUnauthorized)
	}
	var tok AdminToken
	if err := json.Unmarshal(raw, &tok); err != nil {
		return "", fmt.Errorf("%w: token is not JSON", errAdminUnauthorized)
	}
	addr := strings.ToLower(tok.Address)
	if !a.allowed[addr] {
		return "", fmt.Errorf("%w: %s is not an admin", errAdminUnauthorized, tok.Address)
	}
	if tok.Method != method || tok.Path != path {
		return "", fmt.Errorf("%w: token is for %s %s, not %s %s", errAdminUnauthorized, tok.Method, tok.Path, method, path)
	}
	sum := sha256.Sum256(body)
	if !strings.EqualFold(tok.BodySHA256, hex.EncodeToString(sum[:])) {
		return "", fmt.Errorf("%w: token does not cover this body", errAdminUnauthorized)
	}
	if tok.Nonce == "" || len(tok.Nonce) > 128 {
		return "", fmt.Errorf("%w: missing or oversized nonce", errAdminUnauthorized)
	}
	issued, expires := time.UnixMilli(tok.IssuedAt), time.UnixMilli(tok.ExpiresAt)
	now := a.now()
	if !expires.After(issued) || expires.Sub(issued) > adminTokenMaxLifetime {
		return "", fmt.Errorf("%w: token lifetime must be positive and at most %s", errAdminUnauthorized, adminTokenMaxLifetime)
	}
	if now.Before(issued.Add(-adminClockSkew)) || now.After(expires) {
		return "", fmt.Errorf("%w: token is not valid now", errAdminUnauthorized)
	}

	sig, err := hexutil.Decode(sigHex)
	if err != nil {
		return "", fmt.Errorf("%w: signature is not hex", errAdminUnauthorized)
	}
	signer, err := util.RecoverSigner(accounts.TextHash(raw), sig)
	if err != nil || strings.ToLower(signer.Hex()) != addr {
		return "", fmt.Errorf("%w: signature is not %s's", errAdminUnauthorized, tok.Address)
	}

	// Single use. Checked last, so a bad signature cannot burn someone else's nonce.
	a.mu.Lock()
	defer a.mu.Unlock()
	for n, exp := range a.seen {
		if now.UnixMilli() > exp {
			delete(a.seen, n)
		}
	}
	if _, used := a.seen[tok.Nonce]; used {
		return "", fmt.Errorf("%w: nonce already used", errAdminUnauthorized)
	}
	a.seen[tok.Nonce] = tok.ExpiresAt
	return addr, nil
}

// registerAdminOverlay adds the endpoint when both an overlay file and admin addresses are
// configured; otherwise it does not exist.
func (h *Handler) registerAdminOverlay(group *gin.RouterGroup) {
	list := os.Getenv(AdminAddressesEnvVar)
	if list == "" || os.Getenv(config.OverlayFileEnvVar) == "" {
		return
	}
	auth := newAdminAuth(list)
	if len(auth.allowed) == 0 {
		h.logger.Warnf("[ADMIN-OVERLAY] %s has no valid address; the endpoint is off", AdminAddressesEnvVar)
		return
	}
	group.GET("/admin/overlay", h.adminOverlayHandler(auth))
	group.PUT("/admin/overlay", h.adminOverlayHandler(auth))
}

func (h *Handler) adminOverlayHandler(auth *adminAuth) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		body, err := io.ReadAll(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, adminMaxBody))
		if err != nil {
			ctx.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body too large"})
			return
		}
		admin, err := auth.verify(ctx.Request.Method, ctx.Request.URL.Path, body,
			ctx.GetHeader(adminTokenHeader), ctx.GetHeader(adminSignatureHeader))
		if err != nil {
			h.logger.Warnf("[ADMIN-OVERLAY] refused %s %s from %s: %v", ctx.Request.Method, ctx.Request.URL.Path, ctx.ClientIP(), err)
			ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}

		overlayPath := os.Getenv(config.OverlayFileEnvVar)
		if ctx.Request.Method == http.MethodGet {
			data, err := os.ReadFile(overlayPath)
			if err != nil && !os.IsNotExist(err) {
				ctx.JSON(http.StatusInternalServerError, gin.H{"error": "reading the overlay failed"})
				return
			}
			ctx.Data(http.StatusOK, "application/yaml", data)
			return
		}

		base, err := os.ReadFile(config.MainConfigPath())
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "reading the main config failed"})
			return
		}
		merged, keys, err := config.ApplyOverlay(base, body)
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		// The same validation the process runs at start, on the merged result, so an
		// overlay that would stop the broker from starting is refused here instead.
		if err := config.ValidateConfigContent(merged); err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "the config with this overlay does not validate: " + err.Error()})
			return
		}
		if err := writeFileAtomic(overlayPath, body); err != nil {
			h.logger.Errorf("[ADMIN-OVERLAY] writing %s: %v", overlayPath, err)
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": "writing the overlay failed"})
			return
		}
		sum := sha256.Sum256(body)
		h.logger.Infof("[ADMIN-OVERLAY] %s set the overlay (sha256 %x): %s", admin, sum, strings.Join(keys, ", "))
		ctx.JSON(http.StatusAccepted, gin.H{
			"sha256":  hex.EncodeToString(sum[:]),
			"keys":    keys,
			"restart": "the broker and the event service restart on the new overlay within about 10 seconds",
		})
	}
}

// writeFileAtomic replaces path with data so a reader sees the old file or the new one,
// never a partial write.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".overlay-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
