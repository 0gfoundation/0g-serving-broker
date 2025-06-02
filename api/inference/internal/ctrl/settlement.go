package ctrl

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/models"
)

type SettlementInfo struct {
	Account                   string `json:"account"`
	RecordedNonceInContract   string `json:"recorded_nonce_in_contract"`
	RecordedBalanceInContract string `json:"recorded_balance_in_contract"`
	MinNonceInSettlement      string `json:"min_nonce_in_settlement"`
	TotalFeeInSettlement      string `json:"total_fee_in_settlement"`
}

func (c *Ctrl) SettleFees(ctx context.Context) error {
	categorizedSettlementInfo := make(map[string]SettlementInfo)

	err := c.pruneRequest(ctx, &categorizedSettlementInfo)
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to prune request")
		return errors.Wrap(err, "prune request")
	}

	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed: false,
		Sort:      model.PtrOf("nonce ASC"),
	})
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to list requests from db")
		return errors.Wrap(err, "list request from db")
	}

	if len(reqs) == 0 {
		c.logger.Info("No requests to settle, resetting unsettled fee")
		return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
	}

	latestReqCreateAt := reqs[0].CreatedAt
	c.logger.WithFields(logrus.Fields{
		"request_count": len(reqs),
		"latest_time":   latestReqCreateAt,
	}).Info("Starting fee settlement process")

	categorizedReqs := make(map[string][]*models.Request)
	categorizedSigs := make(map[string][][]int64)
	for _, req := range reqs {
		if latestReqCreateAt.Before(*req.CreatedAt) {
			latestReqCreateAt = req.CreatedAt
		}

		var sig []int64
		err := json.Unmarshal([]byte(req.Signature), &sig)
		if err != nil {
			c.logger.WithFields(logrus.Fields{
				"error":        err,
				"user_address": req.UserAddress,
				"nonce":        req.Nonce,
				"service_name": req.ServiceName,
			}).Error("Failed to parse signature")
			return errors.New("Failed to parse signature")
		}

		reqInZK := &models.Request{
			Fee:             req.Fee,
			Nonce:           req.Nonce,
			ProviderAddress: c.contract.ProviderAddress,
			UserAddress:     req.UserAddress,
		}

		if v, ok := categorizedSettlementInfo[req.UserAddress]; ok {
			minNonce := v.MinNonceInSettlement

			cmp, err := util.Compare(minNonce, req.Nonce)
			if err != nil {
				c.logger.WithFields(logrus.Fields{
					"error":     err,
					"min_nonce": minNonce,
					"req_nonce": req.Nonce,
				}).Error("Failed to compare nonces")
				return errors.Wrap(err, "compare nonce")
			}
			if minNonce == "0" || cmp > 0 {
				minNonce = req.Nonce
			}

			totalFeeInSettlement, err := util.Add(req.Fee, v.TotalFeeInSettlement)
			if err != nil {
				c.logger.WithFields(logrus.Fields{
					"error":     err,
					"fee":       req.Fee,
					"total_fee": v.TotalFeeInSettlement,
				}).Error("Failed to add fees")
				return errors.Wrap(err, "add fee")
			}

			categorizedSettlementInfo[req.UserAddress] = SettlementInfo{
				Account:                   req.UserAddress,
				RecordedNonceInContract:   v.RecordedNonceInContract,
				RecordedBalanceInContract: v.RecordedBalanceInContract,
				MinNonceInSettlement:      minNonce,
				TotalFeeInSettlement:      totalFeeInSettlement.String(),
			}
		}

		if _, ok := categorizedReqs[req.UserAddress]; ok {
			categorizedReqs[req.UserAddress] = append(categorizedReqs[req.UserAddress], reqInZK)
			categorizedSigs[req.UserAddress] = append(categorizedSigs[req.UserAddress], sig)
			continue
		}

		categorizedReqs[req.UserAddress] = []*models.Request{reqInZK}
		categorizedSigs[req.UserAddress] = [][]int64{sig}
	}

	verifierInput := contract.VerifierInput{
		InProof:     []*big.Int{},
		ProofInputs: []*big.Int{},
		NumChunks:   big.NewInt(0),
		SegmentSize: []*big.Int{},
	}

	for key := range categorizedReqs {
		reqChunks, sigChunks := splitArray(categorizedReqs[key], c.zk.RequestLength), splitArray(categorizedSigs[key], c.zk.RequestLength)
		verifierInput.NumChunks.Add(verifierInput.NumChunks, big.NewInt(int64(len(reqChunks))))

		segmentSize := 0
		for i := range reqChunks {
			calldata, err := c.GenerateSolidityCalldata(ctx, reqChunks[i], sigChunks[i])
			if err != nil {
				c.logger.WithFields(logrus.Fields{
					"error": err,
					"user":  key,
					"chunk": i,
				}).Error("Failed to generate solidity calldata")
				return err
			}

			proof, err := flattenAndConvert([][]string{calldata.PA}, calldata.PB, [][]string{calldata.PC})
			if err != nil {
				c.logger.WithFields(logrus.Fields{
					"error": err,
					"user":  key,
					"chunk": i,
				}).Error("Failed to flatten and convert proof")
				return err
			}

			verifierInput.InProof = append(verifierInput.InProof, proof...)
			proofInputs, err := flattenAndConvert([][]string{calldata.PubInputs})
			if err != nil {
				c.logger.WithFields(logrus.Fields{
					"error": err,
					"user":  key,
					"chunk": i,
				}).Error("Failed to flatten and convert proof inputs")
				return err
			}

			segmentSize += len(proofInputs)
			verifierInput.ProofInputs = append(verifierInput.ProofInputs, proofInputs...)
		}
		verifierInput.SegmentSize = append(verifierInput.SegmentSize, big.NewInt(int64(segmentSize)))
	}

	var settlementInfos []SettlementInfo
	for k := range categorizedSettlementInfo {
		settlementInfos = append(settlementInfos, categorizedSettlementInfo[k])
	}

	settlementInfoJSON, err := json.Marshal(settlementInfos)
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to marshal settlement infos")
		settlementInfoJSON = []byte("[]")
	}

	c.logger.WithFields(logrus.Fields{
		"settlement_infos": string(settlementInfoJSON),
	}).Info("Settlement information")

	if err := c.contract.SettleFees(ctx, verifierInput); err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to settle fees in contract")
		return errors.Wrapf(err, "settle fees in contract")
	}

	if err := c.db.UpdateRequest(latestReqCreateAt); err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
			"time":  latestReqCreateAt,
		}).Error("Failed to update request in db")
		return errors.Wrap(err, "update request in db")
	}

	if err := c.SyncUserAccounts(ctx); err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to synchronize accounts from contract to database")
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

	c.logger.Info("Successfully completed fee settlement")
	return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
}

