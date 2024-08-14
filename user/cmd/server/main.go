package server

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-agent/common/config"
	"github.com/0glabs/0g-serving-agent/common/zkclient"
	usercontract "github.com/0glabs/0g-serving-agent/user/internal/contract"
	"github.com/0glabs/0g-serving-agent/user/internal/ctrl"
	database "github.com/0glabs/0g-serving-agent/user/internal/db"
	"github.com/0glabs/0g-serving-agent/user/internal/handler"
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

	ctx := context.Background()
	zk := zkclient.NewZKClient(config.ZKProver.Host, config.ZKProver.RequestLength)

	r := gin.New()
	svcCache := cache.New(5*time.Minute, 10*time.Minute)
	ctrl := ctrl.New(db, contract, zk, svcCache)
	if err := ctrl.SyncProviderAccounts(ctx); err != nil {
		panic(err)
	}
	h := handler.New(ctrl)
	h.Register(r)

	// Listen and Serve, config port with PORT=X
	if err := r.Run(); err != nil {
		panic(err)
	}
}
