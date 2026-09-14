package ctrl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

// InitAudioPollScheduler records cfg and, when enabled, starts the background
// poll-to-completion scheduler for audio-generation jobs whose create response was
// non-terminal.
//
// c.audioPollCfg is set UNCONDITIONALLY, even when disabled, and callers should
// call this once at startup regardless. A create accepted while the scheduler is
// off still needs real PollInterval/MaxPollDuration values to schedule its job
// against, and using the OPERATOR'S configured values rather than a hardcoded
// fallback means someone who tuned them while temporarily disabling the scheduler
// gets their values honoured instead of silently ignored.
func (c *Ctrl) InitAudioPollScheduler(cfg config.AudioPollConfig) error {
	c.audioPollCfg = cfg
	if !cfg.Enabled {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	c.audioPollCtx = ctx
	c.audioPollCancel = cancel

	c.audioPollWg.Add(1)
	go func() {
		defer c.audioPollWg.Done()
		c.runAudioPollScanner(ctx)
	}()

	c.audioPollWg.Add(1)
	go func() {
		defer c.audioPollWg.Done()
		c.runAudioPollCleanup(ctx)
	}()

	c.audioPollEnabled.Store(true)
	c.logger.Infof("Audio-generation poll scheduler initialized: maxConcurrentPolls=%d, pollInterval=%v, maxPollDuration=%v, scanInterval=%v, cleanupInterval=%v",
		cfg.MaxConcurrentPolls, cfg.PollInterval, cfg.MaxPollDuration, cfg.ScanInterval, cfg.CleanupInterval)
	return nil
}

// ShutdownAudioPollScheduler stops the scanner and cleanup goroutines and waits for
// any in-flight poll to unwind. Cancelling the context also cancels the parent that
// doAudioPollRequest derives its per-request timeout from, so a hung poll is
// interrupted immediately rather than running to PollRequestTimeout.
//
// There is no queue to drain: all scheduling state lives in the audio_poll_job
// table, so a row not claimed by the time this returns simply waits for the next
// broker start — see ClaimDueAudioPollJobs' crash-recovery semantics.
func (c *Ctrl) ShutdownAudioPollScheduler() {
	if !c.audioPollEnabled.CompareAndSwap(true, false) {
		return
	}
	c.audioPollCancel()
	c.audioPollWg.Wait()
}

// IsAudioPollEnabled reports whether the scheduler is running.
func (c *Ctrl) IsAudioPollEnabled() bool {
	return c.audioPollEnabled.Load()
}

// audioPollBaseCtx returns the scheduler's cancelable context, or Background() when
// the scheduler was never initialized — a *Ctrl built directly (as unit tests do)
// has a nil audioPollCtx.
func (c *Ctrl) audioPollBaseCtx() context.Context {
	if c.audioPollCtx != nil {
		return c.audioPollCtx
	}
	return context.Background()
}

func (c *Ctrl) runAudioPollScanner(ctx context.Context) {
	ticker := time.NewTicker(c.audioPollCfg.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.scanAndPollAudioJobs()
		}
	}
}

// scanAndPollAudioJobs claims up to MaxConcurrentPolls due jobs and polls each
// concurrently, waiting for the batch before returning to the ticker. Bounding
// concurrency to the claim batch size avoids unbounded goroutine growth without a
// separate semaphore.
func (c *Ctrl) scanAndPollAudioJobs() {
	jobs, err := c.audioPollDB.ClaimDueAudioPollJobs(c.audioPollCfg.MaxConcurrentPolls, c.audioPollCfg.LeaseWindow)
	if err != nil {
		c.logger.Errorf("audio poll scheduler: claim due jobs: %v", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(job model.AudioPollJob) {
			defer wg.Done()
			c.pollAudioJob(job)
		}(job)
	}
	wg.Wait()
}

func (c *Ctrl) runAudioPollCleanup(ctx context.Context) {
	ticker := time.NewTicker(c.audioPollCfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.audioPollDB.DeleteExpiredAudioPollJobs(c.audioPollCfg.RetentionTTL); err != nil {
				c.logger.Errorf("audio poll scheduler: cleanup expired jobs: %v", err)
			}
		}
	}
}

