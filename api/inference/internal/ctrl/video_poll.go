package ctrl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// InitVideoPollScheduler records cfg and, if cfg.Enabled, starts the background
// poll-to-completion scheduler for video-generation jobs whose create response was
// non-terminal (queued/in_progress). It bills the actual delivered duration once a terminal
// state is observed, instead of guessing from the requested duration. See
// docs/design/video-generation-async-billing.md.
//
// c.videoPollCfg is set UNCONDITIONALLY, even when cfg.Enabled is false — callers should call
// this once at startup regardless, passing whatever VideoPollConfig the operator configured
// (config.GetConfig() always populates it with sane values). This is deliberate: a video-gen
// request accepted while the scheduler is disabled still needs real PollInterval/MaxPollDuration
// values to schedule its VideoPollJob against (see deferVideoBillingToPoll in video.go) — using
// the OPERATOR'S configured values here, rather than a hardcoded fallback duplicating config.go's
// defaults, is both simpler and more correct (an operator who explicitly tuned these values while
// temporarily disabling the scheduler gets their own values honored, not silently ignored).
func (c *Ctrl) InitVideoPollScheduler(cfg config.VideoPollConfig) error {
	c.videoPollCfg = cfg
	if !cfg.Enabled {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.videoPollCtx = ctx
	c.videoPollCancel = cancel

	c.videoPollWg.Add(1)
	go func() {
		defer c.videoPollWg.Done()
		c.runVideoPollScanner(ctx)
	}()

	c.videoPollWg.Add(1)
	go func() {
		defer c.videoPollWg.Done()
		c.runVideoPollCleanup(ctx)
	}()

	c.videoPollEnabled.Store(true)
	c.logger.Infof("Video-generation poll scheduler initialized: maxConcurrentPolls=%d, pollInterval=%v, maxPollDuration=%v, scanInterval=%v, cleanupInterval=%v",
		cfg.MaxConcurrentPolls, cfg.PollInterval, cfg.MaxPollDuration, cfg.ScanInterval, cfg.CleanupInterval)
	return nil
}

// ShutdownVideoPollScheduler stops the scanner and cleanup goroutines and waits for any
// in-flight poll round-trip to unwind. Canceling videoPollCancel here also cancels
// videoPollCtx, the parent context doVideoPollRequest derives its per-request timeout from, so
// an in-flight HTTP poll is interrupted immediately rather than running to its own
// PollRequestTimeout — shutdown latency is bounded by that cancellation propagating, not by the
// configured timeout. Unlike ShutdownAsync there is no queue to drain: all scheduling state
// lives in the video_poll_job table, so any row not claimed by the time this returns simply
// waits for the next broker start — see ClaimDueVideoPollJobs' crash-recovery semantics (an
// unclaimed or stale-leased row is picked up exactly like a fresh one).
func (c *Ctrl) ShutdownVideoPollScheduler() {
	if !c.videoPollEnabled.CompareAndSwap(true, false) {
		return
	}
	c.videoPollCancel()
	c.videoPollWg.Wait()
}

// IsVideoPollEnabled reports whether the scheduler is running.
func (c *Ctrl) IsVideoPollEnabled() bool {
	return c.videoPollEnabled.Load()
}

// videoPollBaseCtx returns the scheduler's cancelable context (canceled on shutdown), or
// context.Background() if the scheduler was never initialized with one — a *Ctrl built
// directly (as several unit tests do) has a nil videoPollCtx.
func (c *Ctrl) videoPollBaseCtx() context.Context {
	if c.videoPollCtx != nil {
		return c.videoPollCtx
	}
	return context.Background()
}

func (c *Ctrl) runVideoPollScanner(ctx context.Context) {
	ticker := time.NewTicker(c.videoPollCfg.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.scanAndPollVideoJobs()
		}
	}
}

