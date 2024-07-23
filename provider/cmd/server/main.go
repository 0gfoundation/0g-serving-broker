package server

import (
	"os"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-agent/common/config"
	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/ctrl"
	database "github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/internal/handler"
	"github.com/0glabs/0g-serving-agent/provider/internal/proxy"
)

func Main() {
	config := config.GetConfig()

	db, err := gorm.Open(mysql.Open(config.Database.Provider), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{
			SingularTable: true,
		},
	})
	if err != nil {
		panic(err)
	}
	if err := database.Migrate(db); err != nil {
		panic(err)
	}

	c, err := contract.NewServingContract(common.HexToAddress(config.ContractAddress), config, os.Getenv("NETWORK"))
	if err != nil {
		panic(err)
	}
	defer c.Close()

	r := gin.New()
	ctrl := ctrl.New(db)
	p := proxy.New(db, ctrl, r, c, config.Address)
	if err := p.Start(); err != nil {
		panic(err)
	}

	h := handler.New(db, ctrl, p, c, config.ServingUrl)
	h.Register(r)

	// Listen and Serve, config port with PORT=X
	if err := r.Run(); err != nil {
		panic(err)
	}
}
