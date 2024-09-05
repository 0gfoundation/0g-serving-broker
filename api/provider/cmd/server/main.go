package server

import (
	"context"
	"log"

	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/config"
	"github.com/0glabs/0g-serving-agent/common/zkclient"
	providercontract "github.com/0glabs/0g-serving-agent/provider/internal/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
	database "github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/internal/handler"
	"github.com/0glabs/0g-serving-agent/provider/internal/proxy"
)

//go:generate swag fmt
//go:generate swag init --dir ./,../../ --output ../../doc

//	@title		0G Serving Provider Agent API
//	@version	1.0
//	@BasePath	/v1

//	@in	header

func Main() {
	config := config.GetConfig()

	db, err := database.NewDB(config)
	if err != nil {
		panic(err)
	}
	if err := db.Migrate(); err != nil {
		panic(err)
	}

	contract, err := providercontract.NewProviderContract(config)
	if err != nil {
		panic(err)
	}
	defer contract.Close()

	zk := zkclient.NewZKClient(config.ZKProver.Provider, config.ZKProver.RequestLength)
	engine := gin.New()
	ctrl := ctrl.New(db, contract, zk, config.ServingUrl, config.Interval.AutoSettleBufferTime)
	ctx := context.Background()
	if err := ctrl.SyncUserAccounts(ctx); err != nil {
		panic(err)
	}
	settleFeesErr := ctrl.SettleFees(ctx)
	if settleFeesErr != nil {
		log.Printf("settle fee failed: %s", settleFeesErr.Error())
	} else if err := ctrl.SyncServices(ctx); err != nil {
		panic(err)
	}
	proxy := proxy.New(ctrl, engine)
	if err := proxy.Start(); err != nil {
		panic(err)
	}

	h := handler.New(ctrl, proxy)
	h.Register(engine)

	// Listen and Serve, config port with PORT=X
	if err := engine.Run(); err != nil {
		panic(err)
	}
}
