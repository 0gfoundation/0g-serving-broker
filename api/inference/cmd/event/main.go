package event

import (
	"context"
	"os"
	"time"

	"k8s.io/client-go/rest"
	controller "sigs.k8s.io/controller-runtime"
	metricserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/event"
	"github.com/0glabs/0g-serving-broker/inference/internal/signer"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
	"github.com/sirupsen/logrus"
)

func Main() {
	conf := config.GetConfig()

	logger, err := log.GetLogger(&conf.Logger)
	if err != nil {
		panic(err)
	}
	logger = logger.WithFields(logrus.Fields{"name": "inference-event"})

	if conf.Monitor.Enable {
		monitor.InitPrometheus(conf.Service.ServingURL)
		go monitor.StartMetricsServer(conf.Monitor.EventAddress)
	}

	db, err := database.NewDB(conf)
	if err != nil {
		logger.Errorf("Failed to initialize database: %v", err)
		panic(err)
	}
	contract, err := providercontract.NewProviderContract(conf)
	if err != nil {
		logger.Errorf("Failed to initialize contract: %v", err)
		panic(err)
	}
	if conf.Interval.AutoSettleBufferTime > int(contract.LockTime) {
		panic(errors.New("Interval.AutoSettleBufferTime greater than refund LockTime"))
	}
	if conf.Interval.AutoSettleBufferTime > conf.Interval.ForceSettlementProcessor {
		err := errors.New("Interval.AutoSettleBufferTime greater than forceSettlement Interval")
		logger.Errorf("%v", err)
		panic(err)
	}
	if int(contract.LockTime)-conf.Interval.AutoSettleBufferTime < 60 {
		err := errors.New("Interval.AutoSettleBufferTime is too large, which could lead to overly frequent settlements")
		logger.Errorf("%v", err)
		panic(err)
	}
	if conf.Interval.ForceSettlementProcessor < 60 {
		err := errors.New("Interval.ForceSettlementProcessor is too small, which could lead to overly frequent settlements")
		logger.Errorf("%v", err)
		panic(err)
	}

	cfg := &rest.Config{}
	mgr, err := controller.NewManager(cfg, controller.Options{
		Metrics: metricserver.Options{
			BindAddress: conf.Event.ProviderAddr,
		},
	})
	if err != nil {
		logger.Errorf("Failed to initialize controller manager: %v", err)
		panic(err)
	}

	zk := zkclient.NewZKClient(conf.ZKSettlement.Provider, conf.ZKSettlement.RequestLength)
	var teeClientType tee.ClientType
	switch os.Getenv("NETWORK") {
	case "hardhat":
		teeClientType = tee.Mock
	default:
		teeClientType = tee.Phala
	}

	teeService, err := tee.NewTeeService(teeClientType)
	if err != nil {
		logger.Errorf("Failed to initialize TEE service: %v", err)
		panic(err)
	}

	ctx := controller.SetupSignalHandler()

	if err := teeService.SyncQuote(ctx); err != nil {
		logger.Errorf("Failed to sync TEE quote: %v", err)
		panic(err)
	}

	signer, _ := signer.NewSigner()
	encryptedKey, err := signer.InitialKey(ctx, contract, zk, teeService.ProviderSigner)
	if err != nil {
		logger.Errorf("Failed to initialize signer: %v", err)
		panic(err)
	}
	contract.EncryptedPrivKey = encryptedKey

	ctrl := ctrl.New(db, contract, zk, conf, nil, teeService, signer, logger)

	settlementProcessor := event.NewSettlementProcessor(ctrl, logger, conf.Interval.SettlementProcessor, conf.Interval.ForceSettlementProcessor, conf.Monitor.Enable)
	if err := mgr.Add(settlementProcessor); err != nil {
		logger.Errorf("Failed to add settlement processor: %v", err)
		panic(err)
	}

	if err := mgr.Start(ctx); err != nil {
		logger.Errorf("Failed to start manager: %v", err)
		panic(err)
	}
}
