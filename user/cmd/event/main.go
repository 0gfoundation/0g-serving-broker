package event

import (
	"k8s.io/client-go/rest"
	controller "sigs.k8s.io/controller-runtime"

	metricserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/0glabs/0g-serving-agent/common/config"
	usercontract "github.com/0glabs/0g-serving-agent/user/internal/contract"
	"github.com/0glabs/0g-serving-agent/user/internal/ctrl"
	database "github.com/0glabs/0g-serving-agent/user/internal/db"
	"github.com/0glabs/0g-serving-agent/user/internal/event"
)

func Main() {
	config := config.GetConfig()

	db, err := database.NewDB(config)
	if err != nil {
		panic(err)
	}
	if err := db.Migrate(); err != nil {
		panic(err)
	}
	contract, err := usercontract.NewUserContract(config, config.Address)
	if err != nil {
		panic(err)
	}
	defer contract.Close()

	cfg := &rest.Config{}
	mgr, err := controller.NewManager(cfg, controller.Options{
		Metrics: metricserver.Options{
			BindAddress: config.Event.UserAddr,
		},
	})
	if err != nil {
		panic(err)
	}

	ctrl := ctrl.New(db, contract, nil, "", nil)
	refundProcessor := event.NewRefundProcessor(ctrl, config.Interval.RefundProcessor)
	if err := mgr.Add(refundProcessor); err != nil {
		panic(err)
	}

	ctx := controller.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
