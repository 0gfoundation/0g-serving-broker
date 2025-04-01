package server

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	image "github.com/0glabs/0g-serving-broker/common/docker"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/handler"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/settlement"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/verifier"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
)

//go:generate swag fmt
//go:generate swag init --dir ./,../../ --output ../../doc

//	@title			0G Compute Network Fine-tuning Provider API
//	@version		0.1.0
//	@description	These APIs allows providers to interact with the 0G Compute Fine Tune Service
//	@host			localhost:3080
//	@BasePath		/v1
//	@in				header

func Main() {
	config := config.GetConfig()

	logger, err := log.GetLogger(&config.Logger)
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	imageChan := buildImageIfNeeded(ctx, config, logger)

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

	verifier, err := verifier.New(contract, config.BalanceThresholdInEther, logger)
	if err != nil {
		panic(err)
	}

	ctrl := ctrl.New(db, config, contract, storageClient, verifier, logger)
	if !config.Images.BuildImage {
		err = ctrl.SyncServices(ctx)
		if err != nil {
			panic(err)
		}
	} else {
		err = ctrl.DeleteService(ctx)
		if err != nil {
			logger.Warn(err)
		}
	}

	err = ctrl.SyncQuote(ctx)
	if err != nil {
		panic(err)
	}

	err = ctrl.MarkInProgressTasksAsFailed()
	if err != nil {
		panic(err)
	}

	engine := gin.New()
	h := handler.New(ctrl, logger)
	h.Register(engine)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	settlement, err := settlement.New(db, contract, time.Duration(config.SettlementCheckIntervalSecs)*time.Second, ctrl.GetProviderSignerAddress(ctx), config.Service, logger, config.MaxNumRetriesPerTask)
	if err != nil {
		panic(err)
	}
	settlement.Start(ctx, imageChan)

	// Listen and Serve, config port with PORT=X
	if err := engine.Run(); err != nil {
		panic(err)
	}
}

func buildImageIfNeeded(ctx context.Context, config *config.Config, logger log.Logger) chan bool {
	imageChan := make(chan bool)

	go func() {
		if config.Images.BuildImage {
			cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
			if err != nil {
				panic(err)
			}
			defer cli.Close()

			imageName := config.Images.ExecutionImageName
			buildImage := true
			if !config.Images.OverrideImage {
				exists, err := image.ImageExists(ctx, cli, imageName)
				if err != nil {
					panic(err)
				}

				logger.Debugf("Image %s status %v.", imageName, exists)
				if exists {
					buildImage = false
				}
			}

			if buildImage {
				logger.Debugf("Build image %s", imageName)
				err := image.ImageBuild(ctx, cli, "./fine-tuning/execution/transformer", imageName)
				if err != nil {
					panic(err)
				}

				logger.Debugf("Docker image %s built successfully!", imageName)
			}
		}

		imageChan <- true
	}()
	return imageChan
}
