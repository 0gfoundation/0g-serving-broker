package ctrl

import (
	"context"
	"encoding/json"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/0glabs/0g-serving-broker/inference/zkclient/models"
	"github.com/ethereum/go-ethereum/common/hexutil"
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
		return errors.Wrap(err, "prune request")
	}
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed: false,
		Sort:      model.PtrOf("nonce ASC"),
	})
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}
	if len(reqs) == 0 {
		return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
	}
	latestReqCreateAt := reqs[0].CreatedAt

	categorizedReqs := make(map[string][]*models.RequestResponse)
	categorizedSigs := make(map[string][][]int64)
	categorizedTeeSigs := make(map[string][][]int64)
	for _, req := range reqs {
		if latestReqCreateAt.Before(*req.CreatedAt) {
			latestReqCreateAt = req.CreatedAt
		}

		var sig []int64
		err := json.Unmarshal([]byte(req.Signature), &sig)
		if err != nil {
			return errors.New("Failed to parse signature")
		}

		var teeSig []int64
		err = json.Unmarshal([]byte(req.TeeSignature), &teeSig)
		if err != nil {
			return errors.New("Failed to parse signature")
		}

		hash, err := hexutil.Decode(req.RequestHash)
		if err != nil {
			return err
		}

		int64Hash := make([]int64, len(hash))
		for i, v := range hash {
			int64Hash[i] = int64(v)
		}

		reqInZK := &models.RequestResponse{
			ReqFee:           req.InputFee,
			ResFee:           req.OutputFee,
			Nonce:            req.Nonce,
			ProviderAddress:  c.contract.ProviderAddress,
			UserAddress:      req.UserAddress,
			TeeSignerAddress: c.GetProviderSignerAddress(ctx).String(),
			RequestHash:      int64Hash,
		}
		if v, ok := categorizedSettlementInfo[req.UserAddress]; ok {
			minNonce := v.MinNonceInSettlement

			cmp, err := util.Compare(minNonce, req.Nonce)
			if err != nil {
				return errors.Wrap(err, "compare nonce")
			}
			if minNonce == "0" || cmp > 0 {
				minNonce = req.Nonce
			}

			totalFeeInSettlement, err := util.Add(req.Fee, v.TotalFeeInSettlement)
			if err != nil {
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
			categorizedTeeSigs[req.UserAddress] = append(categorizedTeeSigs[req.UserAddress], teeSig)
			continue
		}

		categorizedReqs[req.UserAddress] = []*models.RequestResponse{reqInZK}
		categorizedSigs[req.UserAddress] = [][]int64{sig}
		categorizedTeeSigs[req.UserAddress] = [][]int64{teeSig}
	}

	verifierInput := contract.VerifierInput{
		InProof:     []*big.Int{},
		ProofInputs: []*big.Int{},
		NumChunks:   big.NewInt(0),
		SegmentSize: []*big.Int{},
	}
	for key := range categorizedReqs {
		reqChunks, sigChunks, teeSigChunks := splitArray(categorizedReqs[key], c.zk.RequestLength), splitArray(categorizedSigs[key], c.zk.RequestLength), splitArray(categorizedTeeSigs[key], c.zk.RequestLength)
		verifierInput.NumChunks.Add(verifierInput.NumChunks, big.NewInt(int64(len(reqChunks))))

		segmentSize := 0
		for i := range reqChunks {
			calldata, err := c.GenerateSolidityCalldata(ctx, reqChunks[i], sigChunks[i], teeSigChunks[i])
			if err != nil {
				return err
			}
			proof, err := flattenAndConvert([][]string{calldata.PA}, calldata.PB, [][]string{calldata.PC})
			if err != nil {
				return err
			}
			verifierInput.InProof = append(verifierInput.InProof, proof...)
			// proofInputs: [userAddress, providerAddress, initNonce, finalNonce, totalFee, signerPubKey[0], signerPubKey[1], TeeSignerPubKey[0], TeeSignerPubKey[1]]
			proofInputs, err := flattenAndConvert([][]string{calldata.PubInputs})
			if err != nil {
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
		c.logger.Errorf("Error marshalling settlement infos: %v", err)
		settlementInfoJSON = []byte("[]")
	}
	c.logger.Infof("Settlement infos: %s", string(settlementInfoJSON))
	if err := c.contract.SettleFees(ctx, verifierInput); err != nil {
		return errors.Wrapf(err, "settle fees in contract, the ")
	}

	if err := c.db.UpdateRequest(latestReqCreateAt); err != nil {
		return errors.Wrap(err, "update request in db")
	}
	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

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
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}
	if len(accounts) == 0 {
		return nil
	}
	// Verify the available balance in the contract.
	// If it exceeds the fee, no settlement is necessary;
	// the balance is sufficient for at least the next lock period.
	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}
	accounts, err = c.db.ListUserAccount(&model.UserListOptions{
		MinUnsettledFee:        model.PtrOf(int64(0)),
		LowBalanceRisk:         model.PtrOf(time.Now()),
		SettleTriggerThreshold: &settleTriggerThreshold,
	})
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}
	if len(accounts) == 0 {
		return nil
	}
	c.logger.Warn("Accounts at risk of having insufficient funds and will be settled immediately.")
	return errors.Wrap(c.SettleFees(ctx), "settle fees")
}

func (c *Ctrl) pruneRequest(ctx context.Context, categorizedSettlementInfo *map[string]SettlementInfo) error {
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed: false,
		Sort:      model.PtrOf("nonce ASC"),
	})
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}
	if len(reqs) == 0 {
		return nil
	}
	// accountsInDebt marks the accounts needed to be charged
	accountsInDebt := map[string]string{}
	for _, req := range reqs {
		if _, ok := accountsInDebt[req.UserAddress]; !ok {
			accountsInDebt[req.UserAddress] = "1"
		}
	}
	accounts, err := c.contract.ListUserAccount(ctx)
	if err != nil {
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
