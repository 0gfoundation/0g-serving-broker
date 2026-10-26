//go:build integration

// Package instancetest is what the multi-instance integration tests of the
// broker (cmd/server) and the event process (cmd/event) share: a fake of the
// serving contract reached over JSON-RPC, and a MySQL server.
//
// The fake stands in for the chain because nothing in the settlement path
// takes an interface: ctrl calls the abigen binding and ethclient directly. A
// JSON-RPC server is the one seam both processes already go through — every
// instance dials its own network.url — so the code under test runs unchanged,
// down to signing and sending its transactions. The fake decodes each signed
// transaction it receives, which is what lets a test say which wallet sent it.
package instancetest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/0glabs/0g-serving-broker/inference/contract"
)

// ChainID is the fake chain's id.
const ChainID = 16602

// ContractAddress is where the fake serving contract lives.
const ContractAddress = "0x00000000000000000000000000000000000000a1"

// LockTime is the refund lock time the fake contract reports. The event
// process refuses a lock time under AutoSettleBufferTime + 1 minute.
const LockTime = 10 * time.Minute

// Settlement is one settleFeesWithTEE transaction the fake received.
type Settlement struct {
	TxHash common.Hash
	// Sender is the wallet that signed the transaction: the provider the
	// contract would settle for, whatever the items claim.
	Sender common.Address
	Items  []contract.TEESettlementData
}

// Transfer is one native-token transfer the fake received.
type Transfer struct {
	From, To common.Address
	Value    *big.Int
}

type accountKey struct{ user, provider common.Address }

// Chain is a fake of the serving contract and the chain around it, served over
// JSON-RPC at URL. It keeps per-provider services and per-(user, provider)
// accounts the way the contract does, so instances on different wallets see
// different state through the same URL.
type Chain struct {
	URL string

	t        testing.TB
	abi      *abi.ABI
	contract common.Address

	mu          sync.Mutex
	head        uint64
	services    map[common.Address]contract.Service
	accounts    map[accountKey]contract.Account
	balances    map[common.Address]*big.Int
	nonces      map[common.Address]uint64
	receipts    map[common.Hash]*types.Receipt
	logs        []types.Log
	settlements []Settlement
	transfers   []Transfer
}

// NewChain starts a fake chain that lives as long as t.
func NewChain(t testing.TB) *Chain {
	t.Helper()
	parsed, err := contract.InferenceServingMetaData.GetAbi()
	if err != nil {
		t.Fatalf("parse serving contract ABI: %v", err)
	}
	c := &Chain{
		t:        t,
		abi:      parsed,
		contract: common.HexToAddress(ContractAddress),
		head:     100,
		services: map[common.Address]contract.Service{},
		accounts: map[accountKey]contract.Account{},
		balances: map[common.Address]*big.Int{},
		nonces:   map[common.Address]uint64{},
		receipts: map[common.Hash]*types.Receipt{},
	}
	srv := httptest.NewServer(http.HandlerFunc(c.serveHTTP))
	t.Cleanup(srv.Close)
	c.URL = srv.URL
	return c
}

// SetAccount gives user an acknowledged account with provider holding balance.
func (c *Chain) SetAccount(user, provider common.Address, balance *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accounts[accountKey{user, provider}] = contract.Account{
		User:               user,
		Provider:           provider,
		Nonce:              big.NewInt(0),
		Balance:            new(big.Int).Set(balance),
		PendingRefund:      big.NewInt(0),
		Refunds:            []contract.Refund{},
		Acknowledged:       true,
		ValidRefundsLength: big.NewInt(0),
		Generation:         big.NewInt(0),
		RevokedBitmap:      big.NewInt(0),
	}
}

// Account is user's account with provider, and whether it exists.
func (c *Chain) Account(user, provider common.Address) (contract.Account, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	acc, ok := c.accounts[accountKey{user, provider}]
	return acc, ok
}

// SetService registers svc for svc.Provider, as addOrUpdateService would, with
// its TEE signer already acknowledged.
func (c *Chain) SetService(svc contract.Service) {
	c.mu.Lock()
	defer c.mu.Unlock()
	svc.TeeSignerAcknowledged = true
	for _, n := range []**big.Int{&svc.InputPrice, &svc.OutputPrice, &svc.UpdatedAt} {
		if *n == nil {
			*n = big.NewInt(0)
		}
	}
	c.services[svc.Provider] = svc
}

