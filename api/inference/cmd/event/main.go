package event

import (
	"k8s.io/client-go/rest"
	controller "sigs.k8s.io/controller-runtime"
	metricserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/event"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
)

func Main() {
	conf := config.GetConfig()

	if conf.Monitor.Enable {
		monitor.InitPrometheus()
		go monitor.StartMetricsServer(conf.Monitor.EventAddress)
	}

	db, err := database.NewDB(conf)
	if err != nil {
		panic(err)
	}
	contract, err := providercontract.NewProviderContract(conf)
	if err != nil {
		panic(err)
	}
	if conf.Interval.AutoSettleBufferTime > int(contract.LockTime) {
		panic(errors.New("Interval.AutoSettleBufferTime grater than refund LockTime"))
	}
	if conf.Interval.AutoSettleBufferTime > conf.Interval.ForceSettlementProcessor {
		panic(errors.New("Interval.AutoSettleBufferTime grater than forceSettlement Interval"))
	}
	if int(contract.LockTime)-conf.Interval.AutoSettleBufferTime < 60 {
		panic(errors.New("Interval.AutoSettleBufferTime is too large, which could lead to overly frequent settlements"))
	}
	if conf.Interval.ForceSettlementProcessor < 60 {
		panic(errors.New("Interval.ForceSettlementProcessor is too small, which could lead to overly frequent settlements"))
	}

	cfg := &rest.Config{}
	mgr, err := controller.NewManager(cfg, controller.Options{
		Metrics: metricserver.Options{
			BindAddress: conf.Event.ProviderAddr,
		},
	})
	if err != nil {
		panic(err)
	}

	zk := zkclient.NewZKClient(conf.ZKProver.Provider, conf.ZKProver.RequestLength)
	ctrl := ctrl.New(db, contract, zk, config.Service{}, conf.Interval.AutoSettleBufferTime, nil)
	settlementProcessor := event.NewSettlementProcessor(ctrl, conf.Interval.SettlementProcessor, conf.Interval.ForceSettlementProcessor, conf.Monitor.Enable)
	if err := mgr.Add(settlementProcessor); err != nil {
		panic(err)
	}

	ctx := controller.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
