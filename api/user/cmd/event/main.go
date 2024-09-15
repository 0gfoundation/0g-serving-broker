package event

import (
	"k8s.io/client-go/rest"
	controller "sigs.k8s.io/controller-runtime"

	metricserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/zkclient"
	usercontract "github.com/0glabs/0g-serving-broker/user/internal/contract"
	"github.com/0glabs/0g-serving-broker/user/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/user/internal/db"
	"github.com/0glabs/0g-serving-broker/user/internal/event"
)

func Main() {
	config := config.GetConfig()

	db, err := database.NewDB(config)
	if err != nil {
		panic(err)
	}
	contract, err := usercontract.NewUserContract(config)
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

	ctrl := ctrl.New(db, contract, zkclient.ZKClient{}, nil)
	refundProcessor := event.NewRefundProcessor(ctrl, config.Interval.RefundProcessor)
	if err := mgr.Add(refundProcessor); err != nil {
		panic(err)
	}

	ctx := controller.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
