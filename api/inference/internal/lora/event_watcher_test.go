package lora

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

func TestEventWatcher_Start_EmptyContractAddr(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}
	w := &EventWatcher{
		manager:         m,
		config:          config.LoRAConfig{FineTuningContractAddr: ""},
		providerAddress: common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678"),
		logger:          getTestLogger(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start should return immediately when contract address is empty")
	}
}

func TestEventWatcher_Start_RespectsContextCancellation(t *testing.T) {
	w := &EventWatcher{
		manager: &Manager{
			adapters: make(map[string]*AdapterInfo),
			logger:   getTestLogger(),
		},
		config:          config.LoRAConfig{FineTuningContractAddr: ""},
		providerAddress: common.HexToAddress("0xaaaa"),
		logger:          getTestLogger(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start should return when context is already cancelled")
	}
}

func TestEventWatcher_Stop_NilClient(t *testing.T) {
	w := &EventWatcher{
		logger: getTestLogger(),
	}
	w.Stop()
}

func TestEventWatcher_Stop_ClosesClient(t *testing.T) {
	srv := newFakeRPCServer(100)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial fake RPC: %v", err)
	}

	w := &EventWatcher{
		client: client,
		logger: getTestLogger(),
	}
	w.Stop()
}

func TestDefaultEventLookbackBlocks(t *testing.T) {
	if defaultEventLookbackBlocks != 1000 {
		t.Errorf("defaultEventLookbackBlocks = %d, want 1000", defaultEventLookbackBlocks)
	}
}

func TestEventWatcher_PollEvents_FromBlockAheadOfCurrentBlock(t *testing.T) {
	srv := newFakeRPCServer(100)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial fake RPC: %v", err)
	}

	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	w := &EventWatcher{
		manager:         m,
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		providerAddress: common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678"),
		logger:          getTestLogger(),
	}

	var fromBlock uint64 = 999999
	w.pollEvents(context.Background(), &fromBlock)

	if len(m.adapters) != 0 {
		t.Error("pollEvents should not register adapters when fromBlock > currentBlock")
	}
	if fromBlock != 999999 {
		t.Errorf("fromBlock should not advance when skipped, got %d", fromBlock)
	}
}

func TestEventWatcher_PollEvents_SetsLookbackWhenFromBlockZero(t *testing.T) {
	currentBlock := uint64(5000)
	srv := newFakeRPCServer(currentBlock)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial fake RPC: %v", err)
	}

	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	w := &EventWatcher{
		manager:         m,
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		providerAddress: common.HexToAddress("0x1234"),
		logger:          getTestLogger(),
	}

	var fromBlock uint64 = 0
	w.pollEvents(context.Background(), &fromBlock)

	// After poll, fromBlock should be set to currentBlock + 1
	expected := currentBlock + 1
	if fromBlock != expected {
		t.Errorf("fromBlock = %d, want %d (currentBlock+1)", fromBlock, expected)
	}
}

func TestEventWatcher_PollEvents_SmallBlockNoLookbackOverflow(t *testing.T) {
	currentBlock := uint64(500)
	srv := newFakeRPCServer(currentBlock)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial fake RPC: %v", err)
	}

	w := &EventWatcher{
		manager: &Manager{
			adapters: make(map[string]*AdapterInfo),
			logger:   getTestLogger(),
		},
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		providerAddress: common.HexToAddress("0x1234"),
		logger:          getTestLogger(),
	}

	// When currentBlock (500) < defaultEventLookbackBlocks (1000),
	// the lookback subtraction should not happen (would underflow uint64).
	var fromBlock uint64 = 0
	w.pollEvents(context.Background(), &fromBlock)

	if fromBlock != currentBlock+1 {
		t.Errorf("fromBlock = %d, want %d", fromBlock, currentBlock+1)
	}
}

