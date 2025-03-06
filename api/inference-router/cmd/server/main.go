package server

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/inference-router/config"
	usercontract "github.com/0glabs/0g-serving-broker/inference-router/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference-router/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference-router/internal/db"
	"github.com/0glabs/0g-serving-broker/inference-router/internal/handler"
	"github.com/0glabs/0g-serving-broker/inference-router/zkclient"
)

//go:generate swag fmt
//go:generate swag init --dir ./,../../ --output ../../doc

//	@title		0G Serving Router Broker API
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

	contract, err := usercontract.NewUserContract(config)
	if err != nil {
		panic(err)
	}
	defer contract.Close()

	ctx := context.Background()
	zk := zkclient.NewZKClient(config.ZKProver.Router, config.ZKProver.RequestLength)

	r := gin.New()
	svcCache := cache.New(5*time.Minute, 10*time.Minute)
	ctrl := ctrl.New(db, contract, zk, svcCache)
	if err := ctrl.SyncProviderAccounts(ctx); err != nil {
		panic(err)
	}
	h := handler.New(ctrl, config.PresetService.ProviderAddress)
	h.Register(r)

	// Listen and Serve, config port with PORT=X
	if err := r.Run(); err != nil {
		panic(err)
	}
}
