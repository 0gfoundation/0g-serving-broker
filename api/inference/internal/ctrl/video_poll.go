package ctrl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
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
// (config.Default() always populates it with sane values). This is deliberate: a video-gen
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
// in-flight poll round-trip to finish. Unlike ShutdownAsync there is no queue to drain: all
// scheduling state lives in the video_poll_job table, so any row not claimed by the time this
// returns simply waits for the next broker start — see ClaimDueVideoPollJobs' crash-recovery
// semantics (an unclaimed or stale-leased row is picked up exactly like a fresh one).
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

// pollVideoJob issues one GET to job.PollURL and advances the job's state: bills and marks
// completed on a terminal "completed" response, marks failed on a terminal "failed" response
// or an unresolvable "completed" response, and reschedules on anything else — unless
// job.ExpiresAt has already passed, in which case it is forced to timed_out regardless of
// what this poll would have returned.
func (c *Ctrl) pollVideoJob(job model.VideoPollJob) {
	if time.Now().After(job.ExpiresAt) {
		c.logger.Errorf("video poll job %d (request %s) timed out after %d attempts without reaching a terminal state; "+
			"the provider may have delivered a video the broker never billed for — this is a reconciliation gap, not routine",
			job.ID, job.RequestHash, job.Attempts)
		monitor.RecordVideoPollTimedOut()
		if err := c.videoPollDB.TimeOutVideoPollJob(job.ID, job.Attempts, "exceeded MaxPollDuration without reaching a terminal state"); err != nil {
			c.logger.Errorf("video poll job %d: mark timed_out: %v", job.ID, err)
		}
		return
	}

	body, ok := c.doVideoPollRequest(job)
	if !ok {
		c.rescheduleVideoPollJob(job)
		return
	}

	var fields videoResponseFields
	_ = json.Unmarshal(body, &fields)

	if fields.Status == videoStatusFailed {
		c.logger.Infof("video poll job %d (request %s): provider reported failed", job.ID, job.RequestHash)
		monitor.RecordVideoGenerationFailed()
		if err := c.videoPollDB.FailVideoPollJob(job.ID, job.Attempts, "provider reported status=failed"); err != nil {
			c.logger.Errorf("video poll job %d: mark failed: %v", job.ID, err)
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

	seconds, size, source := resolveVideoBilling(body, job.RequestBody, job.RequestContentType)

	if source == "" {
		// Reported completed but no usable duration anywhere (response nor the original
		// request) — mirrors the sync path's "billing indeterminate" case (video.go).
		// Don't guess, don't keep polling a job that already reached a terminal state.
		c.logger.Errorf("video poll job %d (request %s): provider reported completed but no usable seconds in response or original request; NOT billing (free output)",
			job.ID, job.RequestHash)
		monitor.RecordVideoBillingSkipped()
		if err := c.videoPollDB.FailVideoPollJob(job.ID, job.Attempts, "completed with no resolvable duration"); err != nil {
			c.logger.Errorf("video poll job %d: mark failed: %v", job.ID, err)
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
	outputCount := c.videoOutputUnits(pollCtx, seconds, size)

	outputFee, err := util.Multiply(job.OutputPrice, outputCount)
	if err != nil {
		c.logger.Errorf("video poll job %d (request %s): calculate output fee: %v", job.ID, job.RequestHash, err)
		c.rescheduleVideoPollJob(job)
		return
	}

	if job.ChatKey != "" {
		// Re-sign under the SAME chatKey already returned to the client via the create
		// response's ZG-Res-Key header, overwriting the placeholder signature made over
		// the queued-status body (signChatWithKey just overwrites the cache entry keyed
		// by chatKey — see signing.go) so a client that fetches /videos/{id}/content and
		// verifies against ZG-Res-Key gets a signature over the real, final content.
		if err := c.signChatWithKey(job.RequestBody, body, job.ChatKey); err != nil {
			c.logger.Warnf("video poll job %d: failed to sign completed response (TEE verification will be unavailable): %v", job.ID, err)
		}
	}

	if err := c.videoPollDB.CompleteVideoPollJobWithBilling(job.ID, job.Attempts, job.RequestHash, outputFee.String(), outputFee.String(), outputCount); err != nil {
		if errors.Is(err, db.ErrVideoPollJobAlreadyResolved) {
			// Benign: a stale-lease reclaim let another worker resolve this job first (see
			// ClaimDueVideoPollJobs' crash-recovery semantics). The Request row was
			// deliberately not touched a second time, so nothing was double-billed.
			c.logger.Infof("video poll job %d: already resolved by another worker, skipping duplicate billing", job.ID)
			return
		}
		c.logger.Errorf("video poll job %d (request %s): complete with billing: %v", job.ID, job.RequestHash, err)
		return
	}

	metricModel := job.MetricModel
	if metricModel == "" {
		metricModel = c.Service.ModelType
	}
	monitor.RecordTokens("video-generation", metricModel, 0, outputCount)
}

// doVideoPollRequest issues one GET to job.PollURL and returns the response body. ok is false
// on any transport/status/read error, all of which the caller treats as "try again next
// interval" (bounded by ExpiresAt), not an immediate failure — a single blip should not lose a
// job that would otherwise have billed correctly on the next attempt.
func (c *Ctrl) doVideoPollRequest(job model.VideoPollJob) (body []byte, ok bool) {
	pollCtx, cancel := context.WithTimeout(context.Background(), c.videoPollCfg.PollRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, job.PollURL, nil)
	if err != nil {
		c.logger.Errorf("video poll job %d: build poll request: %v", job.ID, err)
		return nil, false
	}
	httpReq.Header.Set("Accept-Encoding", "identity")
	for k, v := range c.Service.AdditionalSecret {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.logger.Warnf("video poll job %d: poll request failed (will retry): %v", job.ID, err)
		return nil, false
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Warnf("video poll job %d: read poll response (will retry): %v", job.ID, err)
		return nil, false
	}

	if resp.StatusCode != http.StatusOK {
		// Bounded: an error body can be arbitrarily large (or, from a misbehaving
		// intermediary, echo back request content), and this logs on every retry until the
		// job resolves — truncate rather than writing the full body to broker logs each time.
		c.logger.Warnf("video poll job %d: poll returned status %d (will retry): %s", job.ID, resp.StatusCode, truncateForLog(respBody, 500))
		return nil, false
	}

	return respBody, true
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
