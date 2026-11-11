package providercontract

import (
	"context"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

// The two payee-list calls of the assay payout section, declared here because
// the generated binding predates them.
const assayNodeABIJSON = `[
 {"name":"isAssayNode","type":"function","stateMutability":"view",
  "inputs":[{"name":"provider","type":"address"},{"name":"node","type":"address"}],
  "outputs":[{"name":"","type":"bool"}]},
 {"name":"allowAssayNode","type":"function","stateMutability":"nonpayable",
  "inputs":[{"name":"node","type":"address"}],"outputs":[]}]`

// EnsureAssayNode lists node as a payee of this provider's assay pool unless
// it already is one. claim() pays only listed addresses, and listing is
// permanent, so this runs once per payout address. It reports whether a
// transaction was sent.
func (c *ProviderContract) EnsureAssayNode(ctx context.Context, node common.Address) (bool, error) {
	parsed, err := abi.JSON(strings.NewReader(assayNodeABIJSON))
	if err != nil {
		return false, err
	}
	cl := c.Contract.Client.Client
	bound := bind.NewBoundContract(common.HexToAddress(c.ContractAddress), parsed, cl, cl, cl)

	var out []interface{}
	if err := bound.Call(&bind.CallOpts{Context: ctx}, &out, "isAssayNode", common.HexToAddress(c.ProviderAddress), node); err != nil {
		return false, errors.Wrap(WrapContractError(err), "call isAssayNode")
	}
	if listed, _ := out[0].(bool); listed {
		return false, nil
	}

	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return false, err
	}
	opts.Context = ctx
	opts.GasLimit = 0 // estimate, as ServingContract.Transact does
	tx, err := bound.Transact(opts, "allowAssayNode", node)
	if err != nil {
		return false, errors.Wrap(WrapContractError(err), "call allowAssayNode")
	}
	receipt, err := c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		return true, errors.Wrapf(err, "wait for allowAssayNode receipt (txHash=%s)", tx.Hash().Hex())
	}
	return true, receiptError(receipt)
}
