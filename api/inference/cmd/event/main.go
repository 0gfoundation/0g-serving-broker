package event

import (
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
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/ethereum/go-ethereum/common"
)

func Main() {
	conf := config.GetConfig()
	logger, err := log.GetLogger(conf.Logger)
	if err != nil {
		panic(err)
	}

	if conf.Monitor.Enable {
		monitor.InitPrometheus(conf.Service.ServingURL)
		go monitor.StartMetricsServer(conf.Monitor.EventAddress)
	}

	db, err := database.NewDB(conf)
	if err != nil {
		panic(err)
	}
	contract, err := providercontract.NewProviderContract(conf, common.Address{}, logger)
	if err != nil {
		panic(err)
	}
	// contract.LockTime is a time.Duration; AutoSettleBufferTime is now also
	// a Duration. The pre-#507 checks compared integer seconds.
	if conf.Interval.AutoSettleBufferTime > contract.LockTime {
		panic(errors.New("Interval.AutoSettleBufferTime greater than refund LockTime"))
	}
	if conf.Interval.AutoSettleBufferTime > conf.Interval.ForceSettlementProcessor {
		panic(errors.New("Interval.AutoSettleBufferTime greater than forceSettlement Interval"))
	}
	if contract.LockTime-conf.Interval.AutoSettleBufferTime < time.Minute {
		panic(errors.New("Interval.AutoSettleBufferTime is too large, which could lead to overly frequent settlements"))
	}
	if conf.Interval.ForceSettlementProcessor < time.Minute {
		panic(errors.New("Interval.ForceSettlementProcessor is too small, which could lead to overly frequent settlements"))
	}

	cfg := &rest.Config{}
	mgr, err := controller.NewManager(cfg, controller.Options{
		Metrics: metricserver.Options{
			BindAddress: conf.Event.ListenAddr,
		},
	})
	if err != nil {
		panic(err)
	}

	var teeClientType tee.ClientType
	switch os.Getenv("NETWORK") {
	case "hardhat":
		teeClientType = tee.Mock
	case "gcp":
		teeClientType = tee.GCP
	case "alicloud":
		teeClientType = tee.AliCloud
	default:
		teeClientType = tee.Phala
	}
	teeService, err := tee.NewTeeService(teeClientType, logger)
	if err != nil {
		panic(err)
	}

	ctx := controller.SetupSignalHandler()
	if err := teeService.SyncQuote(ctx, false); err != nil {
		panic(err)
	}

	// The event process does not compute request fees, so it doesn't need the
	// USD price cache.  Billing happens in the server process, which owns
	// the cache and the PriceUpdateProcessor.
	ctrl := ctrl.New(db, contract, conf, nil, teeService, nil, logger)

	settlementProcessor := event.NewSettlementProcessor(ctrl, conf.Interval.SettlementProcessor, conf.Interval.ForceSettlementProcessor, conf.Monitor.Enable, logger)
	if err := mgr.Add(settlementProcessor); err != nil {
		panic(err)
	}

	// Add reconciliation processor for event-based settlement verification
	if conf.Interval.ReconciliationProcessor > 0 {
		reconciliationProcessor := event.NewReconciliationProcessor(db, contract, conf.Interval.ReconciliationProcessor, logger)
		if err := mgr.Add(reconciliationProcessor); err != nil {
			panic(err)
		}
		logger.Infof("Starting reconciliation processor: interval=%s", conf.Interval.ReconciliationProcessor)
	}

	// Add revenue transfer processor if configured
	if conf.RevenueTransfer.Interval > 0 && conf.RevenueTransfer.TargetAddress != "" {
		revenueTransferProcessor, err := event.NewRevenueTransferProcessor(
			contract,
			conf.RevenueTransfer.TargetAddress,
			conf.RevenueTransfer.ReserveAmount,
			conf.RevenueTransfer.Interval,
			logger,
		)
		if err != nil {
			panic(err)
		}
		if revenueTransferProcessor != nil {
			logger.Infof("Starting revenue transfer processor: target=%s, interval=%s, reserve=%s",
				conf.RevenueTransfer.TargetAddress,
				conf.RevenueTransfer.Interval,
				conf.RevenueTransfer.ReserveAmount,
			)
			if err := mgr.Add(revenueTransferProcessor); err != nil {
				panic(err)
			}
		}
	}

	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