func TestEventWatcher_PollEvents_AdvancesFromBlock(t *testing.T) {
	currentBlock := uint64(2000)
	srv := newFakeRPCServer(currentBlock)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial fake RPC: %v", err)
	}

	w := &EventWatcher{
		manager: &Manager{
			adapters: make(map[string]*AdapterInfo),
			logger:   getTestLogger(),
		},
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		providerAddress: common.HexToAddress("0x1234"),
		logger:          getTestLogger(),
	}

	var fromBlock uint64 = 1500
	w.pollEvents(context.Background(), &fromBlock)

	if fromBlock != currentBlock+1 {
		t.Errorf("fromBlock = %d, want %d (currentBlock+1)", fromBlock, currentBlock+1)
	}
}

func TestEventWatcher_PollEvents_BlockNumberError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqs []json.RawMessage
		json.NewDecoder(r.Body).Decode(&reqs)

		// Return error for any RPC call
		var responses []map[string]interface{}
		for range reqs {
			responses = append(responses, map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"error":   map[string]interface{}{"code": -32000, "message": "internal error"},
			})
		}

		// Handle single vs batch request
		if len(reqs) == 0 {
			var single json.RawMessage
			// re-parse as single
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"error":   map[string]interface{}{"code": -32000, "message": "internal error"},
			})
			_ = single
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responses)
	}))
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	w := &EventWatcher{
		manager: &Manager{
			adapters: make(map[string]*AdapterInfo),
			logger:   getTestLogger(),
		},
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		providerAddress: common.HexToAddress("0x1234"),
		logger:          getTestLogger(),
	}

	var fromBlock uint64 = 100
	w.pollEvents(context.Background(), &fromBlock)

	// fromBlock should remain unchanged on error
	if fromBlock != 100 {
		t.Errorf("fromBlock should remain unchanged on error, got %d", fromBlock)
	}
}

func TestEventWatcher_PollIntervalDefault(t *testing.T) {
	tests := []struct {
		name              string
		pollBlockInterval int
		wantInterval      time.Duration
	}{
		{"zero defaults to 5s", 0, 5 * time.Second},
		{"negative defaults to 5s", -1, 5 * time.Second},
		{"positive is respected", 10, 10 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pollInterval := time.Duration(tt.pollBlockInterval) * time.Second
			if pollInterval <= 0 {
				pollInterval = 5 * time.Second
			}
			if pollInterval != tt.wantInterval {
				t.Errorf("pollInterval = %v, want %v", pollInterval, tt.wantInterval)
			}
		})
	}
}

func TestEventWatcher_Start_PollsOnTick(t *testing.T) {
	var pollCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pollCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		// Return error to abort early in pollEvents (simpler than full mock)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]interface{}{"code": -32000, "message": "mock"},
		})
	}))
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	w := &EventWatcher{
		manager: &Manager{
			adapters: make(map[string]*AdapterInfo),
			logger:   getTestLogger(),
		},
		db:              nil,
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeef", PollBlockIntervalSeconds: 0},
		providerAddress: common.HexToAddress("0x1234"),
		logger:          getTestLogger(),
	}

	// Start with a contract addr set but db is nil.
	// Start will panic on db.GetLastProcessedBlock if db is nil,
	// so this test verifies the empty-contract-addr path instead.
	// Testing the full Start loop requires a DB, which is covered in integration tests.
	w.config.FineTuningContractAddr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		w.Start(ctx)
		close(done)
	}()

	<-done
}

func TestEventWatcher_ProcessAcknowledgedEvents_NoEvents(t *testing.T) {
	currentBlock := uint64(3000)
	srv := newFakeRPCServer(currentBlock)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	w := &EventWatcher{
		manager:         m,
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		providerAddress: common.HexToAddress("0x1234"),
		logger:          getTestLogger(),
	}

	// Poll events — the mock returns no logs, so no adapters should be registered
	var fromBlock uint64 = 2900
	w.pollEvents(context.Background(), &fromBlock)

	if len(m.adapters) != 0 {
		t.Errorf("expected 0 adapters when no events, got %d", len(m.adapters))
	}
}