// Service is the service provider registered, and whether there is one.
func (c *Chain) Service(provider common.Address) (contract.Service, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	svc, ok := c.services[provider]
	return svc, ok
}

// SetBalance sets addr's native balance.
func (c *Chain) SetBalance(addr common.Address, balance *big.Int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.balances[addr] = new(big.Int).Set(balance)
}

// Mine moves the head n blocks on. A transaction is mined into the next block
// and nothing else moves the head, so a test decides when the reconciliation
// processor, which waits 12 confirmations, may see an event.
func (c *Chain) Mine(n uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.head += n
}

// Settlements are the settleFeesWithTEE transactions received so far, in order.
func (c *Chain) Settlements() []Settlement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Settlement(nil), c.settlements...)
}

// Transfers are the native transfers received so far, in order.
func (c *Chain) Transfers() []Transfer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Transfer(nil), c.transfers...)
}

type rpcRequest struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (c *Chain) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		var reqs []rpcRequest
		if err := json.Unmarshal(trimmed, &reqs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resps := make([]rpcResponse, len(reqs))
		for i, req := range reqs {
			resps[i] = c.handle(req)
		}
		_ = json.NewEncoder(w).Encode(resps)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(c.handle(req))
}

// errRevert is a contract revert, which the broker classifies by its message.
type errRevert string

func (e errRevert) Error() string { return "execution reverted: " + string(e) }

func (c *Chain) handle(req rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	result, err := c.locked(req)
	if err != nil {
		code := -32000
		if _, ok := err.(errRevert); ok {
			code = 3
		}
		resp.Error = &rpcError{Code: code, Message: err.Error()}
		return resp
	}
	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = &rpcError{Code: -32603, Message: err.Error()}
		return resp
	}
	resp.Result = raw
	return resp
}

func (c *Chain) locked(req rpcRequest) (interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dispatch(req.Method, req.Params)
}

// dispatch answers one call, with c.mu held.
func (c *Chain) dispatch(method string, params []json.RawMessage) (interface{}, error) {
	switch method {
	case "eth_chainId":
		return (*hexutil.Big)(big.NewInt(ChainID)), nil
	case "eth_blockNumber":
		return hexutil.Uint64(c.head), nil
	case "eth_gasPrice":
		return (*hexutil.Big)(big.NewInt(1_000_000_000)), nil
	case "eth_estimateGas":
		return hexutil.Uint64(200_000), nil
	case "eth_getCode":
		return hexutil.Bytes{0x60, 0x80}, nil
	case "eth_getTransactionCount":
		var addr common.Address
		if err := param(params, 0, &addr); err != nil {
			return nil, err
		}
		return hexutil.Uint64(c.nonces[addr]), nil
	case "eth_getBalance":
		var addr common.Address
		if err := param(params, 0, &addr); err != nil {
			return nil, err
		}
		bal := c.balances[addr]
		if bal == nil {
			bal = big.NewInt(0)
		}
		return (*hexutil.Big)(bal), nil
	case "eth_call":
		return c.call(params)
	case "eth_sendRawTransaction":
		return c.sendRawTransaction(params)
	case "eth_getTransactionReceipt":
		var hash common.Hash
		if err := param(params, 0, &hash); err != nil {
			return nil, err
		}
		if rec, ok := c.receipts[hash]; ok {
			return rec, nil
		}
		return nil, nil
	case "eth_getLogs":
		return c.getLogs(params)
	}
	c.t.Errorf("fake chain: unhandled JSON-RPC method %s", method)
	return nil, fmt.Errorf("fake chain: method %s is not supported", method)
}

func param(params []json.RawMessage, i int, out interface{}) error {
	if i >= len(params) {
		return fmt.Errorf("missing param %d", i)
	}
	return json.Unmarshal(params[i], out)
}

// method decodes calldata into its ABI method and arguments.
func (c *Chain) method(data []byte) (*abi.Method, []interface{}, error) {
	if len(data) < 4 {
		return nil, nil, fmt.Errorf("calldata of %d bytes has no selector", len(data))
	}
	m, err := c.abi.MethodById(data[:4])
	if err != nil {
		return nil, nil, err
	}
	args, err := m.Inputs.Unpack(data[4:])
	if err != nil {
		return nil, nil, fmt.Errorf("unpack %s arguments: %w", m.Name, err)
	}
	return m, args, nil
}