// scanAndPollVideoJobs claims up to MaxConcurrentPolls due jobs and polls each concurrently,
// waiting for the batch to finish before returning control to the scanner's ticker. Bounding
// concurrency to the claim batch size avoids unbounded goroutine growth without a separate
// semaphore.
func (c *Ctrl) scanAndPollVideoJobs() {
	jobs, err := c.videoPollDB.ClaimDueVideoPollJobs(c.videoPollCfg.MaxConcurrentPolls, c.videoPollCfg.LeaseWindow)
	if err != nil {
		c.logger.Errorf("video poll scheduler: claim due jobs: %v", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(job model.VideoPollJob) {
			defer wg.Done()
			c.pollVideoJob(job)
		}(job)
	}
	wg.Wait()
}

func (c *Ctrl) runVideoPollCleanup(ctx context.Context) {
	ticker := time.NewTicker(c.videoPollCfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.videoPollDB.DeleteExpiredVideoPollJobs(c.videoPollCfg.RetentionTTL); err != nil {
				c.logger.Errorf("video poll scheduler: cleanup expired jobs: %v", err)
			}
		}
	}
}

// recordWhitelistedVideoPollUsage records a resolved (or zero, on failure/timeout) whitelisted
// video job into the hourly_usage_stat reconciliation rollup. The original ephemeral
// whitelistReq (proxy.go) never reaches this background scheduler and is never persisted —
// whitelisted traffic creates no Request row — so this reconstructs the minimal model.Request
// recordWhitelistedUsage needs from the job row itself: ModelName from ResolvedModel (empty is
// fine — recordWhitelistedUsage falls back to c.Service.ModelType) and CreatedAt from the job's
// own creation time (its bucket hour IS the original request's, since the job is created in
// the same call that received the request). Upstream is resolved per-model from ResolvedModel
// (matching the synchronous proxy.go path) so a multi-upstream provider attributes a whitelisted
// video to the upstream it actually hit, not the service-level identity; Unit is deliberately
// left for recordWhitelistedUsage's own DefaultBillingUnitForService fallback.
func (c *Ctrl) recordWhitelistedVideoPollUsage(job model.VideoPollJob, seconds int64, rateClass string) {
	c.recordWhitelistedUsage(model.Request{
		Model:       model.Model{CreatedAt: job.CreatedAt},
		ServiceName: "video-generation",
		ModelName:   job.ResolvedModel,
		// ponytail: no identity — the async poll has no request/header and the job
		// persists only ResolvedModel (PollURL already bakes in the upstream). A
		// same-model multi-upstream VIDEO config would attribute to the first entry
		// here; persist the identity on VideoPollJob if that config is ever supported.
		Upstream: c.UpstreamForModel(job.ResolvedModel, ""),
	}, 0, seconds, 0, 0, rateClass)
}