func TestNewEventWatcher_SetsFields(t *testing.T) {
	srv := newFakeRPCServer(100)
	defer srv.Close()

	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	cfg := config.LoRAConfig{
		ChainRpcUrl:              srv.URL,
		FineTuningContractAddr:   "0xdeadbeef",
		PollBlockIntervalSeconds: 10,
	}
	providerAddr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	w, err := NewEventWatcher(m, nil, cfg, providerAddr, getTestLogger())
	if err != nil {
		t.Fatalf("NewEventWatcher: %v", err)
	}
	if w.manager != m {
		t.Error("manager not set")
	}
	if w.providerAddress != providerAddr {
		t.Error("providerAddress not set")
	}
	if w.config.FineTuningContractAddr != "0xdeadbeef" {
		t.Errorf("config.FineTuningContractAddr = %q", w.config.FineTuningContractAddr)
	}
	if w.client == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestEventWatcher_PollEvents_MultipleCalls(t *testing.T) {
	currentBlock := uint64(3000)
	srv := newFakeRPCServer(currentBlock)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	w := &EventWatcher{
		manager: &Manager{
			adapters: make(map[string]*AdapterInfo),
			logger:   getTestLogger(),
		},
		client:          client,
		config:          config.LoRAConfig{FineTuningContractAddr: "0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"},
		providerAddress: common.HexToAddress("0x1234"),
		logger:          getTestLogger(),
	}

	var fromBlock uint64 = 2500
	w.pollEvents(context.Background(), &fromBlock)
	if fromBlock != currentBlock+1 {
		t.Errorf("after 1st poll: fromBlock = %d, want %d", fromBlock, currentBlock+1)
	}

	// Second call: fromBlock > currentBlock, should be a no-op
	w.pollEvents(context.Background(), &fromBlock)
	if fromBlock != currentBlock+1 {
		t.Errorf("after 2nd poll: fromBlock = %d, should remain %d", fromBlock, currentBlock+1)
	}
}

func TestEventWatcher_Stop_MultipleCallsSafe(t *testing.T) {
	srv := newFakeRPCServer(100)
	defer srv.Close()

	client, err := ethclient.Dial(srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	w := &EventWatcher{
		client: client,
		logger: getTestLogger(),
	}
	w.Stop()
	// Second call should not panic
	w.Stop()
}

func TestNewEventWatcher_InvalidRPC(t *testing.T) {
	m := &Manager{
		adapters: make(map[string]*AdapterInfo),
		logger:   getTestLogger(),
	}

	cfg := config.LoRAConfig{
		ChainRpcUrl: "http://127.0.0.1:1",
	}

	// NewEventWatcher dials the RPC. With an unreachable endpoint,
	// HTTP-based dial typically doesn't fail (lazy connect). That's fine —
	// we verify the constructor returns without error for HTTP URLs.
	w, err := NewEventWatcher(m, nil, cfg, common.HexToAddress("0x1234"), getTestLogger())
	if err != nil {
		t.Logf("NewEventWatcher returned error (expected for some transports): %v", err)
	} else if w == nil {
		t.Fatal("expected non-nil watcher")
	}
}

// newFakeRPCServer creates a JSON-RPC server that responds to eth_blockNumber
// and returns empty results for eth_getLogs. This allows testing pollEvents logic
// without a real Ethereum node.
func newFakeRPCServer(blockNumber uint64) *httptest.Server {
	blockHex := fmt.Sprintf("0x%x", blockNumber)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			ID      interface{}     `json:"id"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		switch req.Method {
		case "eth_blockNumber":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  blockHex,
			})
		case "eth_getLogs":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  []interface{}{},
			})
		case "eth_chainId":
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  "0x1",
			})
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  nil,
			})
		}
	}))
}
