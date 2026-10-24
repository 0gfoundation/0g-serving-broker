package event

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
	"k8s.io/client-go/rest"
	controller "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
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

	extras, err := config.LoadInstances(conf)
	if err != nil {
		panic(err)
	}

	// One metrics server for the process, on the primary's address, whenever any
	// instance exports metrics.
	monitorOn := conf.Monitor.Enable
	for _, extra := range extras {
		monitorOn = monitorOn || extra.Config.Monitor.Enable
	}
	if monitorOn {
		go monitor.StartMetricsServer(conf.Monitor.EventAddress)
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

	teeClientType, err := tee.ClientTypeForNetwork(
		context.Background(), os.Getenv("NETWORK"), conf.Network.ChainID, conf.Network.URL)
	if err != nil {
		panic(err)
	}
	teeService, err := tee.NewTeeService(teeClientType, logger)
	if err != nil {
		panic(err)
	}

	ctx := controller.SetupSignalHandler()
	if err := teeService.SyncQuote(ctx, false); err != nil {
		panic(err)
	}

	addInstance(mgr, "", conf, teeService, logger)
	for _, extra := range extras {
		addInstance(mgr, extra.Name, extra.Config, teeService, logger)
	}

	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}

// addInstance builds one provider instance's settlement, reconciliation and
// revenue-transfer processors and adds them to mgr. name is "" for the primary
// (CONFIG_FILE) instance and the extra instance's name otherwise. The TEE
// service is the process's and is shared by every instance.
func addInstance(mgr manager.Manager, name string, conf *config.Config, teeService *tee.TeeService, logger log.Logger) {
	if name != "" {
		logger = logger.WithFields(logrus.Fields{"instance": name})
	}

	var metrics *monitor.EventMetrics
	if conf.Monitor.Enable {
		metrics = monitor.NewEventMetrics(conf.Service.ServingURL, prometheus.DefaultRegisterer)
	}

	db, err := openDatabase(conf, name, logger)
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

	// The event process does not compute request fees, so it doesn't need the
	// USD price cache.  Billing happens in the server process, which owns
	// the cache and the PriceUpdateProcessor.
	ctrl := ctrl.New(db, contract, conf, nil, teeService, nil, logger)

	settlementProcessor := event.NewSettlementProcessor(ctrl, conf.Interval.SettlementProcessor, conf.Interval.ForceSettlementProcessor, metrics, logger)
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
			metrics,
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
}

// extraDatabaseWait bounds how long the event process waits for the broker to
// create an extra instance's database.
const extraDatabaseWait = 5 * time.Minute

// openDatabase opens the instance's database. The primary's is opened at once,
// as always. An extra instance's database is created and migrated by the broker
// — the one process that does either, so the two never race on a migration — and
// on a first deployment the event process can start before the broker has made
// it, so it waits for it to appear instead of failing or creating an empty one.
func openDatabase(conf *config.Config, name string, logger log.Logger) (*database.DB, error) {
	if name == "" {
		return database.NewDB(conf)
	}
	deadline := time.Now().Add(extraDatabaseWait)
	for {
		db, err := database.NewDB(conf)
		if err == nil {
			return db, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("instance %s: its database did not become reachable within %s (the broker creates it on start): %w", name, extraDatabaseWait, err)
		}
		logger.Infof("waiting for the broker to create this instance's database: %v", err)
		time.Sleep(5 * time.Second)
	}
}