// pollVideoJob issues one GET to job.PollURL and advances the job's state: bills (or, for
// job.IsWhitelisted, records reconciliation usage) and marks completed on a terminal
// "completed" response, marks failed on a terminal "failed" response or an unresolvable
// "completed" response, and reschedules on anything else — unless job.ExpiresAt has already
// passed, in which case it is forced to timed_out regardless of what this poll would have
// returned.
func (c *Ctrl) pollVideoJob(job model.VideoPollJob) {
	if time.Now().After(job.ExpiresAt) {
		c.logger.Errorf("video poll job %d (request %s) timed out after %d attempts without reaching a terminal state; "+
			"the provider may have delivered a video the broker never billed for — this is a reconciliation gap, not routine",
			job.ID, job.RequestHash, job.Attempts)
		monitor.RecordVideoPollTimedOut()
		if err := c.videoPollDB.TimeOutVideoPollJob(job.ID, job.Attempts, job.RequestHash, "exceeded MaxPollDuration without reaching a terminal state"); err != nil {
			if errors.Is(err, db.ErrVideoPollJobAlreadyResolved) {
				c.logger.Infof("video poll job %d: already resolved by another worker, skipping duplicate timeout handling", job.ID)
			} else {
				c.logger.Errorf("video poll job %d: mark timed_out: %v", job.ID, err)
			}
		} else {
			// Terminal without ever signing a final body (see the completed-but-
			// unresolvable path below): the provider may still deliver, so the
			// create-time proof over the queued placeholder must not be what a client
			// verifies the video against.
			c.evictVideoSignature(job, errors.New("timed out before any terminal response could be signed"))
			// Only the worker that actually won the guarded write (err == nil, not a lost
			// race) records usage — otherwise two racing workers could both record it.
			if job.IsWhitelisted {
				c.recordWhitelistedVideoPollUsage(job, 0, "")
			}
		}
		return
	}

	body, respHeader, respTLS, ok := c.doVideoPollRequest(job)
	if !ok {
		c.rescheduleVideoPollJob(job)
		return
	}

	var fields videoResponseFields
	_ = json.Unmarshal(body, &fields)

	if fields.Status == videoStatusFailed {
		c.logger.Infof("video poll job %d (request %s): provider reported failed", job.ID, job.RequestHash)
		monitor.RecordVideoGenerationFailed()
		if err := c.videoPollDB.FailVideoPollJob(job.ID, job.Attempts, job.RequestHash, "provider reported status=failed"); err != nil {
			if errors.Is(err, db.ErrVideoPollJobAlreadyResolved) {
				c.logger.Infof("video poll job %d: already resolved by another worker, skipping duplicate failure handling", job.ID)
			} else {
				c.logger.Errorf("video poll job %d: mark failed: %v", job.ID, err)
			}
		} else {
			// Evict even though no video was generated. "Nothing delivered" is not the
			// same as "no final body": the failed job resource is itself a body the
			// client can GET /videos/{id}, and this service's contract is that
			// ZG-Res-Key covers the FINAL body, not the create envelope. Leaving the
			// queued-envelope proof would have a client following that contract compare
			// it against {"status":"failed"} and see a valid TEE signature over the
			// wrong hash — the false-tampering signal every other eviction prevents.
			c.evictVideoSignature(job, errors.New("provider reported failed; final body never signed"))
			if job.IsWhitelisted {
				c.recordWhitelistedVideoPollUsage(job, 0, "")
			}
		}
		return
	}

	// Deliberately NOT classifyVideoStatus here: that function's default case treats an
	// absent/unrecognized status as "bill now", which is right for a CREATE response (that's
	// how a shim that blocks until completion and never plays the status game looks) but
	// wrong mid-poll — this job only exists because its create response already said
	// queued/in_progress, so an intermediate malformed/empty poll response is far more
	// likely a transient hiccup than a genuine synchronous completion.
	//
	// Only an EXPLICIT "completed" status ends the poll — NOT merely resolveVideoBilling
	// finding a usable duration. A real OpenAI-Video-API-shaped job resource commonly echoes
	// the requested top-level "seconds" as part of the object on every GET, including while
	// still queued/in_progress (videoResponseFields.Seconds doubles as both the request-edge
	// field and the actual-output field — see its doc comment). Treating that echo as
	// "actual output present" would end the poll on the very first non-terminal response and
	// bill the requested duration, defeating the entire point of polling for providers that
	// behave exactly like the real async contract this feature targets.
	if fields.Status != videoStatusCompleted {
		c.rescheduleVideoPollJob(job)
		return
	}

	// Terminal and completed: this is the one poll that owes a routing proof, so it
	// is the one that resolves (and, on a miss, logs and meters) the evidence. An
	// empty ChatKey means the create response never advertised one, so no proof was
	// ever promised — resolving anyway would meter a second loss for a request the
	// create path already counted.
	var upstreamCertFingerprint string
	if job.ChatKey != "" {
		upstreamCertFingerprint = c.upstreamCertFingerprint(respHeader, respTLS)
	}

	seconds, size, source := resolveVideoBilling(body, job.RequestBody, job.RequestContentType)

	if source == "" {
		// Reported completed but no usable duration anywhere (response nor the original
		// request) — mirrors the sync path's "billing indeterminate" case (video.go).
		// Don't guess, don't keep polling a job that already reached a terminal state.
		c.logger.Errorf("video poll job %d (request %s): provider reported completed but no usable seconds in response or original request; NOT billing (free output)",
			job.ID, job.RequestHash)
		monitor.RecordVideoBillingSkipped()
		if err := c.videoPollDB.FailVideoPollJob(job.ID, job.Attempts, job.RequestHash, "completed with no resolvable duration"); err != nil {
			if errors.Is(err, db.ErrVideoPollJobAlreadyResolved) {
				c.logger.Infof("video poll job %d: already resolved by another worker, skipping duplicate failure handling", job.ID)
			} else {
				c.logger.Errorf("video poll job %d: mark failed: %v", job.ID, err)
			}
		} else {
			// Terminal WITHOUT a re-sign: the provider says completed (so the client
			// can fetch the finished video) but we could not resolve a duration, so
			// nothing above signed the final body. Leaving the create-time signature
			// in place would have the client verify the delivered video against a
			// proof over the queued placeholder — a hash mismatch that reads as
			// tampering. Drop it so the lookup 404s instead.
			c.evictVideoSignature(job, errors.New("completed with no resolvable duration; final body never signed"))
			if job.IsWhitelisted {
				c.recordWhitelistedVideoPollUsage(job, 0, "")
			}
		}
		return
	}
	if source == videoSourceRequest {
		c.logger.Warnf("video poll job %d (request %s): billed on REQUESTED duration (provider's completed response did not report actual output); configure the upstream/shim to echo seconds or usage.output_video_duration",
			job.ID, job.RequestHash)
	}

	// videoOutputUnits (multi-model path) resolves per-model pricing from a *gin.Context
	// carrying CtxKeyResolvedModel — there is no live HTTP request in the background
	// scheduler, so synthesize a minimal one carrying just the value captured at create
	// time. A bare &gin.Context{} is sufficient: Get/Set only touch its Keys map.
	pollCtx := &gin.Context{}
	if job.ResolvedModel != "" {
		pollCtx.Set(CtxKeyResolvedModel, job.ResolvedModel)
	}
	// The tier the vendor's own rules say this is, not the raw "size" — see
	// ctrl.VideoBillingTier. This path is where it matters most: a vendor that
	// reports no resolution back leaves settlement holding the size the CLIENT
	// sent, which for pixel dimensions is not a price-table key at all.
	tier := c.VideoBillingTier(pollCtx, size)
	// completionTokens is Seedance's per_video_token billing signal — the poll
	// response's usage.completion_tokens, ignored (0) by every other vendor/mode.
	// This is the actual live path for Seedance, which always creates "queued"
	// and resolves here, not in the synchronous handleVideoGenerationResponse path.
	outputCount := c.videoOutputUnits(pollCtx, seconds, tier, fields.completionTokens())
	rateClass := resolutionRateClass(tier)

	// Same check as the synchronous path (video.go), and the one that matters:
	// an async create is where the reserve is load-bearing, since the fee lands
	// minutes after the gate let the request through.
	c.WarnVideoDurationDrift(pollCtx, job.RequestBody, job.RequestContentType, seconds)
	// The path that matters for a token-billed vendor: it always creates "queued"
	// and resolves here, so this is where a stale per-second rate surfaces.
	c.WarnVideoTokenEstimateDrift(pollCtx, job.RequestBody, job.RequestContentType, fields.completionTokens())

	if job.IsWhitelisted {
		// Commit the completion write BEFORE signing/recording usage, for the same reason as
		// the paying-user path below: only the worker that actually wins the attempts-fenced
		// write may act on the result, or two racing workers could both record usage / sign.
		if err := c.videoPollDB.CompleteVideoPollJobWhitelisted(job.ID, job.Attempts); err != nil {
			if errors.Is(err, db.ErrVideoPollJobAlreadyResolved) {
				c.logger.Infof("video poll job %d: already resolved by another worker, skipping duplicate whitelist usage recording", job.ID)
				return
			}
			c.logger.Errorf("video poll job %d (request %s): complete whitelisted job: %v", job.ID, job.RequestHash, err)
			return
		}
		if job.ChatKey != "" {
			if err := c.signVideoPollResult(job, body, upstreamCertFingerprint); err != nil {
				c.dropStaleVideoSignature(job, err)
			}
		}
		c.recordWhitelistedVideoPollUsage(job, seconds, rateClass)
		metricModel := job.MetricModel
		if metricModel == "" {
			metricModel = c.Service.ModelType
		}
		monitor.RecordTokens("video-generation", metricModel, 0, outputCount)
		monitor.RecordWhitelistTokens("video-generation", metricModel, 0, outputCount)
		return
	}

	// Paying path only — the whitelist branch above returns. A whitelisted job is
	// unbilled BY DESIGN, so "served free" is not a finding about it; see
	// warnIfTokenBillingObservedNothing.
	c.warnIfTokenBillingObservedNothing(pollCtx, fields.completionTokens())

	outputFee, err := util.Multiply(job.OutputPrice, outputCount)
	if err != nil {
		c.logger.Errorf("video poll job %d (request %s): calculate output fee: %v", job.ID, job.RequestHash, err)
		c.rescheduleVideoPollJob(job)
		return
	}

	// Commit the billing write BEFORE signing. In the stale-lease-reclaim race this job is
	// designed to survive, two workers can both reach this point for the same job; only one of
	// them wins the attempts-fenced CompleteVideoPollJobWithBilling below. Signing first (as
	// this code used to) would let the LOSING worker's signChatWithKey call still land in the
	// chatKey cache (last-write-wins) after the winner's own signature — binding the signature
	// the client eventually fetches to a response body that may not byte-for-byte match what
	// was actually billed. Gating the sign call on this call succeeding means only the worker
	// that actually wrote the fee ever signs.
	//
	// Reconciliation records the RAW seconds (unit=seconds) with the resolution as rate_class,
	// not the resolution-weighted units — same convention as the sync path (video.go). The
	// weighted units live only in outputFee above and the RecordTokens metric below.
	if err := c.videoPollDB.CompleteVideoPollJobWithBilling(job.ID, job.Attempts, job.RequestHash, outputFee.String(), outputFee.String(),
		seconds, constant.BillingUnitSeconds, rateClass); err != nil {
		if errors.Is(err, db.ErrVideoPollJobAlreadyResolved) {
			// Benign: a stale-lease reclaim let another worker resolve this job first (see
			// ClaimDueVideoPollJobs' crash-recovery semantics). The Request row was
			// deliberately not touched a second time, so nothing was double-billed, and this
			// (losing) worker must not sign either — the winner already did.
			c.logger.Infof("video poll job %d: already resolved by another worker, skipping duplicate billing", job.ID)
			return
		}
		if errors.Is(err, db.ErrVideoPollJobRequestMissing) {
			// Not benign: a real fee was computed but the linked Request row is gone (e.g.
			// pruned while this job was still in flight) — the write rolled back entirely, so
			// the job is still claimed by us. Fail it explicitly instead of leaving it to spin
			// until MaxPollDuration: retrying cannot make the Request row reappear.
			c.logger.Errorf("video poll job %d (request %s): linked request row no longer exists; fee %s was computed but NOT recorded — reconciliation gap",
				job.ID, job.RequestHash, outputFee.String())
			monitor.RecordVideoBillingSkipped()
			if failErr := c.videoPollDB.FailVideoPollJob(job.ID, job.Attempts, job.RequestHash, "linked request row no longer exists"); failErr != nil && !errors.Is(failErr, db.ErrVideoPollJobAlreadyResolved) {
				c.logger.Errorf("video poll job %d: mark failed: %v", job.ID, failErr)
			} else if failErr == nil {
				// The worst instance of the never-re-signed class: the provider DID
				// report completed and the video is fetchable, but the job is now
				// terminal so no later poll will sign the final body.
				c.evictVideoSignature(job, errors.New("linked request row no longer exists; final body never signed"))
			}
			return
		}
		c.logger.Errorf("video poll job %d (request %s): complete with billing: %v", job.ID, job.RequestHash, err)
		return
	}

	if job.ChatKey != "" {
		// Re-sign under the SAME chatKey already returned to the client via the create
		// response's ZG-Res-Key header, overwriting the placeholder signature made over
		// the queued-status body (signChatWithKey just overwrites the cache entry keyed
		// by chatKey — see signing.go) so a client verifying against ZG-Res-Key gets a
		// signature over the job's real, final state. Note it binds the terminal poll
		// JSON body, not the mp4 bytes served by /videos/{id}/content.
		if err := c.signVideoPollResult(job, body, upstreamCertFingerprint); err != nil {
			c.dropStaleVideoSignature(job, err)
		}
	}

	metricModel := job.MetricModel
	if metricModel == "" {
		metricModel = c.Service.ModelType
	}
	monitor.RecordTokens("video-generation", metricModel, 0, outputCount)
}

