//go:build integration

package integration_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	brokerlog "github.com/0glabs/0g-serving-broker/common/log"
	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// Shared MySQL container DSN, initialized in TestMain.
var sharedDSN string

func TestMain(m *testing.M) {
	ctx := context.Background()

	mysqlContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mysql:8.0",
			ExposedPorts: []string{"3306/tcp"},
			Env: map[string]string{
				"MYSQL_ROOT_PASSWORD": "testpass",
				"MYSQL_DATABASE":      "test_db",
			},
			WaitingFor: wait.ForLog("port: 3306  MySQL Community Server").
				WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start mysql container: %v\n", err)
		os.Exit(1)
	}

	host, _ := mysqlContainer.Host(ctx)
	port, _ := mysqlContainer.MappedPort(ctx, "3306")
	sharedDSN = fmt.Sprintf("root:testpass@tcp(%s:%s)/test_db?parseTime=true", host, port.Port())

	code := m.Run()

	mysqlContainer.Terminate(ctx)
	os.Exit(code)
}

func newTestLogger() brokerlog.Logger {
	logger, err := brokerlog.GetLogger(&commonconfig.LoggerConfig{
		Format: "text",
		Level:  "debug",
	})
	if err != nil {
		panic("create logger: " + err.Error())
	}
	return logger
}

// ==========================================================================
// Session token helper
// ==========================================================================

func createAuthHeader(t *testing.T, privateKey *ecdsa.PrivateKey, providerAddr string) string {
	t.Helper()
	address := crypto.PubkeyToAddress(privateKey.PublicKey)

	token := ctrl.SessionToken{
		Address:    address.Hex(),
		Provider:   providerAddr,
		Timestamp:  time.Now().UnixMilli(),
		ExpiresAt:  time.Now().Add(time.Hour).UnixMilli(),
		Nonce:      fmt.Sprintf("test-nonce-%d", time.Now().UnixNano()),
		Generation: 0,
		TokenId:    255, // ephemeral
	}
	tokenJSON, err := json.Marshal(token)
	if err != nil {
		t.Fatalf("marshal session token: %v", err)
	}

	messageHash := crypto.Keccak256Hash(tokenJSON)
	prefixedMsg := crypto.Keccak256Hash(
		[]byte("\x19Ethereum Signed Message:\n32"),
		messageHash.Bytes(),
	)

	sig, err := crypto.Sign(prefixedMsg.Bytes(), privateKey)
	if err != nil {
		t.Fatalf("sign session token: %v", err)
	}
	sig[64] += 27 // Adjust V for Ethereum recovery

	payload := string(tokenJSON) + "|" + hexutil.Encode(sig)
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	return "Bearer app-sk-" + encoded
}

// ==========================================================================
// Test environment setup
// ==========================================================================

type testEnv struct {
	engine       *gin.Engine
	db           *db.DB
	ctrl         *ctrl.Ctrl
	proxy        *proxy.Proxy
	privateKey   *ecdsa.PrivateKey
	userAddr     string
	providerAddr string
}

