package ctrl

import (
	"context"
	"encoding/json"
	"log"
	"math/big"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/util"
	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func (c *Ctrl) SettleFeesWithTEE(ctx context.Context) error {
	// Get unprocessed requests
	reqs, _, err := c.db.ListRequest(model.RequestListOptions{
		Processed: false,
		Sort:      model.PtrOf("created_at ASC"),
	})
	if err != nil {
		return errors.Wrap(err, "list request from db")
	}
	if len(reqs) == 0 {
		return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
	}

	// Group requests by user
	type UserRequests struct {
		Requests []*model.Request
		TotalFee *big.Int
	}
	userRequestsMap := make(map[string]*UserRequests)
	latestReqCreateAt := reqs[0].CreatedAt

	for _, req := range reqs {
		if latestReqCreateAt.Before(*req.CreatedAt) {
			latestReqCreateAt = req.CreatedAt
		}

		// Parse fee to big.Int
		fee, err := util.HexadecimalStringToBigInt(req.Fee)
		if err != nil {
			return errors.Wrap(err, "parse fee")
		}

		if userReqs, exists := userRequestsMap[req.UserAddress]; exists {
			// Add to existing user's requests
			userReqs.Requests = append(userReqs.Requests, &req)
			userReqs.TotalFee = new(big.Int).Add(userReqs.TotalFee, fee)
		} else {
			// Create new entry for user
			userRequestsMap[req.UserAddress] = &UserRequests{
				Requests: []*model.Request{&req},
				TotalFee: fee,
			}
		}
	}

	// Create settlements for each user
	var settlements []contract.TEESettlementData
	for userAddr, userReqs := range userRequestsMap {
		// Create hash of all requests for this user
		requestsHash := c.hashUserRequests(userReqs.Requests)

		// Generate nonce based on timestamp
		nonce := big.NewInt(time.Now().Unix())

		settlementData := contract.TEESettlementData{
			User:         common.HexToAddress(userAddr),
			Provider:     common.HexToAddress(c.contract.ProviderAddress),
			TotalFee:     userReqs.TotalFee,
			RequestsHash: requestsHash,
			Nonce:        nonce,
		}

		// Create message hash for signing (matching Solidity order)
		messageHash := crypto.Keccak256(
			requestsHash[:],
			common.LeftPadBytes(nonce.Bytes(), 32),
			settlementData.Provider.Bytes(),
			settlementData.User.Bytes(),
			common.LeftPadBytes(userReqs.TotalFee.Bytes(), 32),
		)

		// Sign with TEE service
		signature, err := c.teeService.Sign(messageHash)
		if err != nil {
			return errors.Wrap(err, "TEE signing failed")
		}

		settlementData.Signature = signature
		settlements = append(settlements, settlementData)
	}

	// Log total settlements for debugging
	log.Printf("Total TEE settlements to process: %d users", len(settlements))

	// Process settlements in batches to avoid gas limit issues
	var allFailedUsers []common.Address
	
	for i := 0; i < len(settlements); i += constant.TEESettlementBatchSize {
		// Calculate the end index for this batch
		end := i + constant.TEESettlementBatchSize
		if end > len(settlements) {
			end = len(settlements)
		}
		
		// Get the current batch
		batch := settlements[i:end]
		
		// Log batch for debugging
		batchJSON, err := json.Marshal(batch)
		if err != nil {
			log.Printf("Error marshalling TEE settlements batch %d-%d: %v", i, end-1, err)
		} else {
			log.Printf("Processing TEE settlements batch %d-%d (users %d-%d of %d): %s", 
				i/constant.TEESettlementBatchSize+1, (end-1)/constant.TEESettlementBatchSize+1, i+1, end, len(settlements), string(batchJSON))
		}
		
		// Call contract with the current batch of TEE signed settlements
		failedUsers, err := c.contract.SettleFeesWithTEE(ctx, batch)
		if err != nil {
			return errors.Wrapf(err, "settle fees with TEE in contract for batch %d-%d", i, end-1)
		}
		
		// Accumulate failed users from this batch
		allFailedUsers = append(allFailedUsers, failedUsers...)
		
		// Log progress
		log.Printf("Completed batch %d-%d: %d failed users", i+1, end, len(failedUsers))
	}
	
	// Convert all failed users to string slice for database query
	var failedUserStrings []string
	for _, user := range allFailedUsers {
		failedUserStrings = append(failedUserStrings, user.Hex())
	}

	// Log failed users for debugging
	if len(allFailedUsers) > 0 {
		log.Printf("Settlement failed for users: %v", failedUserStrings)
	}

	// Delete settled requests from database, excluding failed users
	if err := c.db.DeleteSettledRequestsExcludingUsers(latestReqCreateAt, failedUserStrings); err != nil {
		return errors.Wrap(err, "delete settled requests from db")
	}

	if err := c.SyncUserAccounts(ctx); err != nil {
		return errors.Wrap(err, "synchronize accounts from the contract to the database")
	}

	return errors.Wrap(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
}

func (c *Ctrl) hashUserRequests(requests []*model.Request) [32]byte {
	// Create a deterministic hash of all requests for a user
	var requestData []byte
	for _, req := range requests {
		// Concatenate request data: RequestHash + UserAddress + Fee + InputFee + OutputFee
		requestData = append(requestData, []byte(req.RequestHash)...)
		requestData = append(requestData, []byte(req.UserAddress)...)
		requestData = append(requestData, []byte(req.Fee)...)
		requestData = append(requestData, []byte(req.InputFee)...)
		requestData = append(requestData, []byte(req.OutputFee)...)
	}
	return crypto.Keccak256Hash(requestData)
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

	// Verify the available balance in the contract
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

	log.Print("Accounts at risk of having insufficient funds and will be settled immediately with TEE.")
	return errors.Wrap(c.SettleFeesWithTEE(ctx), "settle fees with TEE")
}