// evictVideoSignature drops the create-time signature cached under job.ChatKey.
//
// Call it on any outcome where a final body EXISTS (or may still be delivered) that
// the client can obtain, but nothing re-signed it. The create response was signed
// over the queued placeholder on the assumption that a poll would overwrite it;
// when that never happens, the client fetches a VALID broker signature whose
// response hash does not match the video it downloaded — indistinguishable from
// tampering. A 404 is the honest answer.
//
// The test is whether a body the client can OBTAIN exists, not whether a video was
// produced: a provider-reported failure is still a job resource the client can GET,
// so that path evicts too. The one case that does not is a create response with no
// job id — with no id the client cannot fetch anything, so the cached signature
// still describes exactly the response it holds (see dropUnpollableVideoSignature).
//
// Deliberately not gated on IsCentralized(): a decentralized in-network provider's
// content signature goes just as stale as a routing proof.
func (c *Ctrl) evictVideoSignature(job model.VideoPollJob, cause error) {
	if job.ChatKey == "" {
		return
	}
	c.svcCache.Delete(c.chatCacheKey(job.ChatKey))
	c.logger.Errorf("video poll job %d: no final body was ever signed; dropped the create-time signature so ZG-Res-Key 404s instead of returning a proof over the queued placeholder: %v", job.ID, cause)
}

