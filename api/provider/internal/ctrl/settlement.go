package ctrl

import (
	"context"
	"encoding/json"
	"log"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/util"
	"github.com/0glabs/0g-serving-agent/common/zkclient/models"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) SettleFees(ctx context.Context) error {
	err := c.pruneRequest(ctx)
	if err != nil {
		return errors.Wrap(err, "prune request")
	}
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed: false,
	})
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}
	if len(reqs) == 0 {
		return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
	}

	categorizedReqs := make(map[string][]*models.Request)
	categorizedSigs := make(map[string][][]int64)
	for _, req := range reqs {
		var sig []int64
		err := json.Unmarshal([]byte(req.Signature), &sig)
		if err != nil {
			return errors.New("Failed to parse signature")
		}

		reqInZK := &models.Request{
			Fee:             req.Fee,
			Nonce:           req.Nonce,
			ProviderAddress: c.contract.ProviderAddress,
			UserAddress:     req.UserAddress,
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
				return err
			}
			// ProofInputs: [userAddress, providerAddress, initNonce, finalNonce, totalFee, signerPubKey[0], signerPubKey[1]]
			verifierInput.InProof, err = flattenAndConvert([][]string{calldata.PA}, calldata.PB, [][]string{calldata.PC})
			if err != nil {
				return err
			}
			verifierInput.ProofInputs, err = flattenAndConvert([][]string{calldata.PubInputs})
			if err != nil {
				return err
			}
			segmentSize += len(verifierInput.ProofInputs)
		}
		verifierInput.SegmentSize = append(verifierInput.SegmentSize, big.NewInt(int64(segmentSize)))
	}

	if err := c.contract.SettleFees(ctx, verifierInput); err != nil {
		return errors.Wrap(err, "settle fees in contract")
	}
	if err := c.db.UpdateRequest(); err != nil {
		return errors.Wrap(err, "update request in db")
	}
	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

	return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
}

func (c Ctrl) ProcessSettlement(ctx context.Context) error {
	accounts, err := c.db.ListUserAccount(&model.UserListOptions{
		LowBalanceRisk:  model.PtrOf(time.Now().Add(-c.contract.LockTime - c.autoSettleBufferTime)),
		MinUnsettledFee: model.PtrOf(int64(0)),
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
		MinUnsettledFee: model.PtrOf(int64(0)),
		LowBalanceRisk:  model.PtrOf(time.Now()),
	})
	if err != nil {
		return errors.Wrap(err, "list accounts that need to be settled in db")
	}
	if len(accounts) == 0 {
		return nil
	}
	log.Print("Accounts at risk of having insufficient funds and will be settled immediately.")
	return errors.Wrap(c.SettleFees(ctx), "settle fees")
}

func (c Ctrl) pruneRequest(ctx context.Context) error {
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed: false,
	})
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}
	if len(reqs) == 0 {
		return nil
	}
	accountsInDebt := map[string]int64{}
	for _, req := range reqs {
		if _, ok := accountsInDebt[req.UserAddress]; !ok {
			accountsInDebt[req.UserAddress] = 1
		}
	}
	accounts, err := c.contract.ListUserAccount(ctx)
	if err != nil {
		return errors.Wrap(err, "list account from contract")
	}
	for _, account := range accounts {
		if _, ok := accountsInDebt[account.User.String()]; ok {
			accountsInDebt[account.User.String()] = account.Nonce.Int64()
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
