package server

import (
	"context"
	"os"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"

	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/handler"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
)

//go:generate swag fmt
//go:generate swag init --dir ./,../../ --output ../../doc

//	@title			0G Serving Provider Broker API
//	@version		0.1.0
//	@description	These APIs allow providers to manage services and user accounts. The host is localhost, and the port is configured in the provider's configuration file, defaulting to 3080.
//	@host			localhost:3080
//	@BasePath		/v1
//	@in				header

func Main() {
	// Initialize logger
	logger, err := log.GetLogger(&config.GetConfig().Logger)
	if err != nil {
		panic("Failed to initialize logger: " + err.Error())
	}
	logger.Info("Starting inference service")

	config := config.GetConfig()

	db, err := database.NewDB(config, logger)
	if err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to initialize database")
		panic(err)
	}
	if err := db.Migrate(); err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to migrate database")
		panic(err)
	}

	contract, err := providercontract.NewProviderContract(config, logger)
	if err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to initialize provider contract")
		panic(err)
	}
	defer contract.Close()

	zk := zkclient.NewZKClient(config.ZKProver.Provider, config.ZKProver.RequestLength)
	zkClient := &zk // Convert to pointer type

	engine := gin.New()

	if config.Monitor.Enable {
		monitor.PrometheusInit(config.Service.ServingURL)
		engine.GET("/metrics", gin.WrapH(promhttp.Handler()))
		logger.Info("Prometheus monitoring enabled")
	}

	svcCache := cache.New(5*time.Minute, 10*time.Minute)
	phalaClientType := phala.TEE
	if os.Getenv("NETWORK") == "hardhat" {
		phalaClientType = phala.Mock
		logger.Info("Using mock Phala client for hardhat network")
	}

	phalaService, err := phala.NewPhalaService(phalaClientType)
	if err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to initialize Phala service")
		panic(err)
	}

	ctrl := ctrl.New(db, contract, zkClient, config.Service, config.Interval.AutoSettleBufferTime, svcCache, phalaService, logger)
	ctx := context.Background()

	logger.Info("Starting initial service synchronization")
	if err := ctrl.SyncUserAccounts(ctx); err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to sync user accounts")
		panic(err)
	}

	logger.Info("Starting initial fee settlement")
	settleFeesErr := ctrl.SettleFees(ctx)
	if settleFeesErr != nil {
		logger.WithFields(logrus.Fields{"error": settleFeesErr}).Error("Failed to settle fees")
		panic(settleFeesErr)
	}

	if err := ctrl.SyncService(ctx); err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to sync service")
		panic(err)
	}

	proxy := proxy.New(ctrl, engine, config.AllowOrigins, config.Monitor.Enable, logger)
	if err := proxy.Start(); err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to start proxy")
		panic(err)
	}

	h := handler.New(ctrl, proxy, logger)
	h.Register(engine)

	logger.WithFields(logrus.Fields{
		"port": os.Getenv("PORT"),
	}).Info("Starting HTTP server")

	// Listen and Serve, config port with PORT=X
	if err := engine.Run(); err != nil {
		logger.WithFields(logrus.Fields{"error": err}).Error("Failed to start HTTP server")
		panic(err)
	}
}
