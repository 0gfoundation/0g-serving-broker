package usercontract

import (
	"context"
	"math/big"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference-router/contract"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

func (c *UserContract) CreateProviderAccount(ctx context.Context, provider common.Address, balance big.Int, signer [2]*big.Int, settleSignerEncryptedPrivateKey string) error {
	account, _ := c.GetProviderAccount(ctx, provider)
	zeroAddress := common.Address{}
	if account.User != zeroAddress {
		return errors.New("account already exists")
	}
	return c.AddAccount(ctx, provider, balance, signer, settleSignerEncryptedPrivateKey)
}

func (c *UserContract) GetProviderAccount(ctx context.Context, provider common.Address) (contract.Account, error) {
	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	return c.Contract.GetAccount(callOpts, common.HexToAddress(c.UserAddress), provider)
}

func (c *UserContract) ListProviderAccount(ctx context.Context) ([]contract.Account, error) {
	callOpts := &bind.CallOpts{
		Context: context.Background(),
	}
	accounts, err := c.Contract.GetAllAccounts(callOpts)
	if err != nil {
		return nil, err
	}
	ret := []contract.Account{}
	for i := range accounts {
		if accounts[i].User.String() != c.UserAddress {
			continue
		}
		ret = append(ret, accounts[i])
	}
	return ret, nil
}

func (c *UserContract) AddAccount(ctx context.Context, provider common.Address, balance big.Int, signer [2]*big.Int, settleSignerEncryptedPrivateKey string) error {
	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}

	opts.Value = &balance
	tx, err := c.Contract.AddAccount(opts, provider, signer, settleSignerEncryptedPrivateKey)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return err
}

func (c *UserContract) DepositFund(ctx context.Context, provider common.Address, balance big.Int) error {
	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}

	opts.Value = &balance
	tx, err := c.Contract.DepositFund(opts, provider)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return err
}

func (c *UserContract) RequestRefund(ctx context.Context, provider common.Address, refund *big.Int) (*contract.ServingRefundRequested, error) {
	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return nil, err
	}
	tx, err := c.Contract.RequestRefund(opts, provider, refund)
	if err != nil {
		return nil, err
	}
	receipt, err := c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		return nil, err
	}

	return c.Contract.Serving.ParseRefundRequested(*receipt.Logs[0])
}

func (c *UserContract) ProcessRefund(ctx context.Context, provider common.Address, indices []*big.Int) error {
	opts, err := c.Contract.CreateTransactOpts()
	if err != nil {
		return err
	}
	tx, err := c.Contract.ProcessRefund(opts, provider, indices)
	if err != nil {
		return err
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	return err
}