// dropStaleVideoSignature is evictVideoSignature for the case where a re-sign was
// actually ATTEMPTED and failed — the only case that represents a lost routing
// proof, and so the only one that feeds the skipped-proof counter.
func (c *Ctrl) dropStaleVideoSignature(job model.VideoPollJob, cause error) {
	// No metric here: whichever step failed already counted it with the reason it
	// knows — upstreamCertFingerprint for missing evidence, signCentralizedRoutingProof
	// for a signing failure. Counting again would double-report one lost proof, and a
	// failure on the DECENTRALIZED branch (signChatWithKey) is not a routing-proof
	// skip at all.
	c.evictVideoSignature(job, cause)
}

// signVideoPollResult is the background scheduler's counterpart to signVideoResponse
// (video.go): same dispatch on the trust model, but reading the poll's own upstream
// certificate instead of a *gin.Context — there is no live HTTP request here.
//
// It re-signs under the SAME chatKey the create response already returned to the
// client via ZG-Res-Key, overwriting the earlier signature over the queued-status
// body (both signers just overwrite the cache entry keyed by chatKey — see
// signing.go), so a client that verifies after fetching the finished video gets a
// proof over the real content.
func (c *Ctrl) signVideoPollResult(job model.VideoPollJob, body []byte, upstreamCertFingerprint string) error {
	if c.Service.IsCentralized() {
		// Video-generation rejects per-model providerIdentity at config load, so ""
		// falls back to the service-level identity inside the signer.
		return c.signCentralizedRoutingProof(job.RequestBody, body, job.ChatKey, upstreamCertFingerprint, "")
	}
	return c.signChatWithKey(job.RequestBody, body, job.ChatKey)
}

