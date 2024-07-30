package server

import (
	"github.com/gin-gonic/gin"

	"github.com/0glabs/0g-serving-agent/common/config"
	providercontract "github.com/0glabs/0g-serving-agent/provider/internal/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
	database "github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/internal/handler"
	"github.com/0glabs/0g-serving-agent/provider/internal/proxy"
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

	contract, err := providercontract.NewProviderContract(config, config.Address)
	if err != nil {
		panic(err)
	}
	defer contract.Close()

	engine := gin.New()
	ctrl := ctrl.New(db, contract, config.ServingUrl)
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