func (c *Chain) call(params []json.RawMessage) (interface{}, error) {
	var msg struct {
		To    *common.Address `json:"to"`
		Data  hexutil.Bytes   `json:"data"`
		Input hexutil.Bytes   `json:"input"`
	}
	if err := param(params, 0, &msg); err != nil {
		return nil, err
	}
	if msg.To == nil || *msg.To != c.contract {
		return nil, fmt.Errorf("eth_call to %v, not the serving contract", msg.To)
	}
	data := msg.Input
	if len(data) == 0 {
		data = msg.Data
	}
	m, args, err := c.method(data)
	if err != nil {
		return nil, err
	}
	var out []byte
	switch m.Name {
	case "lockTime":
		out, err = m.Outputs.Pack(big.NewInt(int64(LockTime / time.Second)))
	case "getService":
		svc, ok := c.services[args[0].(common.Address)]
		if !ok {
			return nil, errRevert("service not found")
		}
		out, err = m.Outputs.Pack(svc)
	case "getAccount":
		acc, ok := c.accounts[accountKey{args[0].(common.Address), args[1].(common.Address)}]
		if !ok {
			return nil, errRevert("account not exists")
		}
		out, err = m.Outputs.Pack(acc)
	case "getAccountsByProvider":
		provider, offset, limit := args[0].(common.Address), args[1].(*big.Int), args[2].(*big.Int)
		var all []contract.Account
		for k, acc := range c.accounts {
			if k.provider == provider {
				all = append(all, acc)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].User.Hex() < all[j].User.Hex() })
		total := big.NewInt(int64(len(all)))
		start := int(offset.Int64())
		if start > len(all) {
			start = len(all)
		}
		end := start + int(limit.Int64())
		if end > len(all) {
			end = len(all)
		}
		page := append([]contract.Account{}, all[start:end]...)
		out, err = m.Outputs.Pack(page, total)
	case "previewSettlementResults":
		// Every settlement settles in full.
		out, err = m.Outputs.Pack([]common.Address{}, []uint8{}, []common.Address{}, []*big.Int{})
	default:
		c.t.Errorf("fake chain: unhandled contract call %s", m.Name)
		return nil, fmt.Errorf("fake chain: call %s is not supported", m.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("pack %s result: %w", m.Name, err)
	}
	return hexutil.Bytes(out), nil
}

func (c *Chain) sendRawTransaction(params []json.RawMessage) (interface{}, error) {
	var raw hexutil.Bytes
	if err := param(params, 0, &raw); err != nil {
		return nil, err
	}
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(raw); err != nil {
		return nil, fmt.Errorf("decode transaction: %w", err)
	}
	from, err := types.Sender(types.LatestSignerForChainID(big.NewInt(ChainID)), tx)
	if err != nil {
		return nil, fmt.Errorf("recover sender: %w", err)
	}
	if tx.Nonce() != c.nonces[from] {
		return nil, fmt.Errorf("nonce %d for %s, want %d", tx.Nonce(), from.Hex(), c.nonces[from])
	}

	block := c.head + 1
	var logs []*types.Log
	switch {
	case tx.To() == nil:
		return nil, fmt.Errorf("contract creation is not supported")
	case *tx.To() == c.contract:
		logs, err = c.transact(from, tx, block)
		if err != nil {
			return nil, err
		}
	default:
		value := tx.Value()
		bal := c.balances[from]
		if bal == nil || bal.Cmp(value) < 0 {
			return nil, fmt.Errorf("insufficient funds for transfer")
		}
		c.balances[from] = new(big.Int).Sub(bal, value)
		to := *tx.To()
		if c.balances[to] == nil {
			c.balances[to] = big.NewInt(0)
		}
		c.balances[to] = new(big.Int).Add(c.balances[to], value)
		c.transfers = append(c.transfers, Transfer{From: from, To: to, Value: new(big.Int).Set(value)})
	}

	c.nonces[from]++
	blockHash := common.BigToHash(new(big.Int).SetUint64(block))
	for i, l := range logs {
		l.BlockNumber, l.BlockHash, l.TxHash, l.Index = block, blockHash, tx.Hash(), uint(i)
		c.logs = append(c.logs, *l)
	}
	if logs == nil {
		logs = []*types.Log{}
	}
	c.receipts[tx.Hash()] = &types.Receipt{
		Type:              tx.Type(),
		Status:            types.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21_000,
		Logs:              logs,
		TxHash:            tx.Hash(),
		GasUsed:           21_000,
		EffectiveGasPrice: tx.GasPrice(),
		BlockHash:         blockHash,
		BlockNumber:       new(big.Int).SetUint64(block),
	}
	c.head = block
	return tx.Hash(), nil
}

