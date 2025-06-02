package ctrl

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
)

type Ctrl struct {
	db                   *db.DB
	contract             *providercontract.ProviderContract
	zk                   *zkclient.ZKClient
	service              config.Service
	autoSettleBufferTime time.Duration
	svcCache             *cache.Cache
	phalaService         *phala.PhalaService
	logger               log.Logger

	mutex sync.Mutex
}

func New(db *db.DB, contract *providercontract.ProviderContract, zk *zkclient.ZKClient, service config.Service, autoSettleBufferTime int, svcCache *cache.Cache, phala *phala.PhalaService, logger log.Logger) *Ctrl {
	return &Ctrl{
		db:                   db,
		contract:             contract,
		zk:                   zk,
		service:              service,
		autoSettleBufferTime: time.Duration(autoSettleBufferTime) * time.Second,
		svcCache:             svcCache,
		phalaService:         phala,
		logger:               logger,
	}
}

func (c *Ctrl) handleBrokerError(ctx *gin.Context, err error, context string) {
	info := "Provider proxy"
	if context != "" {
		info += (": " + context)
	}
	c.logger.WithFields(logrus.Fields{
		"error":   err.Error(),
		"context": context,
	}).Error(info)
	errors.Response(ctx, errors.Wrap(err, info))
}

func (c *Ctrl) handleServiceError(ctx *gin.Context, body io.ReadCloser) {
	respBody, err := io.ReadAll(body)
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("Failed to read response body")
		return
	}
	if _, err := ctx.Writer.Write(respBody); err != nil {
		c.logger.WithFields(logrus.Fields{
			"error": err.Error(),
		}).Error("Failed to write response body")
	}
}

func (c *Ctrl) handleError(err error, context string) error {
	if err != nil {
		c.logger.WithFields(logrus.Fields{
			"error":   err,
			"context": context,
		}).Error("Operation failed")
	}
	return err
}

func (c *Ctrl) SyncUserAccounts(ctx context.Context) error {
	accounts, err := c.contract.ListUserAccount(ctx)
	if err != nil {
		return c.handleError(err, "list user accounts from contract")
	}

	for _, account := range accounts {
		if err := c.db.UpsertUserAccount(account); err != nil {
			return c.handleError(err, "upsert user account")
		}
	}

	return nil
}

func (c *Ctrl) SyncService(ctx context.Context) error {
	services, err := c.db.ListService(nil)
	if err != nil {
		return c.handleError(err, "list services from db")
	}

	for _, service := range services {
		if err := c.db.UpdateServiceProgress(service.ID, service.Progress, service.Progress); err != nil {
			return c.handleError(err, "update service progress")
		}
	}

	return nil
}

func (c *Ctrl) SettleFees(ctx context.Context) error {
	// ... existing code ...
	if err := c.contract.SettleFees(ctx, verifierInput); err != nil {
		return c.handleError(err, "settle fees in contract")
	}

	if err := c.db.UpdateRequest(latestReqCreateAt); err != nil {
		return c.handleError(err, "update request in db")
	}

	if err := c.SyncUserAccounts(ctx); err != nil {
		return c.handleError(err, "sync user accounts after settlement")
	}

	return c.handleError(c.db.ResetUnsettledFee(), "reset unsettled fee in db")
}

// GetService returns the service configuration
func (c *Ctrl) GetService() config.Service {
	return c.service
}

// GetOutputPrice returns the service output price
func (c *Ctrl) GetOutputPrice() int64 {
	return c.service.OutputPrice
}
