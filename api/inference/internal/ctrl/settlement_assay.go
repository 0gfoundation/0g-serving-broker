package ctrl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/common"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// assayVerdictResult is one request's entry in the verifier's settlement-check
// response (POST {assay.verifierUrl}/v1/settlement/check).
type assayVerdictResult struct {
	Verdict string `json:"verdict"`
	NodeID  string `json:"node_id"`
	// Sig is the verifier's secp256k1 EIP-191 signature over
	// "assay-verdict-v1|<verdict>|<requestHash>" (0x hex). Only final
	// verdicts (PASS/REJECT/UNVERIFIED) are signed; PENDING/UNKNOWN are
	// transient and arrive unsigned.
	Sig string `json:"sig"`
}

// assayCheckResponse is the verifier's settlement-check payload. Summary and
// the advisory settle/cheat flags are informational — the broker re-derives
// the decision from the (authenticated) per-request verdicts.
type assayCheckResponse struct {
	Results       map[string]assayVerdictResult `json:"results"`
	CheatDetected bool                          `json:"cheat_detected"`
	Settle        bool                          `json:"settle"`
}

// gateSettlementWithAssay is the pre-settlement Assay gate. The verifier
// answers requests non-blockingly (ZG-Verdict: PENDING) and audits in the
// background, so right before settling the broker asks it once for the
// batch's final verdicts and then decides:
//
//   - any REJECT (or strict-mode INVALID_SIG) in the batch → cheating was
//     detected: the ENTIRE batch is voided — every request is deleted
//     unsettled and nothing is charged for this cycle;
//   - PENDING audits → those requests are parked (skip_until) and re-gated
//     on a later cycle, the rest of the batch settles;
//   - otherwise (PASS / UNVERIFIED / no verdict) → settle normally.
//
// Verdicts fetched from the verifier are authenticated with
// assay.verifierAddress when configured, exactly like the response-header
// path. If the verifier is unreachable the gate falls back to the verdicts
// recorded from response headers (fail-open for requests without one).
// No-op when the Assay integration is disabled.
func (c *Ctrl) gateSettlementWithAssay(ctx context.Context, reqs []model.Request) []model.Request {
	if !c.assayVerdictFilter || len(reqs) == 0 {
		return reqs
	}

	if c.assayVerifierURL != "" {
		results, err := c.fetchAssayVerdicts(ctx, requestHashes(reqs))
		if err != nil {
			c.logger.Warnf("Assay: settlement check against %s failed (falling back to recorded verdicts): %v",
				c.assayVerifierURL, err)
		} else {
			// Backfill node attribution from the settlement check for rows
			// whose ZG-Node response header was lost (payout needs it).
			for i := range reqs {
				if reqs[i].Node == "" {
					if r, ok := results[reqs[i].RequestHash]; ok && r.NodeID != "" {
						reqs[i].Node = r.NodeID
						if err := c.db.UpdateRequestNode(reqs[i].RequestHash, r.NodeID); err != nil {
							c.logger.Warnf("Assay: failed to backfill node %q for request %s: %v", r.NodeID, reqs[i].RequestHash, err)
						}
					}
				}
			}
			changed := resolveAssayVerdicts(reqs, results, c.assayVerifierAddress, c.assayStrictVerdict)
			for hash, verdict := range changed {
				if err := c.db.UpdateRequestVerdict(hash, verdict); err != nil {
					c.logger.Warnf("Assay: failed to record verdict %q for request %s: %v", verdict, hash, err)
				}
			}
		}
	}

	settleable, rejected, pendingHashes, invalidSigHashes := partitionAssayRequests(reqs)
	rejectHashes := requestHashes(rejected)

	// Void exactly the implicated requests, not the batch they arrived in.
	//
	// This used to delete every unsettled row on any cheat — across all users
	// and all nodes, and the unsettled list is not even the settlement batch,
	// so one REJECT could void an arbitrarily large backlog. That handed any
	// single node a way to zero the provider's revenue indefinitely at no cost
	// to itself: cheat once per cycle. spml-design.en.md §4 step 9 always
	// specified "non-cheat requests only".
	void := append([]string{}, invalidSigHashes...)

	// A REJECT is a threshold judgement and the thresholds are uncalibrated,
	// so by default it is recorded and surfaced but does not withhold money
	// (config assay.rejectGatesSettlement). INVALID_SIG is a signature
	// failure — no calibration involved — and always withholds.
	if len(rejectHashes) > 0 {
		if c.assayRejectGates {
			void = append(void, rejectHashes...)
			c.logger.Warnf("Assay: CHEAT — voiding %d REJECT'd request(s) %v (the remaining %d in this batch still settle)",
				len(rejectHashes), rejectHashes, len(reqs)-len(rejectHashes))
		} else {
			// Advisory: they settle like any other request. Leaving them out
			// of both `void` and `settleable` would park them forever.
			settleable = append(settleable, rejected...)
			c.logger.Warnf("Assay: %d REJECT'd request(s) %v — ADVISORY ONLY, still settling: the LDD thresholds are uncalibrated (set assay.rejectGatesSettlement once the calibration ceremony has been run)",
				len(rejectHashes), rejectHashes)
		}
	}
	if len(invalidSigHashes) > 0 {
		c.logger.Warnf("Assay: %d request(s) with missing/bad verdict signature %v; voiding those",
			len(invalidSigHashes), invalidSigHashes)
	}

	if len(void) > 0 {
		if err := c.db.DeleteRequestsByHashes(void); err != nil {
			// Deletion failed: the rows survive and are re-gated next cycle.
			// They are not in `settleable` either way, so nothing is charged
			// for them now.
			c.logger.Errorf("Assay: failed to void %d request(s): %v", len(void), err)
		}
	}

	if len(pendingHashes) > 0 {
		c.logger.Infof("Assay: %d request(s) with audits still pending; parking them for %v and settling the remaining %d",
			len(pendingHashes), constant.AssayPendingRetryDelay, len(settleable))
		if err := c.markRequestsWithSkipUntil(pendingHashes, constant.AssayPendingRetryDelay); err != nil {
			c.logger.Warnf("Assay: failed to park pending requests: %v", err)
		}
	}
	return settleable
}

