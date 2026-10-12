package ctrl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

type refusingHooks struct {
	noHooks
	err     error
	blocked bool
}

func (h refusingHooks) beforeUpstream(*http.Request, []byte, model.Request) error { return h.err }
func (h refusingHooks) settlementBlocked() (bool, string)                         { return h.blocked, "test" }

// A hook that refuses a request must stop it before it reaches the upstream: the hook
// exists for checks a request has to pass to be sent at all.
func TestProcessHTTPRequest_HookRefusalSendsNothing(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer upstream.Close()

	c := newChatbotTestCtrl(t, config.Service{ProviderType: constant.ProviderTypeDecentralized, Type: constant.ServiceTypeDecisions})
	c.SetHTTPClient(upstream.Client())
	refusal := errors.New("cannot sign")
	c.hooks = refusingHooks{err: refusal}

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/proxy/decisions", nil)
	upReq, err := http.NewRequest(http.MethodPost, upstream.URL+"/decisions", nil)
	require.NoError(t, err)

	err = c.ProcessHTTPRequest(ctx, constant.ServiceTypeDecisions, upReq, model.Request{RequestHash: "h", IsWhitelisted: true}, "0", true)
	require.ErrorIs(t, err, refusal)
	require.Zero(t, hits.Load(), "a refused request must not be forwarded")
}

// A blocked round returns before anything touches the database. This Ctrl has no db,
// so reaching the pruning or listing below the gate would panic.
func TestSettleFeesWithTEE_BlockedRoundTouchesNothing(t *testing.T) {
	c := &Ctrl{logger: testLogger(), hooks: refusingHooks{blocked: true}}
	require.NoError(t, c.SettleFeesWithTEE(context.Background()))
}

// A Ctrl built without hooks behaves as teeml: nothing refused, nothing blocked.
func TestProviderHooks_NilIsTeeml(t *testing.T) {
	h := (&Ctrl{}).providerHooks()
	require.NoError(t, h.beforeUpstream(nil, nil, model.Request{}))
	blocked, _ := h.settlementBlocked()
	require.False(t, blocked)
	reqs := []model.Request{{RequestHash: "a"}}
	require.Equal(t, reqs, h.filterSettlement(context.Background(), reqs))
	require.Nil(t, h.upstreamTLS())
}
