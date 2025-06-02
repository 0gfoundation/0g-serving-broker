package providercontract

import (
	"context"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/sirupsen/logrus"
)

type ProviderContract struct {
	contract *Contract
	logger   log.Logger
}

func NewProviderContract(conf *config.Config, logger log.Logger) (*ProviderContract, error) {
	contract, err := NewContract(conf, logger)
	if err != nil {
		return nil, err
	}
	return &ProviderContract{
		contract: contract,
		logger:   logger,
	}, nil
}

func (c *ProviderContract) Close() {
	c.contract.Close()
}

func (c *ProviderContract) GetProviderAddress() string {
	return c.contract.GetProviderAddress()
}

func (c *ProviderContract) GetLockTime(ctx context.Context) (uint64, error) {
	lockTime, err := c.contract.GetLockTime(ctx)
	if err != nil {
		c.logger.WithFields(logrus.Fields{"error": err}).Error("Failed to get lock time")
		return 0, err
	}
	return lockTime, nil
}

func (c *ProviderContract) ListUserAccount(ctx context.Context) ([]*Account, error) {
	accounts, err := c.contract.ListUserAccount(ctx)
	if err != nil {
		c.logger.WithFields(logrus.Fields{"error": err}).Error("Failed to list user accounts")
		return nil, err
	}
	return accounts, nil
}

func (c *ProviderContract) SettleFees(ctx context.Context, verifierInput *VerifierInput) error {
	if err := c.contract.SettleFees(ctx, verifierInput); err != nil {
		c.logger.WithFields(logrus.Fields{"error": err}).Error("Failed to settle fees")
		return err
	}
	return nil
}
