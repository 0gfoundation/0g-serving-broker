package event

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/common/log"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
)

type RevenueTransferProcessor struct {
	contract       *providercontract.ProviderContract
	logger         log.Logger
	targetAddress  common.Address
	reserveAmount  *big.Int
	transferInterval int
}

func NewRevenueTransferProcessor(
	contract *providercontract.ProviderContract,
	targetAddress string,
	reserveAmount string,
	transferInterval int,
	logger log.Logger,
) (*RevenueTransferProcessor, error) {
	if !common.IsHexAddress(targetAddress) {
		return nil, nil // Return nil if target address is not configured
	}

	reserve, ok := new(big.Int).SetString(reserveAmount, 10)
	if !ok {
		// Default to 10 0G if parsing fails
		reserve = new(big.Int).Mul(big.NewInt(10), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	}

	return &RevenueTransferProcessor{
		contract:         contract,
		logger:           logger,
		targetAddress:    common.HexToAddress(targetAddress),
		reserveAmount:    reserve,
		transferInterval: transferInterval,
	}, nil
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (r *RevenueTransferProcessor) Start(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(r.transferInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			r.handleTransfer(ctx)
		}
	}
}

func (r *RevenueTransferProcessor) handleTransfer(ctx context.Context) {
	// Get current balance
	balance, err := r.contract.GetBalance(ctx)
	if err != nil {
		r.logger.Errorf("Failed to get balance: %s", err.Error())
		return
	}

	r.logger.Infof("Current balance: %s, Reserve amount: %s", balance.String(), r.reserveAmount.String())

	// Calculate transfer amount (balance - reserve)
	transferAmount := new(big.Int).Sub(balance, r.reserveAmount)
	if transferAmount.Cmp(big.NewInt(0)) <= 0 {
		r.logger.Infof("Balance (%s) is not greater than reserve amount (%s), skipping transfer", balance.String(), r.reserveAmount.String())
		return
	}

	r.logger.Infof("Transferring %s to %s", transferAmount.String(), r.targetAddress.Hex())

	// Perform transfer
	txHash, err := r.contract.TransferNative(ctx, r.targetAddress, transferAmount)
	if err != nil {
		r.logger.Errorf("Failed to transfer revenue: %s", err.Error())
		return
	}

	r.logger.Infof("Revenue transfer successful, tx hash: %s", txHash.Hex())

	// Record revenue transfer for monitoring
	monitor.RecordRevenueTransfer(transferAmount)
}
