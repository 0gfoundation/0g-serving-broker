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

	// Log settlements for debugging
	settlementsJSON, err := json.Marshal(settlements)
	if err != nil {
		log.Println("Error marshalling TEE settlements:", err)
	} else {
		log.Printf("TEE settlements to process: %s", string(settlementsJSON))
	}

	// Call contract with TEE signed settlements
	failedUsers, err := c.contract.SettleFeesWithTEE(ctx, settlements)
	if err != nil {
		return errors.Wrap(err, "settle fees with TEE in contract")
	}

	// Convert failed users to string slice for database query
	var failedUserStrings []string
	for _, user := range failedUsers {
		failedUserStrings = append(failedUserStrings, user.Hex())
	}

	// Log failed users for debugging
	if len(failedUsers) > 0 {
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