// fetchAssayVerdicts asks the verifier for the final verdicts of the given
// request hashes, holding up to assayCheckWaitMs for in-flight audits.
func (c *Ctrl) fetchAssayVerdicts(ctx context.Context, hashes []string) (map[string]assayVerdictResult, error) {
	body, err := json.Marshal(map[string]interface{}{
		"request_hashes": hashes,
		"wait_ms":        c.assayCheckWaitMs,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.assayVerifierURL+"/v1/settlement/check", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.signAssayBody(req, body)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("settlement check returned %d: %s", resp.StatusCode, snippet)
	}
	var parsed assayCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Results, nil
}

// isFinalAssayVerdict reports whether a verdict is a decision rather than a
// placeholder. PENDING (audit still running) and UNKNOWN (verifier has no
// record) are placeholders; INVALID_SIG is final, recorded locally when strict
// mode rejects a response's verdict.
func isFinalAssayVerdict(v string) bool {
	switch v {
	case constant.AssayVerdictPass, constant.AssayVerdictReject,
		constant.AssayVerdictUnverified, constant.AssayVerdictInvalidSig:
		return true
	}
	return false
}

// resolveAssayVerdicts merges the verifier's settlement-check results into the
// requests' verdicts (in place) and returns the {hash: verdict} changes to
// persist. Pure over its inputs so the merge rules are unit-testable:
//
//   - final verdicts (PASS/REJECT/UNVERIFIED): adopted — but when pubkey is
//     set, only with a valid signature; an unauthenticated final verdict is
//     ignored (fail-open) or, in strict mode, recorded as INVALID_SIG;
//   - PENDING: adopted as-is (non-final, only defers settlement — it never
//     needs authentication because it can't decide who gets paid);
//   - UNKNOWN (verifier has no record, e.g. restart): a request stuck in
//     PENDING is downgraded to UNVERIFIED (the audit evidence is gone;
//     fail-open like an unsampled request) — except in strict mode, where it
//     stays PENDING and keeps being parked rather than settling unaudited;
//   - absent from results: left untouched.
func resolveAssayVerdicts(reqs []model.Request, results map[string]assayVerdictResult, signer *common.Address, strict bool) map[string]string {
	changed := make(map[string]string)
	for i := range reqs {
		req := &reqs[i]
		r, ok := results[req.RequestHash]
		if !ok || r.Verdict == "" {
			continue
		}

		// A final verdict is a decision already made and authenticated; a
		// non-final one carries no information that can undo it. Letting
		// PENDING or UNKNOWN land on top of a REJECT laundered it into a
		// payable request: REJECT -> PENDING (parked, unsigned, unauthenticated)
		// -> on a later cycle the verifier answers UNKNOWN (restart, or its
		// in-memory store evicted the entry) -> UNVERIFIED -> settles.
		if isFinalAssayVerdict(req.Verdict) && !isFinalAssayVerdict(r.Verdict) {
			continue
		}

		var verdict string
		switch r.Verdict {
		case constant.AssayVerdictPending:
			verdict = constant.AssayVerdictPending
		case constant.AssayVerdictUnknown:
			if req.Verdict != constant.AssayVerdictPending || strict {
				continue
			}
			verdict = constant.AssayVerdictUnverified
		case constant.AssayVerdictPass, constant.AssayVerdictReject, constant.AssayVerdictUnverified:
			if signer != nil && !verifyAssayVerdictSig(*signer, r.Verdict, req.RequestHash, r.Sig) {
				if !strict {
					continue
				}
				verdict = constant.AssayVerdictInvalidSig
			} else {
				verdict = r.Verdict
			}
		default:
			// Unrecognized verdict value: never let it into the DB.
			continue
		}

		if verdict != req.Verdict {
			req.Verdict = verdict
			changed[req.RequestHash] = verdict
		}
	}
	return changed
}

func requestHashes(reqs []model.Request) []string {
	hashes := make([]string, len(reqs))
	for i, req := range reqs {
		hashes[i] = req.RequestHash
	}
	return hashes
}

// partitionAssayRequests splits reqs by their effective verdict, preserving
// order: settleable (PASS, UNVERIFIED, or no verdict), pending (audit still
// running), rejected (the LDD threshold verdict), and invalid-sig (strict-mode
// signature failures). The last two are kept apart because only one of them is
// a calibration judgement — see gateSettlementWithAssay. Pure (no DB); the
// side effects live in gateSettlementWithAssay.
func partitionAssayRequests(reqs []model.Request) (settleable, rejected []model.Request, pendingHashes, invalidSigHashes []string) {
	for _, req := range reqs {
		switch req.Verdict {
		case constant.AssayVerdictReject:
			rejected = append(rejected, req)
		case constant.AssayVerdictInvalidSig:
			invalidSigHashes = append(invalidSigHashes, req.RequestHash)
		case constant.AssayVerdictPending:
			pendingHashes = append(pendingHashes, req.RequestHash)
		default:
			settleable = append(settleable, req)
		}
	}
	return settleable, rejected, pendingHashes, invalidSigHashes
}
