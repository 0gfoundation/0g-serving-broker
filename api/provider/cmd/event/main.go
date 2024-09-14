package event

import (
	"k8s.io/client-go/rest"
	controller "sigs.k8s.io/controller-runtime"
	metricserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/0glabs/0g-serving-agent/common/config"
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/zkclient"
	providercontract "github.com/0glabs/0g-serving-agent/provider/internal/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
	database "github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/internal/event"
)

func Main() {
	config := config.GetConfig()

	db, err := database.NewDB(config)
	if err != nil {
		panic(err)
	}
	contract, err := providercontract.NewProviderContract(config)
	if err != nil {
		panic(err)
	}
	if config.Interval.AutoSettleBufferTime > int(contract.LockTime) {
		panic(errors.New("Interval.AutoSettleBufferTime grater than refund LockTime"))
	}
	if config.Interval.AutoSettleBufferTime > config.Interval.ForceSettlementProcessor {
		panic(errors.New("Interval.AutoSettleBufferTime grater than forceSettlement Interval"))
	}
	if int(contract.LockTime)-config.Interval.AutoSettleBufferTime < 60 {
		panic(errors.New("Interval.AutoSettleBufferTime is too large, which could lead to overly frequent settlements"))
	}
	if config.Interval.ForceSettlementProcessor < 60 {
		panic(errors.New("Interval.ForceSettlementProcessor is too small, which could lead to overly frequent settlements"))
	}

	cfg := &rest.Config{}
	mgr, err := controller.NewManager(cfg, controller.Options{
		Metrics: metricserver.Options{
			BindAddress: config.Event.ProviderAddr,
		},
	})
	if err != nil {
		panic(err)
	}

	zk := zkclient.NewZKClient(config.ZKProver.Provider, config.ZKProver.RequestLength)
	ctrl := ctrl.New(db, contract, zk, "", config.Interval.AutoSettleBufferTime, nil)
	settlementProcessor := event.NewSettlementProcessor(ctrl, config.Interval.SettlementProcessor, config.Interval.ForceSettlementProcessor)
	if err := mgr.Add(settlementProcessor); err != nil {
		panic(err)
	}

	ctx := controller.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
