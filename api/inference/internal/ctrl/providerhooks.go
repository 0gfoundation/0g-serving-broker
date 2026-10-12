package ctrl

import (
	"context"
	"crypto/tls"
	"net/http"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

// providerHooks is what a provider type adds to the request and settlement path every
// type shares. The shared path calls these unconditionally, so a type's extra behaviour
// lives behind one value chosen at startup instead of a type test at each call site.
// A type that adds nothing uses noHooks.
type providerHooks interface {
	// upstreamTLS is the TLS config for the shared upstream client, or nil for Go's
	// default certificate validation. Read once, when the client is built.
	upstreamTLS() *tls.Config
	// beforeUpstream runs on every proxied request, after its body has been read and
	// before it is sent. An error refuses the request: nothing is forwarded.
	beforeUpstream(req *http.Request, body []byte, reqModel model.Request) error
	// onChargedResponse runs once per billed upstream response, streamed or not,
	// before its body is read.
	onChargedResponse(resp *http.Response, reqModel model.Request)
	// settlementBlocked, when true, skips a whole settlement round before anything is
	// listed, pruned, signed or deleted; the next round tries again.
	settlementBlocked() (blocked bool, why string)
	// filterSettlement may drop requests from a round, or hold them back for a later
	// one, before they are batched for signing.
	filterSettlement(ctx context.Context, reqs []model.Request) []model.Request
	// afterSettlement sees a round's outcomes before the settled rows are deleted.
	afterSettlement(ctx context.Context, outcomes []*SettlementOutcome)
}

type noHooks struct{}

func (noHooks) upstreamTLS() *tls.Config                                              { return nil }
func (noHooks) beforeUpstream(*http.Request, []byte, model.Request) error             { return nil }
func (noHooks) onChargedResponse(*http.Response, model.Request)                       {}
func (noHooks) settlementBlocked() (bool, string)                                     { return false, "" }
func (noHooks) afterSettlement(context.Context, []*SettlementOutcome)                 {}
func (noHooks) filterSettlement(_ context.Context, r []model.Request) []model.Request { return r }

// providerHooks returns the hooks NewCtrl chose, or noHooks for a Ctrl built without
// them (tests construct Ctrl literals).
func (c *Ctrl) providerHooks() providerHooks {
	if c.hooks == nil {
		return noHooks{}
	}
	return c.hooks
}
