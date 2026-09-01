package tee

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestClientTypeForNetwork(t *testing.T) {
	const zgTestnetChainID = 16601

	tests := []struct {
		name    string
		network string
		chainID int64
		want    ClientType
		wantErr string
	}{
		{name: "hardhat on the local node", network: "hardhat", chainID: hardhatChainID, want: Mock},
		{
			name:    "hardhat pointed at a real chain",
			network: "hardhat",
			chainID: zgTestnetChainID,
			want:    Phala,
			wantErr: "constant committed to this repository",
		},
		{
			// An unset chainID is the shape a half-filled config has, and it is
			// not the local node, so it must be refused for the same reason.
			name:    "hardhat with no chainID configured",
			network: "hardhat",
			chainID: 0,
			want:    Phala,
			wantErr: "rather than the local hardhat node's",
		},
		{name: "removed gcp backend", network: "gcp", chainID: zgTestnetChainID, want: Phala, wantErr: "no longer supported"},
		{name: "removed alicloud backend", network: "alicloud", chainID: zgTestnetChainID, want: Phala, wantErr: "no longer supported"},
		{name: "phala", network: "phala", chainID: zgTestnetChainID, want: Phala},
		{name: "unset defaults to phala", network: "", chainID: zgTestnetChainID, want: Phala},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ClientTypeForNetwork(tt.network, tt.chainID)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected an error mentioning %q, got none (client type %v)", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not mention %q", err, tt.wantErr)
				}
			}
			// Checked on the error paths too: ClientType's zero value is Mock, so
			// a rejected combination that still answered Mock would hand out the
			// backend this function exists to withhold.
			if got != tt.want {
				t.Errorf("client type = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestMockKeysAreRecomputableFromTheRepository is the premise ClientTypeForNetwork
// rests on. MockTappdClient.DeriveKey ignores its path argument and returns a
// literal, so the signer key and the E2EE key material are the same public
// constant. If a future change gives the mock backend real per-path entropy this
// test fails, and the chain guard can be reconsidered rather than silently kept
// for a reason that stopped being true.
func TestMockKeysAreRecomputableFromTheRepository(t *testing.T) {
	c := &MockTappdClient{}
	ctx := t.Context()

	signerMaterial, err := c.DeriveKey(ctx, "/")
	if err != nil {
		t.Fatalf("DeriveKey(/): %v", err)
	}
	encMaterial, err := c.DeriveKey(ctx, encKeyDerivePath)
	if err != nil {
		t.Fatalf("DeriveKey(%s): %v", encKeyDerivePath, err)
	}
	if signerMaterial != encMaterial {
		t.Fatalf("mock backend now separates key paths; revisit the ClientTypeForNetwork chain guard")
	}

	signer, err := crypto.HexToECDSA(signerMaterial)
	if err != nil {
		t.Fatalf("HexToECDSA: %v", err)
	}
	const knownAddress = "0x325744be57db298C2652c672B32Eb12875d92D83"
	if got := crypto.PubkeyToAddress(signer.PublicKey).Hex(); got != knownAddress {
		t.Fatalf("mock signer address = %s, want the publicly derivable %s", got, knownAddress)
	}
}

// chainIDServer is a minimal JSON-RPC endpoint that answers eth_chainId with the
// given id, which is all VerifyChainIsLocal asks a node for.
func chainIDServer(t *testing.T, id int64) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode rpc request: %v", err)
			return
		}
		if req.Method != "eth_chainId" {
			t.Errorf("unexpected rpc method %q; VerifyChainIsLocal should ask only for the chain id", req.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x%x"}`, req.ID, id)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestVerifyChainIsLocal(t *testing.T) {
	const zgTestnetChainID = 16601

	t.Run("phala never dials", func(t *testing.T) {
		// A URL nothing is listening on: reaching the network at all would fail
		// the test, which is the property real deployments depend on.
		if err := VerifyChainIsLocal(t.Context(), Phala, "http://127.0.0.1:1"); err != nil {
			t.Fatalf("Phala must skip the check entirely, got %v", err)
		}
	})

	t.Run("mock against the local node", func(t *testing.T) {
		if err := VerifyChainIsLocal(t.Context(), Mock, chainIDServer(t, hardhatChainID)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mock against a real chain that a config declared as local", func(t *testing.T) {
		// The gap ClientTypeForNetwork alone leaves: network.chainID says 31337,
		// network.url points somewhere else. Only dialing catches it.
		err := VerifyChainIsLocal(t.Context(), Mock, chainIDServer(t, zgTestnetChainID))
		if err == nil {
			t.Fatal("expected a refusal when the node reports a real chain id")
		}
		if !strings.Contains(err.Error(), "16601") {
			t.Errorf("error should name the observed chain id, got: %v", err)
		}
	})

	t.Run("mock with no url", func(t *testing.T) {
		if err := VerifyChainIsLocal(t.Context(), Mock, ""); err == nil {
			t.Fatal("expected a refusal when there is no url to check")
		}
	})

	t.Run("mock against an unreachable node", func(t *testing.T) {
		if err := VerifyChainIsLocal(t.Context(), Mock, "http://127.0.0.1:1"); err == nil {
			t.Fatal("expected a refusal when the node cannot be reached")
		}
	})
}
