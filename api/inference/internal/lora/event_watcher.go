package lora

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/0glabs/0g-serving-broker/common/log"
	ftcontract "github.com/0glabs/0g-serving-broker/fine-tuning/contract"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
)

// EventWatcher watches the FineTuningServing contract for DeliverableAcknowledged events
// and triggers adapter registration in the LoRA Manager.
type EventWatcher struct {
	manager         *Manager
	db              *db.DB
	config          config.LoRAConfig
	providerAddress common.Address
	logger          log.Logger
}

func NewEventWatcher(
	manager *Manager,
	database *db.DB,
	cfg config.LoRAConfig,
	providerAddress common.Address,
	logger log.Logger,
) *EventWatcher {
	return &EventWatcher{
		manager:         manager,
		db:              database,
		config:          cfg,
		providerAddress: providerAddress,
		logger:          logger,
	}
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

func (w *EventWatcher) pollEvents(ctx context.Context, fromBlock *uint64) {
	client, err := ethclient.DialContext(ctx, w.config.ChainRpcUrl)
	if err != nil {
		w.logger.Errorf("failed to connect to chain RPC: %v", err)
		return
	}
	defer client.Close()

	contractAddr := common.HexToAddress(w.config.FineTuningContractAddr)
	filterer, err := ftcontract.NewFineTuningServingFilterer(contractAddr, client)
	if err != nil {
		w.logger.Errorf("failed to create contract filterer: %v", err)
		return
	}

	currentBlock, err := client.BlockNumber(ctx)
	if err != nil {
		w.logger.Errorf("failed to get current block number: %v", err)
		return
	}

	if *fromBlock == 0 {
		// Start from a recent block if no checkpoint
		if currentBlock > 1000 {
			*fromBlock = currentBlock - 1000
		}
	}

	if *fromBlock > currentBlock {
		return
	}

	w.processAcknowledgedEvents(ctx, filterer, client, contractAddr, *fromBlock, currentBlock)

	*fromBlock = currentBlock + 1
}

func (w *EventWatcher) processAcknowledgedEvents(
	ctx context.Context,
	filterer *ftcontract.FineTuningServingFilterer,
	client *ethclient.Client,
	contractAddr common.Address,
	startBlock, endBlock uint64,
) {
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
		return
	}
	defer iter.Close()

	for iter.Next() {
		event := iter.Event
		w.logger.Infof("DeliverableAcknowledged event: user=%s, deliverableId=%s, block=%d",
			event.User.Hex(), event.DeliverableId, event.Raw.BlockNumber)

		// Fetch the deliverable details to get the ModelRootHash
		rootHash, err := w.getDeliverableRootHash(ctx, client, contractAddr, event.User, event.DeliverableId)
		if err != nil {
			w.logger.Errorf("failed to get deliverable root hash for %s: %v", event.DeliverableId, err)
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
		}
	}
}

// getDeliverableRootHash fetches the on-chain modelRootHash for a deliverable.
// Returns the raw hex (no 0x prefix) which may be >32 bytes if it contains the
// provider-encrypted AES key appended after the storage hash.
func (w *EventWatcher) getDeliverableRootHash(
	ctx context.Context,
	client *ethclient.Client,
	contractAddr common.Address,
	user common.Address,
	deliverableId string,
) (string, error) {
	caller, err := ftcontract.NewFineTuningServingCaller(contractAddr, client)
	if err != nil {
		return "", err
	}

	deliverable, err := caller.GetDeliverable(&bind.CallOpts{Context: ctx}, user, w.providerAddress, deliverableId)
	if err != nil {
		w.logger.Warnf("could not fetch deliverable details, using deliverableId as reference: %v", err)
		return deliverableId, nil
	}

	rawHex := common.Bytes2Hex(deliverable.ModelRootHash)
	if len(deliverable.ModelRootHash) > 32 {
		w.logger.Infof("deliverable %s has combined modelRootHash (%d bytes: 32-byte storage hash + %d-byte provider key)",
			deliverableId, len(deliverable.ModelRootHash), len(deliverable.ModelRootHash)-32)
	}

	return rawHex, nil
}
