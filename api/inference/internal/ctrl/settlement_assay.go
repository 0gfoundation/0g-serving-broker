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

	settleable, pendingHashes, cheatHashes := partitionAssayRequests(reqs)

	if len(cheatHashes) > 0 {
		// Cheating detected → the whole batch is void: charge nothing this
		// cycle, for anyone. The deleted rows are the audit trail's job now
		// (verifier keeps per-request and per-node records); log the evidence.
		c.logger.Warnf("Assay: CHEAT DETECTED — %d REJECT'd/INVALID_SIG request(s) %v; voiding the entire settlement batch (%d requests, nothing charged)",
			len(cheatHashes), cheatHashes, len(reqs))
		if err := c.db.DeleteRequestsByHashes(requestHashes(reqs)); err != nil {
			// Deletion failed: the rows survive and will be re-gated next
			// cycle. Still refuse to settle a batch containing cheating.
			c.logger.Errorf("Assay: failed to void batch: %v", err)
		}
		return nil
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
// running), and cheating (REJECT, or INVALID_SIG from strict-mode signature
// failures). Pure (no DB); the side effects live in gateSettlementWithAssay.
func partitionAssayRequests(reqs []model.Request) (settleable []model.Request, pendingHashes, cheatHashes []string) {
	for _, req := range reqs {
		switch req.Verdict {
		case constant.AssayVerdictReject, constant.AssayVerdictInvalidSig:
			cheatHashes = append(cheatHashes, req.RequestHash)
		case constant.AssayVerdictPending:
			pendingHashes = append(pendingHashes, req.RequestHash)
		default:
			settleable = append(settleable, req)
		}
	}
	return settleable, pendingHashes, cheatHashes
}
