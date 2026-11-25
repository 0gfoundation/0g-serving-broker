package ctrl

import (
	stderrors "errors"
	"fmt"

	paramnorm "github.com/0gfoundation/0g-param-normalization"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

// The request-parameter handling for a chat request (output-token cap,
// max_tokens ↔ max_completion_tokens, reasoning_effort → the native thinking
// control, request profile) lives in the shared library
// github.com/0gfoundation/0g-param-normalization, which the router also uses.
// The broker's part is resolving which model entry a request targets and
// mapping its config onto the library's switches.

// paramRow builds the library's view of the entry a request resolved to. model
// is the resolved canonical id (CtxKeyResolvedModel), NOT the body's "model"
// field, which ValidateModelAllowlist may have rewritten to the upstream id;
// identity is the router-named upstream (config.UpstreamIdentityHeader). An
// empty model selects the service-level ModelInfo.
//
// The row is never nil: the request profile comes from the pricing entry or the
// service (RequestProfileFor), not from ModelInfo, so it applies to an entry
// that has no ModelInfo. A row with no ModelInfo fields is a no-op for the cap
// and both translations.
func (c *Ctrl) paramRow(model, identity string) *paramnorm.Row {
	row := &paramnorm.Row{RequestProfile: c.Service.RequestProfileFor(model, identity)}
	if mi := c.Service.EffectiveModelInfoFor(model, identity); mi != nil {
		row.SupportedParameters = mi.SupportedParameters
		row.SupportedFormats = mi.SupportedFormats
		row.ReasoningEffortLevels = mi.ReasoningEffortLevels
		row.ThinkingTypes = mi.ThinkingTypes
		row.MaxCompletionTokens = mi.MaxCompletionTokens
		row.ContextLength = mi.ContextLength
	}
	return row
}

// paramOptions maps the service's switches onto the library's:
//   - the translations (max_tokens rename, reasoning) always run;
//   - the output-token cap runs only with enforceMaxCompletionTokens;
//   - the request profile runs when one is configured: applied with
//     requestProfileEnforce, otherwise computed and logged only.
func (c *Ctrl) paramOptions(row *paramnorm.Row) paramnorm.Options {
	opts := paramnorm.Options{Translate: paramnorm.On}
	if c.Service.EnforceMaxCompletionTokens {
		opts.CapOutput = paramnorm.On
	}
	if row.RequestProfile != "" {
		opts.Drop = paramnorm.DryRun
		if c.Service.RequestProfileEnforce {
			opts.Drop = paramnorm.On
		}
	}
	return opts
}

// normalizeParams rewrites a chat request body for the entry it resolved to.
// The library fixes the order: cap, then the max_tokens rename (so a request
// that carried no cap gets one under whichever name this upstream accepts),
// then reasoning, then the request profile (so it judges what is actually
// forwarded). surface is the API surface the request arrived on
// (apiFormatForPath). See docs/design/request-translation.md and
// docs/design/reasoning-translation.md.
func (c *Ctrl) normalizeParams(body []byte, model, identity, surface string) ([]byte, error) {
	row := c.paramRow(model, identity)
	res, err := paramnorm.Normalize(paramnorm.Request{Body: body, Format: surface}, row, c.paramOptions(row))
	if err != nil {
		var se *paramnorm.StepError
		if stderrors.As(err, &se) {
			return nil, errors.Wrap(err, stepErrorPrefix[se.Step])
		}
		return nil, err
	}
	c.logParamChanges(res, row.RequestProfile, model)
	return res.Body, nil
}

// stepErrorPrefix labels a failed step as PrepareHTTPRequest always has.
var stepErrorPrefix = map[paramnorm.Step]string{
	paramnorm.StepCapOutput:      "cap max output tokens",
	paramnorm.StepMaxTokens:      "translate max tokens",
	paramnorm.StepReasoning:      "translate reasoning",
	paramnorm.StepRequestProfile: "apply request profile",
}

// logParamChanges writes the lines the broker has always logged for these
// rewrites (names only, never a cap's value).
func (c *Ctrl) logParamChanges(res paramnorm.Result, profile, model string) {
	var dropped []string
	for _, ch := range res.Applied {
		switch {
		case ch.Step == paramnorm.StepCapOutput && ch.Action == paramnorm.ActionNotInjected:
			// Fail open, and say so. "Configured but never applied" is otherwise
			// indistinguishable from "applied and the client asked for less".
			c.logger.Infof("capMaxOutputTokens: no room for the advertised %s-token cap alongside an estimated %s-token prompt; forwarding without one", ch.To, ch.From)
		case ch.Step == paramnorm.StepMaxTokens && ch.Action == paramnorm.ActionDropped:
			// The client sent both spellings; the supported one is forwarded.
			to := "max_tokens"
			if ch.Param == to {
				to = "max_completion_tokens"
			}
			c.logger.Infof("translateMaxTokens: request for model %q sent both %q and %q; forwarding %q and dropping unsupported %q", model, ch.Param, to, to, ch.Param)
		case ch.Step == paramnorm.StepRequestProfile:
			dropped = append(dropped, fmt.Sprintf("%s=%q", ch.Param, ch.From))
		}
	}
	if len(dropped) > 0 {
		c.logger.Infof("request profile %q dropped %v for model %q", profile, dropped, model)
	}
	var wouldDrop []string
	for _, ch := range res.Skipped {
		wouldDrop = append(wouldDrop, fmt.Sprintf("%s=%q", ch.Param, ch.From))
	}
	if len(wouldDrop) > 0 {
		c.logger.Infof("request profile %q would drop %v for model %q (log-only)", profile, wouldDrop, model)
	}
}
