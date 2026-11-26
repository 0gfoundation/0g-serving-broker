package providercontract

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
)

// A reverted settlement tx has no TEESettlementResult logs. Parsing it like a
// success would read every user as SUCCESS and delete + accrue rows that were
// never charged on chain, so the wrapper must refuse the receipt instead.
func TestReceiptErrorRefusesRevertedReceipt(t *testing.T) {
	ok := &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: big.NewInt(1)}
	if err := receiptError(ok); err != nil {
		t.Fatalf("successful receipt: %v", err)
	}
	bad := &types.Receipt{Status: types.ReceiptStatusFailed, BlockNumber: big.NewInt(1)}
	err := receiptError(bad)
	if !errors.Is(err, ErrTxReverted) {
		t.Fatalf("failed receipt: got %v, want ErrTxReverted", err)
	}
}
