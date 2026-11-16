package providercontract

import (
	"context"
	"os"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Against a real chain (the local hardhat node of pipeline/scripts/spml_e2e_local.sh):
//
//	SPML_NODE_RPC=http://127.0.0.1:8545 SPML_NODE_CONTRACT=0x... SPML_NODE_PROVIDER_KEY=<hex> go test -run TestEnsureSpmlNodeOnChain ./inference/internal/contract/
//
// The first call lists a fresh payee with one transaction, the second sends none.
func TestEnsureSpmlNodeOnChain(t *testing.T) {
	rpc := os.Getenv("SPML_NODE_RPC")
	if rpc == "" {
		t.Skip("SPML_NODE_RPC not set")
	}
	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatal(err)
	}
	conf := &config.Config{ContractAddress: os.Getenv("SPML_NODE_CONTRACT")}
	conf.Network = commonconfig.NetworkConfig{URL: rpc, ChainID: 31337, PrivateKeys: []string{os.Getenv("SPML_NODE_PROVIDER_KEY")}, TransactionLimit: 3000000}
	conf.Network.PrivateKeyStore = commonconfig.NewPrivateKeyStore(&conf.Network)
	c, err := NewProviderContract(conf, common.Address{}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	key, _ := crypto.GenerateKey()
	payee := crypto.PubkeyToAddress(key.PublicKey)
	for i, want := range []bool{true, false} {
		sent, err := c.EnsureSpmlNode(context.Background(), payee)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if sent != want {
			t.Fatalf("call %d: sent=%v, want %v", i, sent, want)
		}
	}
	t.Logf("provider %s listed payee %s with one transaction", c.ProviderAddress, payee.Hex())
}
