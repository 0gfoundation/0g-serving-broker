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
	const zgTestnetChainID = 16602 // the wizard's testnet base config

	tests := []struct {
		name    string
		network string
		chainID int64
		// url is only dialled on the hardhat path; every other case can leave it
		// pointing nowhere, which is itself the assertion that no dial happens.
		url     func(t *testing.T) string
		want    ClientType
		wantErr string
	}{
		{
			name:    "hardhat on the local node",
			network: "hardhat",
			chainID: hardhatChainID,
			url:     func(t *testing.T) string { return chainIDServer(t, hardhatChainID) },
			want:    Mock,
		},
		{
			// The declaration says local, the node says otherwise. Only dialling catches it.
			name:    "hardhat declaring 31337 while pointing at a real chain",
			network: "hardhat",
			chainID: hardhatChainID,
			url:     func(t *testing.T) string { return chainIDServer(t, zgTestnetChainID) },
			want:    Phala,
			wantErr: "16602",
		},
		{
			name:    "hardhat with no url to check",
			network: "hardhat",
			chainID: hardhatChainID,
			want:    Phala,
			wantErr: "network.url is empty",
		},
		{
			name:    "hardhat against an unreachable node",
			network: "hardhat",
			chainID: hardhatChainID,
			url:     func(t *testing.T) string { return "http://127.0.0.1:1" },
			want:    Phala,
			wantErr: "must be confirmed local",
		},
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
			url := ""
			if tt.url != nil {
				url = tt.url(t)
			}
			got, err := ClientTypeForNetwork(t.Context(), tt.network, tt.chainID, url)
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

func TestVerifierForNetworkAgreesWithClientType(t *testing.T) {
	tests := []struct {
		network  string
		chainID  int64
		wantType ClientType
		wantVer  string
	}{
		{network: "", chainID: 16602, wantType: Phala, wantVer: VerifierDStack},
		{network: "phala", chainID: 16602, wantType: Phala, wantVer: VerifierDStack},
		{network: "hardhat", chainID: hardhatChainID, wantType: Mock, wantVer: VerifierCryptoPilot},
	}

	for _, tt := range tests {
		t.Run("NETWORK="+tt.network, func(t *testing.T) {
			url := ""
			if tt.network == "hardhat" {
				url = chainIDServer(t, hardhatChainID)
			}
			gotType, err := ClientTypeForNetwork(t.Context(), tt.network, tt.chainID, url)
			if err != nil {
				t.Fatalf("ClientTypeForNetwork: %v", err)
			}
			if gotType != tt.wantType {
				t.Errorf("client type = %v, want %v", gotType, tt.wantType)
			}
			if got := VerifierForNetwork(tt.network); got != tt.wantVer {
				t.Errorf("verifier = %q, want %q", got, tt.wantVer)
			}
			// The property that matters: the Phala backend never advertises a verifier
			// that cannot check a dstack quote.
			if gotType == Phala && VerifierForNetwork(tt.network) != VerifierDStack {
				t.Errorf("NETWORK=%q selects the Phala backend but advertises %q", tt.network, VerifierForNetwork(tt.network))
			}
		})
	}
}
