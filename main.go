package main

import (
	"github.com/gin-gonic/gin"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-data-retrieve-agent/internal/config"
	"github.com/0glabs/0g-data-retrieve-agent/internal/contract"
	database "github.com/0glabs/0g-data-retrieve-agent/internal/db"
	"github.com/0glabs/0g-data-retrieve-agent/internal/handler"
	"github.com/0glabs/0g-data-retrieve-agent/internal/proxy"
)

func main() {
	config := config.GetConfig()

	db, err := gorm.Open(mysql.Open(config.MySQL), &gorm.Config{
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

	client := contract.MustNewWeb3(config.ChainUrl, config.PrivateKey)
	defer client.Close()
	c, err := contract.NewServingContract(common.HexToAddress(config.ContractAddress), client, config.CustomGasPrice, config.CustomGasLimit)
	if err != nil {
		panic(err)
	}

	r := gin.New()
	p := proxy.New(db, r, c, config.Address, config.PrivateKey)
	if err := p.Start(); err != nil {
		panic(err)
	}

	h := handler.New(db, p, c, config.ServingUrl, config.PrivateKey)
	h.Register(r)

	// Listen and Serve, config port with PORT=X
	if err := r.Run(); err != nil {
		panic(err)
	}
}
