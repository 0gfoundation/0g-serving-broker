package server

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-agent/common/config"
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

	r := gin.New()
	svcCache := cache.New(5*time.Minute, 10*time.Minute)
	ctrl := ctrl.New(db, contract, config.SigningKey, svcCache)
	h := handler.New(db, ctrl, contract)
	h.Register(r)

	// Listen and Serve, config port with PORT=X
	if err := r.Run(); err != nil {
		panic(err)
	}
}