func setupTestEnv(t *testing.T, opts ...func(*config.Config)) *testEnv {
	t.Helper()

	// Ethereum key pair
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	userAddr := crypto.PubkeyToAddress(privateKey.PublicKey)
	providerAddr := "0x1234567890abcdef1234567890abcdef12345678"

	// Config (defaults for video-generation; callers override via opts)
	cfg := &config.Config{
		AllowOrigins: []string{"*"},
		Database: struct {
			Provider string `yaml:"provider"`
		}{
			Provider: sharedDSN,
		},
		Service: config.Service{
			Type:            "video-generation",
			ModelType:       "sora-2",
			TargetSeparated: true,
		},
		Interval: struct {
			AutoSettleBufferTime     int `yaml:"autoSettleBufferTime"`
			ForceSettlementProcessor int `yaml:"forceSettlementProcessor"`
			SettlementProcessor      int `yaml:"settlementProcessor"`
			ReconciliationProcessor  int `yaml:"reconciliationProcessor"`
		}{
			AutoSettleBufferTime: 60,
		},
		ChatCacheExpiration: 20 * time.Minute,
	}

	// Apply optional config overrides
	for _, opt := range opts {
		opt(cfg)
	}

	// Database
	database, err := db.NewDB(cfg)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	if err := database.Migrate(); err != nil {
		t.Fatalf("migrate db: %v", err)
	}

	// Pre-create user in DB with sufficient balance
	lockBalance := "1000000000000000000000" // 1000 0G
	if err := database.CreateUserAccounts([]model.User{{
		User:                 userAddr.Hex(),
		LockBalance:          &lockBalance,
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
	}}); err != nil {
		if !strings.Contains(err.Error(), "Duplicate") {
			t.Fatalf("create test user: %v", err)
		}
	}

	// Create Ctrl
	logger := newTestLogger()
	provContract := &providercontract.ProviderContract{
		ProviderAddress: providerAddr,
	}
	svcCache := cache.New(5*time.Minute, 10*time.Minute)

	// Create TeeService with a real signing key for centralized providers
	// (decentralized tests with TargetSeparated=true skip signing, so nil is fine)
	var teeService *teeutil.TeeService
	if cfg.Service.IsCentralized() {
		teeKey, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("generate TEE key: %v", err)
		}
		teeService = &teeutil.TeeService{
			ProviderSigner: teeKey,
			Address:        crypto.PubkeyToAddress(teeKey.PublicKey),
		}
	}

	c := ctrl.New(database, provContract, cfg, svcCache, teeService, logger)

	// Pre-seed caches to avoid contract calls
	c.SeedContractAccountCache(userAddr.Hex(), &contract.Account{
		User:          userAddr,
		Balance:       big.NewInt(1e18), // 1 0G
		PendingRefund: big.NewInt(0),
		Generation:    big.NewInt(0),
		RevokedBitmap: big.NewInt(0),
		Acknowledged:  true,
	})

	c.SeedServiceCache(model.Service{
		Type:                  cfg.Service.Type,
		OutputPrice:           "100",
		InputPrice:            "0",
		TeeSignerAcknowledged: true,
	})

	// Gin engine + Proxy
	gin.SetMode(gin.TestMode)
	engine := gin.New()

	p := proxy.New(c, engine, []string{"*"}, false, config.ConcurrencyLimitConfig{}, logger)
	if err := p.Start(); err != nil {
		t.Fatalf("start proxy: %v", err)
	}

	return &testEnv{
		engine:       engine,
		db:           database,
		ctrl:         c,
		proxy:        p,
		privateKey:   privateKey,
		userAddr:     userAddr.Hex(),
		providerAddr: providerAddr,
	}
}

// filterRequestsByUser returns only the requests belonging to the given user address.
// This is necessary because all tests share the same MySQL database.
func filterRequestsByUser(requests []model.Request, userAddr string) []model.Request {
	var filtered []model.Request
	for _, r := range requests {
		if r.UserAddress == userAddr {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// closeNotifierRecorder wraps httptest.ResponseRecorder to implement http.CloseNotifier.
// gin.Context.Stream() calls CloseNotify(), which panics on a bare ResponseRecorder.
type closeNotifierRecorder struct {
	*httptest.ResponseRecorder
	closeCh chan bool
}

func newCloseNotifierRecorder() *closeNotifierRecorder {
	return &closeNotifierRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closeCh:          make(chan bool, 1),
	}
}

func (c *closeNotifierRecorder) CloseNotify() <-chan bool {
	return c.closeCh
}

// Ensure it satisfies the interface.
var _ http.CloseNotifier = (*closeNotifierRecorder)(nil)

// seedTestUser creates a DB user and seeds caches for an additional test user.
func seedTestUser(t *testing.T, env *testEnv, privateKey *ecdsa.PrivateKey) {
	t.Helper()
	userAddr := crypto.PubkeyToAddress(privateKey.PublicKey)

	lockBalance := "1000000000000000000000"
	if err := env.db.CreateUserAccounts([]model.User{{
		User:                 userAddr.Hex(),
		LockBalance:          &lockBalance,
		LastBalanceCheckTime: model.PtrOf(time.Now().UTC()),
	}}); err != nil {
		if !strings.Contains(err.Error(), "Duplicate") {
			t.Fatalf("create test user: %v", err)
		}
	}

	env.ctrl.SeedContractAccountCache(userAddr.Hex(), &contract.Account{
		User:          userAddr,
		Balance:       big.NewInt(1e18),
		PendingRefund: big.NewInt(0),
		Generation:    big.NewInt(0),
		RevokedBitmap: big.NewInt(0),
		Acknowledged:  true,
	})
}