// pollAudioJob issues one GET to job.PollURL and advances the job's state.
//
// The shape follows pollVideoJob, with one deliberate divergence at the terminal
// step — see the "completed" branch below.
func (c *Ctrl) pollAudioJob(job model.AudioPollJob) {
	if time.Now().After(job.ExpiresAt) {
		c.logger.Errorf("audio poll job %d (request %s) timed out after %d attempts without reaching a terminal state; "+
			"the vendor may have produced audio it charged us for that the broker never billed — a reconciliation gap, not routine",
			job.ID, job.RequestHash, job.Attempts)
		monitor.RecordAudioPollTimedOut()
		if err := c.audioPollDB.TimeOutAudioPollJob(job.ID, job.Attempts, job.RequestHash, "exceeded MaxPollDuration without reaching a terminal state"); err != nil {
			c.logAudioResolveErr(job, "mark timed_out", err)
		} else if job.IsWhitelisted {
			// Only the worker that WON the guarded write records usage; a lost race
			// returns the sentinel above and skips this, so two workers cannot both
			// record it.
			c.recordWhitelistedAudioPollUsage(job, 0)
		}
		return
	}

	body, ok := c.doAudioPollRequest(job)
	if !ok {
		c.rescheduleAudioPollJob(job)
		return
	}

	fields := parseAudioResponseFields(body)

	if fields.Status == audioStatusFailed {
		c.logger.Infof("audio poll job %d (request %s): vendor reported failed", job.ID, job.RequestHash)
		monitor.RecordAudioGenerationFailed()
		if err := c.audioPollDB.FailAudioPollJob(job.ID, job.Attempts, job.RequestHash, "vendor reported status=failed"); err != nil {
			c.logAudioResolveErr(job, "mark failed", err)
		} else if job.IsWhitelisted {
			c.recordWhitelistedAudioPollUsage(job, 0)
		}
		return
	}

	// Deliberately NOT classifyAudioStatus here. That function's default arm treats an
	// absent/unrecognized status as "bill now", which is right for a CREATE response —
	// that is how an adaptor blocking until completion looks — but wrong mid-poll: this
	// job exists only because its create response already said queued/in_progress, so a
	// malformed or empty poll response is far more likely a transient hiccup than a
	// genuine synchronous completion.
	if fields.Status != audioStatusCompleted {
		c.rescheduleAudioPollJob(job)
		return
	}

	// Terminal and completed.
	//
	// DIVERGENCE FROM VIDEO, and it is the payoff of the reserve being a true bound.
	// pollVideoJob has a fourth outcome here: "completed but no resolvable duration",
	// which fails the job and serves the output for FREE, because video has no number
	// it could honestly substitute. resolveAudioBilling always returns one — falling
	// back to the ceiling the gate already held against this caller's balance — so
	// audio never reaches that state. The caller was told up front that this much could
	// be charged, and the vendor did produce output we are being invoiced for, so
	// charging the bound beats giving it away.
	//
	// It still OVER-bills whenever it fires, so it is metered rather than merely
	// logged: broker_audio_billing_fallback_total{source} is how an operator finds out
	// the adaptor stopped reporting durations, which is the actual defect behind it.
	seconds, source := resolveAudioBilling(fields, job.ReservedSeconds)
	if source == audioQuantityReserve {
		c.logger.Errorf("audio poll job %d (request %s): vendor reported completed but no usable duration in the response; "+
			"billing the reserved ceiling of %ds instead. This OVER-bills — check that the adaptor is reporting usage.output_audio_seconds",
			job.ID, job.RequestHash, seconds)
	}
	monitor.RecordAudioBillingSource(string(source))

	if job.IsWhitelisted {
		if err := c.audioPollDB.CompleteAudioPollJobWhitelisted(job.ID, job.Attempts); err != nil {
			c.logAudioResolveErr(job, "mark completed (whitelisted)", err)
			return
		}
		c.recordWhitelistedAudioPollUsage(job, seconds)
		return
	}

	fee, err := util.Multiply(job.OutputPrice, seconds)
	if err != nil {
		c.logger.Errorf("audio poll job %d: calculate fee from price %q x %ds: %v", job.ID, job.OutputPrice, seconds, err)
		c.rescheduleAudioPollJob(job)
		return
	}

	// rateClass is empty: audio-generation has no price class (one flat per-second
	// rate, no tier axis — see config.BillingModePerAudioSecond). The column is still
	// written so the row shape matches every other modality's, which is what lets one
	// reconciliation query cover them all.
	if err := c.audioPollDB.CompleteAudioPollJobWithBilling(
		job.ID, job.Attempts, job.RequestHash,
		fee.String(), fee.String(), seconds,
		constant.BillingUnitSeconds, "",
	); err != nil {
		c.logAudioResolveErr(job, "complete with billing", err)
		return
	}

	c.logger.Infof("audio poll job %d (request %s): completed, billed %ds from %s at %s/s",
		job.ID, job.RequestHash, seconds, source, job.OutputPrice)
}