func (c *Ctrl) ProcessSettlement(ctx context.Context) error {
	settleTriggerThreshold := (c.Service.InputPrice + c.Service.OutputPrice) * constant.SettleTriggerThreshold

	accounts, err := c.db.ListUserAccount(&model.UserListOptions{
		LowBalanceRisk:         model.PtrOf(time.Now().Add(-c.contract.LockTime + c.autoSettleBufferTime)),
		MinUnsettledFee:        model.PtrOf(int64(0)),
		SettleTriggerThreshold: &settleTriggerThreshold,
	})
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to list accounts that need to be settled")
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}

	if len(accounts) == 0 {
		c.logger.Debug("No accounts need settlement")
		return nil
	}

	c.logger.WithFields(logrus.Fields{
		"account_count": len(accounts),
	}).Info("Found accounts that need settlement")

	if err := c.SyncUserAccounts(ctx); err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to synchronize accounts from contract")
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

	accounts, err = c.db.ListUserAccount(&model.UserListOptions{
		MinUnsettledFee:        model.PtrOf(int64(0)),
		LowBalanceRisk:         model.PtrOf(time.Now()),
		SettleTriggerThreshold: &settleTriggerThreshold,
	})
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to list accounts after sync")
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}

	if len(accounts) == 0 {
		c.logger.Debug("No accounts need settlement after sync")
		return nil
	}

	c.logger.WithFields(logrus.Fields{
		"account_count": len(accounts),
	}).Info("Accounts at risk of having insufficient funds, proceeding with immediate settlement")

	return errors.Wrap(c.SettleFees(ctx), "settle fees")
}

func (c *Ctrl) pruneRequest(ctx context.Context, categorizedSettlementInfo *map[string]SettlementInfo) error {
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed: false,
		Sort:      model.PtrOf("nonce ASC"),
	})
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to list requests for pruning")
		return errors.Wrap(err, "list request from db")
	}

	if len(reqs) == 0 {
		c.logger.Debug("No requests to prune")
		return nil
	}

	c.logger.WithFields(logrus.Fields{
		"request_count": len(reqs),
	}).Info("Starting request pruning")

	accountsInDebt := map[string]string{}
	for _, req := range reqs {
		if _, ok := accountsInDebt[req.UserAddress]; !ok {
			accountsInDebt[req.UserAddress] = "1"
		}
	}

	accounts, err := c.contract.ListUserAccount(ctx)
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to list accounts from contract")
		return errors.Wrap(err, "list account from contract")
	}

	for _, account := range accounts {
		if _, ok := accountsInDebt[account.User.String()]; ok {
			accountsInDebt[account.User.String()] = account.Nonce.String()
			if categorizedSettlementInfo != nil && *categorizedSettlementInfo != nil {
				(*categorizedSettlementInfo)[account.User.String()] = SettlementInfo{
					RecordedNonceInContract:   account.Nonce.String(),
					RecordedBalanceInContract: account.Balance.String(),
					MinNonceInSettlement:      "0",
					TotalFeeInSettlement:      "0",
				}
			}
		}
	}

	c.logger.WithFields(logrus.Fields{
		"account_count": len(accountsInDebt),
	}).Info("Pruning requests in database")

	return errors.Wrap(c.db.PruneRequest(accountsInDebt), "prune request in db")
}

func splitArray[T any](arr1 []T, groupSize int) [][]T {
	var splitArr1 [][]T

	for i := 0; i < len(arr1); i += groupSize {
		end := i + groupSize
		if end > len(arr1) {
			end = len(arr1)
		}

		tempArr1 := arr1[i:end]
		splitArr1 = append(splitArr1, tempArr1)
	}

	return splitArr1
}

func flattenAndConvert(inputs ...[][]string) ([]*big.Int, error) {
	var result []*big.Int

	for _, input := range inputs {
		for _, row := range input {
			for _, val := range row {
				num, err := util.HexadecimalStringToBigInt(val)
				if err != nil {
					return []*big.Int{}, err
				}
				result = append(result, num)
			}
		}
	}

	return result, nil
}
