package lora

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/0glabs/0g-serving-broker/common/log"
	ftcontract "github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
)

const defaultEventLookbackBlocks = 1000

// EventWatcher watches the FineTuningServing contract for DeliverableAcknowledged events
// and triggers adapter registration in the LoRA Manager.
type EventWatcher struct {
	manager         *Manager
	db              *db.DB
	config          config.LoRAConfig
	providerAddress common.Address
	logger          log.Logger
	client          *ethclient.Client
}

// NewEventWatcher creates a watcher that polls the FineTuningServing contract for
// DeliverableAcknowledged events and triggers adapter registration in the Manager.
func NewEventWatcher(
	manager *Manager,
	database *db.DB,
	cfg config.LoRAConfig,
	providerAddress common.Address,
	logger log.Logger,
) (*EventWatcher, error) {
	client, err := ethclient.Dial(cfg.ChainRpcUrl)
	if err != nil {
		return nil, fmt.Errorf("connect to chain RPC %s: %w", cfg.ChainRpcUrl, err)
	}

	return &EventWatcher{
		manager:         manager,
		db:              database,
		config:          cfg,
		providerAddress: providerAddress,
		logger:          logger,
		client:          client,
	}, nil
}

// Start begins polling for on-chain events. Blocks until ctx is cancelled.
func (w *EventWatcher) Start(ctx context.Context) {
	if w.config.FineTuningContractAddr == "" {
		w.logger.Warn("FineTuningServing contract address not configured, event watcher disabled")
		return
	}

	pollInterval := time.Duration(w.config.PollBlockIntervalSeconds) * time.Second
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}

	lastBlock, err := w.db.GetLastProcessedBlock()
	if err != nil {
		w.logger.Errorf("failed to get last processed block: %v", err)
	}
	if lastBlock > 0 {
		lastBlock++ // Start from next block
	}

	w.logger.Infof("event watcher starting from block %d, polling every %v", lastBlock, pollInterval)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("event watcher stopped")
			return
		case <-ticker.C:
			w.pollEvents(ctx, &lastBlock)
		}
	}
}

// Stop closes the underlying RPC client connection.
func (w *EventWatcher) Stop() {
	if w.client != nil {
		w.client.Close()
		w.logger.Info("event watcher RPC client closed")
	}
}

func (w *EventWatcher) pollEvents(ctx context.Context, fromBlock *uint64) {
	contractAddr := common.HexToAddress(w.config.FineTuningContractAddr)
	filterer, err := ftcontract.NewFineTuningServingFilterer(contractAddr, w.client)
	if err != nil {
		w.logger.Errorf("failed to create contract filterer: %v", err)
		return
	}

	currentBlock, err := w.client.BlockNumber(ctx)
	if err != nil {
		w.logger.Errorf("failed to get current block number: %v", err)
		return
	}

	if *fromBlock == 0 {
		if currentBlock > defaultEventLookbackBlocks {
			*fromBlock = currentBlock - defaultEventLookbackBlocks
		}
	}

	if *fromBlock > currentBlock {
		return
	}

	lowestFailed := w.processAcknowledgedEvents(ctx, filterer, contractAddr, *fromBlock, currentBlock)

	if lowestFailed > 0 {
		*fromBlock = lowestFailed
	} else {
		*fromBlock = currentBlock + 1
	}
}

// processAcknowledgedEvents handles DeliverableAcknowledged events in the block range.
// Returns the block number of the earliest failed event (so the watcher can retry it),
// or 0 if all events were processed successfully.
func (w *EventWatcher) processAcknowledgedEvents(
	ctx context.Context,
	filterer *ftcontract.FineTuningServingFilterer,
	contractAddr common.Address,
	startBlock, endBlock uint64,
) uint64 {
	opts := &bind.FilterOpts{
		Start:   startBlock,
		End:     &endBlock,
		Context: ctx,
	}

	// Filter for events where provider = our provider address
	providerAddrs := []common.Address{w.providerAddress}

	iter, err := filterer.FilterDeliverableAcknowledged(opts, nil, providerAddrs)
	if err != nil {
		w.logger.Errorf("failed to filter DeliverableAcknowledged events: %v", err)
		return startBlock
	}
	defer iter.Close()

	var lowestFailed uint64
	for iter.Next() {
		event := iter.Event
		w.logger.Infof("DeliverableAcknowledged event: user=%s, deliverableId=%s, block=%d",
			event.User.Hex(), event.DeliverableId, event.Raw.BlockNumber)

		rootHash, err := w.getDeliverableRootHash(ctx, contractAddr, event.User, event.DeliverableId)
		if err != nil {
			w.logger.Errorf("failed to get deliverable root hash for %s: %v", event.DeliverableId, err)
			if lowestFailed == 0 || event.Raw.BlockNumber < lowestFailed {
				lowestFailed = event.Raw.BlockNumber
			}
			continue
		}

		if err := w.manager.RegisterAdapter(
			ctx,
			event.DeliverableId,
			event.User.Hex(),
			w.manager.config.BaseModel,
			rootHash,
			event.Raw.BlockNumber,
		); err != nil {
			w.logger.Errorf("failed to register adapter for task %s: %v", event.DeliverableId, err)
			if lowestFailed == 0 || event.Raw.BlockNumber < lowestFailed {
				lowestFailed = event.Raw.BlockNumber
			}
		}
	}
	return lowestFailed
}

// getDeliverableRootHash fetches the on-chain modelRootHash for a deliverable.
// Returns the raw hex (no 0x prefix). With HTTP key sharing, this is always a pure 32-byte storage hash.
func (w *EventWatcher) getDeliverableRootHash(
	ctx context.Context,
	contractAddr common.Address,
	user common.Address,
	deliverableId string,
) (string, error) {
	caller, err := ftcontract.NewFineTuningServingCaller(contractAddr, w.client)
	if err != nil {
		return "", err
	}

	deliverable, err := caller.GetDeliverable(&bind.CallOpts{Context: ctx}, user, w.providerAddress, deliverableId)
	if err != nil {
		return "", fmt.Errorf("fetch deliverable %s from contract: %w", deliverableId, err)
	}

	return "0x" + common.Bytes2Hex(deliverable.ModelRootHash), nil
}
