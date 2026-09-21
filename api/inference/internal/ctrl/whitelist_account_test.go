package ctrl

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// TestProcessHTTPRequest_WhitelistedSkipsOnChainAccount: a whitelisted caller
// is never billed, so ProcessHTTPRequest must not require its on-chain
// sub-account with this provider. The Ctrl here has NO db and NO contract
// client — before the skip, the account lookup dereferenced them and the
// request could not even be forwarded; the router's whitelisted wallet hit
// exactly this ("account not exists") on a provider it had never funded.
func TestProcessHTTPRequest_WhitelistedSkipsOnChainAccount(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"typesafe/jev-1.13","answers":{"q":{"type":"noul","noul":0.9}},"usage":{"input_tokens":12,"output_tokens":3},"id":"x"}`))
	}))
	defer upstream.Close()

	c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeDecentralized, Type: constant.ServiceTypeDecisions})
	mockDB := &mockReconciliationDB{}
	c.reconciliationDB = mockDB
	c.SetHTTPClient(upstream.Client())
	// Address only: the underlying contract handle stays nil, so any on-chain
	// account lookup on this path would dereference it and fail the test.
	c.contract = &providercontract.ProviderContract{ProviderAddress: "0xa15Cb38DDfe114b282336fB368504D19404b9c72"}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/proxy/decisions", nil)

	upReq, err := http.NewRequest(http.MethodPost, upstream.URL+"/decisions", nil)
	require.NoError(t, err)

	reqModel := model.Request{UserAddress: "0xBB3f5b0b5062CB5B3245222C5917afD1f6e13aF6", IsWhitelisted: true, ServiceName: "decisions", RequestHash: "h"}
	err = c.ProcessHTTPRequest(ctx, constant.ServiceTypeDecisions, upReq, reqModel, "0", true)
	require.NoError(t, err, "a whitelisted request must be served without an on-chain account")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"answers"`)
	require.Len(t, mockDB.calls, 1, "whitelisted usage is still recorded for reconciliation")
	assert.Equal(t, int64(12), mockDB.calls[0].InputCount)
}
