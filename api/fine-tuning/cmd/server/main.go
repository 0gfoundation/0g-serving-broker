package server

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/handler"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/settlement"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/verifier"
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

	logger, err := log.GetLogger(&config.Logger)
	if err != nil {
		panic(err)
	}

	db, err := db.NewDB(config, logger)
	if err != nil {
		panic(err)
	}
	if err := db.Migrate(); err != nil {
		panic(err)
	}

	storageClient, err := storage.New(config, logger)
	if err != nil {
		panic(err)
	}

	contract, err := providercontract.NewProviderContract(config, logger)
	if err != nil {
		panic(err)
	}
	defer contract.Close()

	verifier, err := verifier.New(contract, logger)
	if err != nil {
		panic(err)
	}

	ctrl := ctrl.New(db, config, contract, storageClient, verifier, logger)
	ctx := context.Background()
	err = ctrl.SyncServices(ctx)
	if err != nil {
		panic(err)
	}

	err = ctrl.SyncQuote(ctx)
	if err != nil {
		// TODO: panic error
		logger.Errorf("Error syncing quote: %v", err)

	}

	engine := gin.New()
	h := handler.New(ctrl, logger)
	h.Register(engine)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	settlement, err := settlement.New(logger)
	if err != nil {
		panic(err)
	}
	settlement.Start(ctx)

	go func() {
		// Listen and Serve, config port with PORT=X
		if err := engine.Run(); err != nil {
			panic(err)
		}
	}()

	logger.Info("Server started")
	<-stop

	if err := ctrl.DeleteAllService(ctx); err != nil {
		logger.Errorf("Error deleting all services: %v", err)
	}
}