// logAudioResolveErr reports a terminal-write failure, distinguishing the benign
// lost-race case from a real error. A lost race is expected under a stale-lease
// reclaim and means the OTHER worker resolved the job correctly — logging it at
// Error would make a normal concurrency outcome look like a fault.
func (c *Ctrl) logAudioResolveErr(job model.AudioPollJob, what string, err error) {
	if errors.Is(err, db.ErrAudioPollJobAlreadyResolved) {
		c.logger.Infof("audio poll job %d: already resolved by another worker, skipping duplicate %s", job.ID, what)
		return
	}
	c.logger.Errorf("audio poll job %d: %s: %v", job.ID, what, err)
}

// recordWhitelistedAudioPollUsage records a resolved (or zero, on failure/timeout)
// whitelisted job into the hourly_usage_stat reconciliation rollup.
//
// The original ephemeral whitelisted request never reaches this background
// scheduler and is never persisted, so this reconstructs the minimal model.Request
// the rollup needs from the job row itself: CreatedAt is the job's own creation
// time, whose bucket hour IS the original request's because the job is created in
// the same call that received it.
func (c *Ctrl) recordWhitelistedAudioPollUsage(job model.AudioPollJob, seconds int64) {
	c.recordWhitelistedUsage(model.Request{
		Model:       model.Model{CreatedAt: job.CreatedAt},
		ServiceName: constant.ServiceTypeAudioGeneration,
		ModelName:   job.ResolvedModel,
		Upstream:    c.UpstreamForModel(job.ResolvedModel, ""),
	}, 0, seconds, 0, 0, "")
}

// doAudioPollRequest issues one GET to the job's poll URL. ok=false means "could not
// get an answer this time" — the caller reschedules rather than resolving, because a
// transient network failure is not a vendor verdict.
func (c *Ctrl) doAudioPollRequest(job model.AudioPollJob) (body []byte, ok bool) {
	pollCtx, cancel := context.WithTimeout(c.audioPollBaseCtx(), c.audioPollCfg.PollRequestTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(pollCtx, http.MethodGet, job.PollURL, nil)
	if err != nil {
		c.logger.Errorf("audio poll job %d: build poll request: %v", job.ID, err)
		return nil, false
	}
	httpReq.Header.Set("Accept-Encoding", "identity")
	// Per-model secrets keyed on the job's resolved model, so a poll to an upstream
	// with per-model credentials uses the same ones the create used. For Seed Audio
	// this is the whole openspeech header SET, not a single Authorization — see the
	// design doc.
	for k, v := range c.Service.EffectiveAdditionalSecret(job.ResolvedModel) {
		httpReq.Header.Set(k, v)
	}
	// Stripped last, matching the other request builders: an operator naming these in
	// additionalSecret is the only way they could reach an outbound poll, and an
	// outbound copy must never be able to prime an echo of evidence we then trust.
	httpReq.Header.Del(teeutil.HeaderUpstreamCertFingerprint)
	httpReq.Header.Del(teeutil.HeaderUpstreamCertHost)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.logger.Warnf("audio poll job %d: poll request failed (will retry): %v", job.ID, err)
		return nil, false
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Warnf("audio poll job %d: read poll response (will retry): %v", job.ID, err)
		return nil, false
	}
	if resp.StatusCode != http.StatusOK {
		c.logger.Warnf("audio poll job %d: poll returned HTTP %d (will retry): %s",
			job.ID, resp.StatusCode, truncateForLog(respBody, 256))
		return nil, false
	}
	return respBody, true
}

// rescheduleAudioPollJob returns a claimed job to pending for another attempt after
// PollInterval.
func (c *Ctrl) rescheduleAudioPollJob(job model.AudioPollJob) {
	next := time.Now().Add(c.audioPollCfg.PollInterval)
	if err := c.audioPollDB.RescheduleAudioPollJob(job.ID, job.Attempts, next); err != nil {
		c.logger.Errorf("audio poll job %d: reschedule: %v", job.ID, err)
	}
}
