package server

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/handler"
	"github.com/gin-gonic/gin"
)

//go:generate swag fmt
//go:generate swag init --dir ./,../../ --output ../../doc

//	@title			0G Serving Provider Broker API
//	@version		0.2.0
//	@description	These APIs allows customers to interact with the 0G Compute Fine Tune Service
//	@host			localhost:3080
//	@BasePath		/v1
//	@in				header

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

	ctrl := ctrl.New(db, contract, config.Services)

	ctx := context.Background()
	err = ctrl.SyncServices(ctx)
	if err != nil {
		panic(err)
	}

	engine := gin.New()
	h := handler.New(ctrl)
	h.Register(engine)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		// Listen and Serve, config port with PORT=X
		if err := engine.Run(); err != nil {
			panic(err)
		}
	}()

	<-stop

	if err := ctrl.DeleteAllService(ctx); err != nil {
		log.Printf("Error deleting all services: %v", err)
	}
}
