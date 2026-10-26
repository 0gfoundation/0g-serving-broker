//go:build integration

package event

import (
	"context"
	"crypto/ecdsa"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	_ "github.com/go-sql-driver/mysql"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	brokerlog "github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
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

// captureManager is the slice of manager.Manager addInstance uses: it collects
// the runnables instead of running them.
type captureManager struct {
	manager.Manager
	runnables []manager.Runnable
}

func (m *captureManager) Add(r manager.Runnable) error {
	m.runnables = append(m.runnables, r)
	return nil
}

var oneZG = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

func zg(tenths int64) *big.Int {
	return new(big.Int).Div(new(big.Int).Mul(big.NewInt(tenths), oneZG), big.NewInt(10))
}

// instanceUnderTest is one provider instance as the test seeds and checks it.
type instanceUnderTest struct {
	name     string // "" for the primary
	conf     *config.Config
	wallet   common.Address
	hashes   []string // its request rows, as the broker billed them
	fee      *big.Int // their total
	revenue  *big.Int // its native balance
	settleTx common.Hash
}

// The event process serving two providers (CONFIG_FILE and CONFIG_DIR/image.yaml)
// settles each one's requests from its own database, as its own wallet, for its
// own provider address — even for a user both providers bill. Its
// reconciliation confirms only its own settlement, though it reads the other
// instance's TEESettlementResult event for the same user from the shared
// contract, and its revenue transfer moves only its own wallet's balance.
//
// The request rows are written here, as the broker would have billed them; that
// billing is what cmd/server's TestInstancesBillIntoTheirOwnDatabase covers.
func TestInstancesSettleTheirOwnRequests(t *testing.T) {
	chain := instancetest.NewChain(t)
	_, user := newKey(t)
	revenueTarget := common.HexToAddress("0x00000000000000000000000000000000000000ff")
	reserve := zg(100) // 10 0G

	chatKey, chatWallet := newKey(t)
	imageKey, imageWallet := newKey(t)

	dir := t.TempDir()
	instancesDir := filepath.Join(dir, "instances")
	if err := os.Mkdir(instancesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	primaryPath := filepath.Join(dir, "config.yaml")
	writeFile(t, primaryPath, eventYAML(chain, chatKey, "https://chat.provider.test", "chatbot", revenueTarget, reserve))
	writeFile(t, filepath.Join(instancesDir, "image.yaml"), eventYAML(chain, imageKey, "https://image.provider.test", "text-to-image", revenueTarget, reserve))
	t.Setenv("CONFIG_FILE", primaryPath)
	t.Setenv(config.InstancesDirEnvVar, instancesDir)
	t.Setenv("DATABASE_DSN", sharedDSN)

	// Loaded the way Main loads them.
	conf := config.GetConfig()
	extras, err := config.LoadInstances(conf)
	if err != nil {
		t.Fatalf("LoadInstances: %v", err)
	}
	if len(extras) != 1 || extras[0].Name != "image" {
		t.Fatalf("extra instances = %+v, want one named image", extras)
	}

	chat := &instanceUnderTest{conf: conf, wallet: chatWallet, hashes: []string{"chat-1"}, fee: zg(10), revenue: zg(500)}
	image := &instanceUnderTest{name: "image", conf: extras[0].Config, wallet: imageWallet, hashes: []string{"image-1", "image-2"}, fee: zg(8), revenue: zg(700)}
	instances := []*instanceUnderTest{chat, image}

	// The broker's half: each wallet has registered its service, the user has an
	// account with each provider, and each instance's database holds the user
	// and the requests that instance billed.
	for _, inst := range instances {
		chain.SetService(contract.Service{
			Provider:    inst.wallet,
			ServiceType: inst.conf.Service.Type,
			Url:         inst.conf.Service.ServingURL,
			InputPrice:  big.NewInt(0),
			OutputPrice: zg(4),
			Model:       inst.conf.Service.ModelType,
		})
		chain.SetAccount(user, inst.wallet, zg(100))
		chain.SetBalance(inst.wallet, inst.revenue)

		if err := database.EnsureDatabase(inst.conf.Database.DSN); err != nil {
			t.Fatal(err)
		}
		d, err := database.NewDB(inst.conf)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Migrate(); err != nil {
			t.Fatal(err)
		}
		balance := zg(100).String()
		if err := d.CreateUserAccounts([]model.User{{User: user.Hex(), LockBalance: &balance, LastBalanceCheckTime: model.PtrOf(time.Now().UTC())}}); err != nil {
			t.Fatal(err)
		}
		each := new(big.Int).Div(inst.fee, big.NewInt(int64(len(inst.hashes))))
		for _, h := range inst.hashes {
			if err := d.CreateRequest(model.Request{
				UserAddress: user.Hex(),
				Nonce:       h,
				RequestHash: h,
				ServiceName: inst.conf.Service.Type,
				InputFee:    "0",
				OutputFee:   each.String(),
				Fee:         each.String(),
				InputCount:  10,
				OutputCount: 2,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	teeKey, teeAddr := newKey(t)
	teeService := &tee.TeeService{ProviderSigner: teeKey, Address: teeAddr}
	logger, err := brokerlog.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "info"})
	if err != nil {
		t.Fatal(err)
	}

	// The event process's half, built the way Main builds it.
	mgr := &captureManager{}
	addInstance(mgr, "", conf, teeService, logger)
	addInstance(mgr, extras[0].Name, extras[0].Config, teeService, logger)
	if len(mgr.runnables) != 6 {
		t.Fatalf("addInstance added %d runnables for two instances, want settlement, reconciliation and revenue transfer for each", len(mgr.runnables))
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, r := range mgr.runnables {
		wg.Add(1)
		go func(r manager.Runnable) {
			defer wg.Done()
			_ = r.Start(ctx)
		}(r)
	}
	// Registered after the fake chain's cleanup, so it runs first: nothing may
	// call the chain once it is gone.
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})

	// A settlement waits ten seconds before it first asks for its receipt, and
	// records its transaction hash only after that: the blocks are mined once both
	// are recorded, as the reconciliation processor needs (see waitFor).
	waitFor(t, "both settlements recorded with their transactions, and both revenue transfers", func() bool {
		if len(chain.Transfers()) != 2 {
			return false
		}
		for _, inst := range instances {
			ps := pendingSettlements(t, inst.conf, "pending")
			if len(ps) != 1 || ps[0].TxHash == "" {
				return false
			}
		}
		return true
	}, func() string {
		return fmt.Sprintf("%d settlements, %d transfers on chain; settlement records chat=%+v image=%+v",
			len(chain.Settlements()), len(chain.Transfers()), pendingSettlements(t, chat.conf, ""), pendingSettlements(t, image.conf, ""))
	})
	chain.Mine(20)
	waitFor(t, "both settlements confirmed by reconciliation", func() bool {
		for _, inst := range instances {
			if len(pendingSettlements(t, inst.conf, "confirmed")) != 1 {
				return false
			}
		}
		return true
	}, func() string {
		return fmt.Sprintf("settlement records chat=%+v image=%+v", pendingSettlements(t, chat.conf, ""), pendingSettlements(t, image.conf, ""))
	})

	// Each wallet sent one settlement, for its own provider, of its own fees.
	settlements := chain.Settlements()
	if len(settlements) != 2 {
		t.Fatalf("%d settlement transactions, want one per instance: %+v", len(settlements), settlements)
	}
	for _, inst := range instances {
		var found bool
		for _, s := range settlements {
			if s.Sender != inst.wallet {
				continue
			}
			found = true
			inst.settleTx = s.TxHash
			if len(s.Items) != 1 {
				t.Fatalf("instance %q settled %d users, want 1: %+v", inst.name, len(s.Items), s.Items)
			}
			item := s.Items[0]
			if item.User != user || item.Provider != inst.wallet || item.TotalFee.Cmp(inst.fee) != 0 {
				t.Errorf("instance %q settled user %s provider %s fee %s, want user %s provider %s fee %s",
					inst.name, item.User.Hex(), item.Provider.Hex(), item.TotalFee, user.Hex(), inst.wallet.Hex(), inst.fee)
			}
		}
		if !found {
			t.Errorf("instance %q (wallet %s) sent no settlement", inst.name, inst.wallet.Hex())
		}
		acc, _ := chain.Account(user, inst.wallet)
		if want := new(big.Int).Sub(zg(100), inst.fee); acc.Balance.Cmp(want) != 0 {
			t.Errorf("user's balance with instance %q = %s, want %s", inst.name, acc.Balance, want)
		}
	}
	if chat.settleTx == image.settleTx {
		t.Fatal("both instances report the same settlement transaction")
	}

	// Each database's settlement record is its own transaction over its own rows,
	// confirmed by its own reconciliation; the rows are gone and nothing is
	// unsettled.
	for _, inst := range instances {
		ps := pendingSettlements(t, inst.conf, "")
		if len(ps) != 1 {
			t.Fatalf("instance %q: %d pending settlement records, want 1: %+v", inst.name, len(ps), ps)
		}
		if ps[0].TxHash != inst.settleTx.Hex() || ps[0].Status != "confirmed" || ps[0].TotalFee != inst.fee.String() {
			t.Errorf("instance %q: settlement record %+v, want tx %s confirmed for %s", inst.name, ps[0], inst.settleTx.Hex(), inst.fee)
		}
		var hashes []string
		if err := json.Unmarshal([]byte(ps[0].RequestHashes), &hashes); err != nil {
			t.Fatal(err)
		}
		sort.Strings(hashes)
		if fmt.Sprint(hashes) != fmt.Sprint(inst.hashes) {
			t.Errorf("instance %q settled requests %v, want its own %v", inst.name, hashes, inst.hashes)
		}
		d, err := database.NewDB(inst.conf)
		if err != nil {
			t.Fatal(err)
		}
		if fee, err := d.CalculateUnsettledFee(user.Hex()); err != nil || fee.Sign() != 0 {
			t.Errorf("instance %q: unsettled fee after settlement = %v (%v), want 0", inst.name, fee, err)
		}
	}

	// Each wallet moved its own revenue above the reserve, and only that.
	for _, inst := range instances {
		var found bool
		for _, tr := range chain.Transfers() {
			if tr.From != inst.wallet {
				continue
			}
			found = true
			if want := new(big.Int).Sub(inst.revenue, reserve); tr.To != revenueTarget || tr.Value.Cmp(want) != 0 {
				t.Errorf("instance %q transferred %s to %s, want %s to %s", inst.name, tr.Value, tr.To.Hex(), want, revenueTarget.Hex())
			}
		}
		if !found {
			t.Errorf("instance %q (wallet %s) made no revenue transfer", inst.name, inst.wallet.Hex())
		}
	}
}

// waitFor polls cond until it holds, failing with state() after 90 seconds.
//
// The test mines the blocks reconciliation needs only after every settlement
// record has its transaction hash, because the broker writes that hash only once
// WaitForReceipt returns, at least ten seconds after sending. A chain that
// confirms the event sooner lets reconciliation scan it while the record has no
// hash yet, and its cursor then moves past the event for good — so on a fast
// chain the receipt flow settles the requests and reconciliation never confirms
// the record, which stays pending until it expires.
func waitFor(t *testing.T, what string, cond func() bool, state func() string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %s", what, state())
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// pendingSettlements are the instance's settlement records with the given
// status, or all of them for "".
func pendingSettlements(t *testing.T, conf *config.Config, status string) []model.PendingSettlement {
	t.Helper()
	conn, err := sql.Open("mysql", conf.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	q := "SELECT COALESCE(tx_hash, ''), status, total_fee, request_hashes FROM pending_settlement"
	var args []interface{}
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, status)
	}
	rows, err := conn.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []model.PendingSettlement
	for rows.Next() {
		var ps model.PendingSettlement
		if err := rows.Scan(&ps.TxHash, &ps.Status, &ps.TotalFee, &ps.RequestHashes); err != nil {
			t.Fatal(err)
		}
		out = append(out, ps)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// eventYAML is one provider's config with its event-process workers ticking
// fast enough for a test. The force settlement keeps the shortest interval the
// event process accepts; the settlement check triggers first, since the
// contract's prices make every user with unsettled fees "at risk".
func eventYAML(chain *instancetest.Chain, wallet *ecdsa.PrivateKey, servingURL, serviceType string, revenueTarget common.Address, reserve *big.Int) string {
	return fmt.Sprintf(`contractAddress: %q
service:
  servingUrl: %q
  targetUrl: "http://upstream.invalid"
  targetSeparated: true
  type: %q
  model: "m"
  inputPrice: "0"
  outputPrice: "1"
network:
  url: %q
  chainID: %d
  privateKeys: [%q]
interval:
  autoSettleBufferTime: 60s
  settlementProcessor: 300ms
  forceSettlementProcessor: 1m
  reconciliationProcessor: 300ms
revenueTransfer:
  targetAddress: %q
  reserveAmount: %q
  interval: 300ms
`, instancetest.ContractAddress, servingURL, serviceType, chain.URL, instancetest.ChainID,
		hexutil.Encode(crypto.FromECDSA(wallet))[2:], revenueTarget.Hex(), reserve.String())
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