// doVideoPollRequest issues one GET to job.PollURL and returns the response body. ok is false
// on any transport/status/read error, all of which the caller treats as "try again next
// interval" (bounded by ExpiresAt), not an immediate failure — a single blip should not lose a
// job that would otherwise have billed correctly on the next attempt.
//
// respHeader/respTLS are this poll's own evidence for Ctrl.upstreamCertFingerprint:
// a centralized provider's routing proof over the completed body must bind the
// connection that actually delivered that body, not the one the create request used
// possibly hours earlier. They are returned unresolved so the caller can resolve
// once, at the terminal poll, rather than on every attempt.
func (c *Ctrl) doVideoPollRequest(job model.VideoPollJob) (body []byte, respHeader http.Header, respTLS *tls.ConnectionState, ok bool) {
	pollCtx, cancel := context.WithTimeout(c.videoPollBaseCtx(), c.videoPollCfg.PollRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, job.PollURL, nil)
	if err != nil {
		c.logger.Errorf("video poll job %d: build poll request: %v", job.ID, err)
		return nil, nil, nil, false
	}
	httpReq.Header.Set("Accept-Encoding", "identity")
	// Per-model secret keyed on the job's resolved model, so a poll to an upstream
	// with per-model API keys uses the same key the create request used.
	for k, v := range c.Service.EffectiveAdditionalSecret(job.ResolvedModel) {
		httpReq.Header.Set(k, v)
	}
	// Last, matching the other two request builders. This is the one that actually
	// talks to the targetTLSProxy sidecar and whose RESPONSE header is trusted as
	// evidence, so it is the builder where an outbound copy of that header would
	// matter most — an operator naming it in additionalSecret is the only way it
	// could get here, and that must not be able to prime an echo.
	httpReq.Header.Del(teeutil.HeaderUpstreamCertFingerprint)
	httpReq.Header.Del(teeutil.HeaderUpstreamCertHost)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.logger.Warnf("video poll job %d: poll request failed (will retry): %v", job.ID, err)
		return nil, nil, nil, false
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Warnf("video poll job %d: read poll response (will retry): %v", job.ID, err)
		return nil, nil, nil, false
	}

	if resp.StatusCode != http.StatusOK {
		// Fingerprinted rather than truncated. The comment this replaces had the risk
		// right — a misbehaving intermediary can echo request content into an error body —
		// but bounding it kept the leak and only made it smaller, on a line that logs at
		// Warn and so runs at the default level. See bodyFingerprintForLog.
		c.logger.Warnf("video poll job %d: poll returned status %d (will retry): %s", job.ID, resp.StatusCode, bodyFingerprintForLog(respBody))
		return nil, nil, nil, false
	}

	// For forwarder providers, strip #184 upstream identity/cost leak fields before this body
	// is parsed for status or signed — mirrors handleVideoGenerationResponse's sync path
	// (video.go) exactly: decode a compressed body first (an upstream that ignores our
	// identity request would otherwise slip past the JSON status parse entirely, silently
	// stalling this job's polling until it times out), then sanitize. Doing this here, once,
	// means every downstream consumer in pollVideoJob (status check, resolveVideoBilling,
	// signChatWithKey) sees the same bytes — sanitize-before-sign keeps the signature bound to
	// what the client will actually receive when it separately fetches /videos/{id}/content.
	if c.Service.IsForwarder() {
		respBody = c.sanitizeForwarderPollResponseBody(respBody, resp.Header.Get("Content-Encoding"))
	}

	// Deliberately NOT resolving the routing-proof fingerprint here: this runs on
	// every poll while a proof is owed only on the terminal one. Hand the two
	// response fields the resolver reads back to the caller instead — neither is
	// affected by the deferred Body close — so resolution (which logs and meters a
	// miss) happens exactly once, where the proof is actually due.
	return respBody, resp.Header, resp.TLS, true
}