// transact applies a contract transaction from `from` and returns the logs it emits.
func (c *Chain) transact(from common.Address, tx *types.Transaction, block uint64) ([]*types.Log, error) {
	m, args, err := c.method(tx.Data())
	if err != nil {
		return nil, err
	}
	switch m.Name {
	case "addOrUpdateService":
		p := *abi.ConvertType(args[0], new(contract.ServiceParams)).(*contract.ServiceParams)
		c.services[from] = contract.Service{
			Provider:              from,
			ServiceType:           p.ServiceType,
			Url:                   p.Url,
			InputPrice:            p.InputPrice,
			OutputPrice:           p.OutputPrice,
			UpdatedAt:             new(big.Int).SetUint64(block),
			Model:                 p.Model,
			Verifiability:         p.Verifiability,
			AdditionalInfo:        p.AdditionalInfo,
			TeeSignerAddress:      p.TeeSignerAddress,
			TeeSignerAcknowledged: true,
		}
		return nil, nil
	case "settleFeesWithTEE":
		items := *abi.ConvertType(args[0], new([]contract.TEESettlementData)).(*[]contract.TEESettlementData)
		c.settlements = append(c.settlements, Settlement{TxHash: tx.Hash(), Sender: from, Items: items})
		event := c.abi.Events["TEESettlementResult"]
		var logs []*types.Log
		for _, item := range items {
			// The contract settles against the sender's accounts.
			key := accountKey{item.User, from}
			if acc, ok := c.accounts[key]; ok {
				acc.Balance = new(big.Int).Sub(acc.Balance, item.TotalFee)
				c.accounts[key] = acc
			}
			data, err := event.Inputs.NonIndexed().Pack(uint8(0), big.NewInt(0))
			if err != nil {
				return nil, fmt.Errorf("pack TEESettlementResult: %w", err)
			}
			logs = append(logs, &types.Log{
				Address: c.contract,
				Topics:  []common.Hash{event.ID, common.BytesToHash(item.User.Bytes())},
				Data:    data,
			})
		}
		return logs, nil
	}
	c.t.Errorf("fake chain: unhandled contract transaction %s", m.Name)
	return nil, fmt.Errorf("fake chain: transaction %s is not supported", m.Name)
}

func (c *Chain) getLogs(params []json.RawMessage) (interface{}, error) {
	var q struct {
		FromBlock *hexutil.Big      `json:"fromBlock"`
		ToBlock   *hexutil.Big      `json:"toBlock"`
		Topics    []json.RawMessage `json:"topics"`
	}
	if err := param(params, 0, &q); err != nil {
		return nil, err
	}
	var want []common.Hash
	if len(q.Topics) > 0 && !strings.EqualFold(string(q.Topics[0]), "null") {
		if err := json.Unmarshal(q.Topics[0], &want); err != nil {
			var one common.Hash
			if err := json.Unmarshal(q.Topics[0], &one); err != nil {
				return nil, fmt.Errorf("topics[0]: %w", err)
			}
			want = []common.Hash{one}
		}
	}
	out := []types.Log{}
	for _, l := range c.logs {
		if q.FromBlock != nil && l.BlockNumber < q.FromBlock.ToInt().Uint64() {
			continue
		}
		if q.ToBlock != nil && l.BlockNumber > q.ToBlock.ToInt().Uint64() {
			continue
		}
		if len(want) > 0 && !containsHash(want, l.Topics[0]) {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

func containsHash(hs []common.Hash, h common.Hash) bool {
	for _, x := range hs {
		if x == h {
			return true
		}
	}
	return false
}
