//go:build integration

package server

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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/go-sql-driver/mysql"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	brokerlog "github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	cfg "github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/instancetest"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// sharedDSN is the root DSN of the MySQL server TestMain starts, on its
// instancetest.PrimaryDatabase.
var sharedDSN string

func TestMain(m *testing.M) {
	dsn, stop, err := instancetest.StartMySQL(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sharedDSN = dsn
	code := m.Run()
	stop()
	os.Exit(code)
}

var (
	// oneZG is 1 0G in neuron.
	oneZG = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	// chatOutputPrice and imagePrice are per completion token and per image.
	chatOutputPrice = tenthsOfZG(5)
	imagePrice      = tenthsOfZG(4)
)

func tenthsOfZG(n int64) *big.Int {
	return new(big.Int).Div(new(big.Int).Mul(big.NewInt(n), oneZG), big.NewInt(10))
}

// One broker process serving a chatbot provider (CONFIG_FILE) and a
// text-to-image provider (CONFIG_DIR/image.yaml), both billing one user who is
// not whitelisted. Each request is routed by Host to its own instance, billed
// into that instance's database, and admitted against that instance's balance
// and unsettled fees alone.
//
// The balances are set so that admission itself proves the isolation: the user's
// account with each provider covers that provider's own fees plus the minimum
// locked balance, and not the other provider's fees. A request that counted the
// other instance's unsettled fee would be refused for insufficient balance.
func TestInstancesBillIntoTheirOwnDatabase(t *testing.T) {
	chain := instancetest.NewChain(t)
	chatUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("chat upstream: unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id":      "chatcmpl-1",
			"object":  "chat.completion",
			"model":   "gpt-4o",
			"choices": []map[string]interface{}{{"message": map[string]string{"role": "assistant", "content": "hi"}, "finish_reason": "stop"}},
			"usage":   map[string]interface{}{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
		})
	}))
	t.Cleanup(chatUpstream.Close)
	imageUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			t.Errorf("image upstream: unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"created": 1234567890,
			"data":    []map[string]interface{}{{"url": "https://example.com/1.png"}},
		})
	}))
	t.Cleanup(imageUpstream.Close)

	chatWallet, chatAddr := newKey(t)
	imageWallet, imageAddr := newKey(t)
	userKey, user := newKey(t)

	// Fees: one chat request is 2 completion tokens (input is free), one image
	// request is one image.
	chatFee := new(big.Int).Mul(big.NewInt(2), chatOutputPrice)
	imageFee := new(big.Int).Set(imagePrice)
	minLocked, _ := new(big.Int).SetString(constant.MinimumLockedBalance, 10)
	// The chat request is sent after one image request, and must not count it:
	// half an image short of covering it.
	chatBalance := new(big.Int).Add(minLocked, new(big.Int).Div(imageFee, big.NewInt(2)))
	// The second image request is sent after the chat request, and must count the
	// first image but not the chat: half a chat short of covering it.
	imageBalance := new(big.Int).Add(new(big.Int).Add(minLocked, imageFee), new(big.Int).Div(chatFee, big.NewInt(2)))
	chain.SetAccount(user, chatAddr, chatBalance)
	chain.SetAccount(user, imageAddr, imageBalance)

	dir := t.TempDir()
	instancesDir := filepath.Join(dir, "instances")
	if err := os.Mkdir(instancesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	primaryPath := filepath.Join(dir, "config.yaml")
	writeFile(t, primaryPath, providerYAML(chain, chatWallet, "https://chat.provider.test", chatUpstream.URL, "chatbot", "gpt-4o", chatOutputPrice))
	writeFile(t, filepath.Join(instancesDir, "image.yaml"), providerYAML(chain, imageWallet, "https://image.provider.test", imageUpstream.URL, "text-to-image", "dall-e-3", imagePrice))
	t.Setenv("CONFIG_FILE", primaryPath)
	t.Setenv(cfg.InstancesDirEnvVar, instancesDir)
	t.Setenv("DATABASE_DSN", sharedDSN)

	// Loaded the way Main loads them.
	config := cfg.GetConfig()
	extras, err := cfg.LoadInstances(config)
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(extras) != 1 || extras[0].Name != "image" {
		t.Fatalf("extra instances = %+v, want one named image", extras)
	}
	image := extras[0].Config
	if got := databaseName(t, config.Database.DSN); got != instancetest.PrimaryDatabase {
		t.Errorf("primary database = %q, want %q", got, instancetest.PrimaryDatabase)
	}
	if got, want := databaseName(t, image.Database.DSN), instancetest.PrimaryDatabase+"_image"; got != want {
		t.Errorf("extra instance database = %q, want %q", got, want)
	}
	if _, err := database.NewDB(image); err == nil {
		t.Fatal("the extra instance's database exists before the broker starts; the test cannot show the broker creates it")
	}

	teeKey, teeAddr := newKey(t)
	teeService := &tee.TeeService{ProviderSigner: teeKey, Address: teeAddr}
	logger, err := brokerlog.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "info"})
	if err != nil {
		t.Fatal(err)
	}

	instances, router := startInstances(context.Background(), config, extras, teeService, logger)
	t.Cleanup(func() {
		for _, inst := range instances {
			inst.shutdown()
		}
	})

	// Each wallet registered its own service.
	for addr, want := range map[common.Address]string{chatAddr: "chatbot", imageAddr: "text-to-image"} {
		svc, ok := chain.Service(addr)
		if !ok || svc.ServiceType != want {
			t.Fatalf("service registered by %s = %+v (registered %v), want type %s", addr.Hex(), svc, ok, want)
		}
	}

	chatDB, err := database.NewDB(config)
	if err != nil {
		t.Fatal(err)
	}
	imageDB, err := database.NewDB(image)
	if err != nil {
		t.Fatalf("the broker did not create the extra instance's database: %v", err)
	}

	send := func(host, path, body string, provider common.Address) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", authHeader(t, userKey, provider))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	const (
		chatHost   = "chat.provider.test"
		imageHost  = "image.provider.test"
		chatPath   = "/v1/proxy/chat/completions"
		imagePath  = "/v1/proxy/images/generations"
		chatBody   = `{"model":"gpt-4o","messages":[{"role":"user","content":"Hi"}],"stream":false}`
		imageBody  = `{"model":"dall-e-3","prompt":"a cat","n":1,"size":"1024x1024"}`
		imageFirst = "first image request"
	)

	if w := send(imageHost, imagePath, imageBody, imageAddr); w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", imageFirst, w.Code, w.Body.String())
	}
	if w := send(chatHost, chatPath, chatBody, chatAddr); w.Code != http.StatusOK {
		t.Fatalf("chat request after an image request was refused — did the chat instance count the image fee? %d %s", w.Code, w.Body.String())
	}
	if w := send(imageHost, imagePath, imageBody, imageAddr); w.Code != http.StatusOK {
		t.Fatalf("second image request after a chat request was refused — did the image instance count the chat fee? %d %s", w.Code, w.Body.String())
	}
	// A session for the chat provider presented at the image host reaches the
	// image instance, which refuses it rather than billing it.
	if w := send(imageHost, chatPath, chatBody, chatAddr); w.Code == http.StatusOK {
		t.Fatalf("a chat-provider session was served by the image instance: %s", w.Body.String())
	}

	chatRows := unprocessedRequests(t, chatDB, user)
	imageRows := unprocessedRequests(t, imageDB, user)
	assertRows(t, "chat instance", chatRows, constant.ServiceTypeChatbot, chatFee, 1)
	assertRows(t, "image instance", imageRows, constant.ServiceTypeTextToImage, imageFee, 2)
	for _, c := range chatRows {
		for _, i := range imageRows {
			if c.RequestHash == i.RequestHash {
				t.Errorf("request %s is in both databases", c.RequestHash)
			}
		}
	}

	// Each instance reports its own unsettled fee, through its own host.
	for _, tc := range []struct {
		host     string
		provider common.Address
		want     *big.Int
	}{
		{chatHost, chatAddr, chatFee},
		{imageHost, imageAddr, new(big.Int).Mul(big.NewInt(2), imageFee)},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/user/"+user.Hex()+"/unsettledfee", nil)
		req.Host = tc.host
		req.Header.Set("Authorization", authHeader(t, userKey, tc.provider))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("unsettled fee at %s: %d %s", tc.host, w.Code, w.Body.String())
		}
		var resp struct {
			UnsettledFee string `json:"unsettledFee"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.UnsettledFee != tc.want.String() {
			t.Errorf("unsettled fee at %s = %s, want %s", tc.host, resp.UnsettledFee, tc.want)
		}
	}
}

func assertRows(t *testing.T, who string, rows []model.Request, service string, fee *big.Int, n int) {
	t.Helper()
	if len(rows) != n {
		t.Fatalf("%s: %d billed requests, want %d: %+v", who, len(rows), n, rows)
	}
	for _, r := range rows {
		if r.ServiceName != service || r.Fee != fee.String() || r.IsWhitelisted {
			t.Errorf("%s: request %s is service %q fee %s (whitelisted %v), want service %q fee %s",
				who, r.RequestHash, r.ServiceName, r.Fee, r.IsWhitelisted, service, fee)
		}
	}
}

func unprocessedRequests(t *testing.T, d *database.DB, user common.Address) []model.Request {
	t.Helper()
	rows, _, err := d.ListRequest(model.RequestListOptions{Processed: false})
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Request
	for _, r := range rows {
		if strings.EqualFold(r.UserAddress, user.Hex()) {
			out = append(out, r)
		}
	}
	return out
}

// providerYAML is one provider's config, as an operator would write it.
func providerYAML(chain *instancetest.Chain, wallet *ecdsa.PrivateKey, servingURL, targetURL, serviceType, modelName string, outputPrice *big.Int) string {
	return fmt.Sprintf(`contractAddress: %q
service:
  servingUrl: %q
  targetUrl: %q
  targetSeparated: true
  type: %q
  model: %q
  inputPrice: "0"
  outputPrice: %q
network:
  url: %q
  chainID: %d
  privateKeys: [%q]
`, instancetest.ContractAddress, servingURL, targetURL, serviceType, modelName, outputPrice.String(),
		chain.URL, instancetest.ChainID, hexutil.Encode(crypto.FromECDSA(wallet))[2:])
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newKey(t *testing.T) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey)
}

func databaseName(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.DBName
}

// authHeader is a user's ephemeral session token for provider, as the SDK
// builds it.
func authHeader(t *testing.T, key *ecdsa.PrivateKey, provider common.Address) string {
	t.Helper()
	now := time.Now()
	tokenJSON, err := json.Marshal(ctrl.SessionToken{
		Address:   crypto.PubkeyToAddress(key.PublicKey).Hex(),
		Provider:  provider.Hex(),
		Timestamp: now.UnixMilli(),
		ExpiresAt: now.Add(time.Hour).UnixMilli(),
		Nonce:     fmt.Sprintf("nonce-%d", now.UnixNano()),
		TokenId:   255,
	})
	if err != nil {
		t.Fatal(err)
	}
	hash := crypto.Keccak256Hash([]byte("\x19Ethereum Signed Message:\n32"), crypto.Keccak256(tokenJSON))
	sig, err := crypto.Sign(hash.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	sig[64] += 27
	payload := string(tokenJSON) + "|" + hexutil.Encode(sig)
	return "Bearer app-sk-" + base64.StdEncoding.EncodeToString([]byte(payload))
}