// sanitizeForwarderPollResponseBody mirrors sanitizeForwarderResponseBody (sanitize.go) for the
// background poller, which — unlike a live request handler — has no *gin.Context/ResponseWriter
// to strip a Content-Encoding header from (the client's own separate GET /videos/{id} request
// handles its own response headers independently via the normal proxy path). Only the body
// needs decoding + leak-field sanitization here.
func (c *Ctrl) sanitizeForwarderPollResponseBody(body []byte, contentEncoding string) []byte {
	out := body
	if isCompressedEncoding(contentEncoding) {
		decoded, err := decodeBody(body, contentEncoding)
		if err != nil {
			c.logger.Warnf("#184 leak sanitization SKIPPED: could not decode %s poll response; using raw body (potential identity/cost leak): %v", contentEncoding, err)
			return body
		}
		out = decoded
	}
	if sanitized, changed := c.sanitizeResponseBody(out, ""); changed {
		return sanitized
	}
	return out
}

// truncateForLog bounds a byte slice to at most max bytes for log output, appending a marker
// when truncated so the reader knows the body was cut, not naturally short.
func truncateForLog(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...(truncated)"
}

// rescheduleVideoPollJob returns a claimed job to pending for another attempt after
// PollInterval.
func (c *Ctrl) rescheduleVideoPollJob(job model.VideoPollJob) {
	next := time.Now().Add(c.videoPollCfg.PollInterval)
	if err := c.videoPollDB.RescheduleVideoPollJob(job.ID, job.Attempts, next); err != nil {
		c.logger.Errorf("video poll job %d: reschedule: %v", job.ID, err)
	}
}
