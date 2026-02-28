package server

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	image "github.com/0glabs/0g-serving-broker/common/docker"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	constant "github.com/0glabs/0g-serving-broker/fine-tuning/const"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/ctrl"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/handler"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/services"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/serving"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/utils"
	"github.com/0glabs/0g-serving-broker/fine-tuning/monitor"
	"github.com/docker/docker/client"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	cfg, logger, err := initializeBaseComponents()
	if err != nil {
		panic(err)
	}

	utils.SetDataDir(cfg.Service.DataDir)
	logger.Infof("Data directory set to: %s", utils.GetDataDir())

	if err := util.CheckPythonEnv(util.TrainingPackages, logger); err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	imageChan := buildImageIfNeeded(ctx, cfg, logger)

	appServices, err := initializeServices(ctx, cfg, logger)
	if err != nil {
		panic(err)
	}
	defer appServices.contract.Close()

	if err := runApplication(ctx, cfg, appServices, logger, imageChan); err != nil {
		panic(err)
	}
}

type ApplicationServices struct {
	db            *db.DB
	storageClient *storage.Client
	contract      *providercontract.ProviderContract
	teeService    *tee.TeeService
	ctrl          *ctrl.Ctrl
	setup         *services.Setup
	executor      *services.Executor
	finalizer     *services.Finalizer
	settlement    *services.Settlement
}

func initializeBaseComponents() (*config.Config, log.Logger, error) {
	config := config.GetConfig()
	logger, err := log.GetLogger(&config.Logger)
	return config, logger, err
}

func buildImageIfNeeded(ctx context.Context, config *config.Config, logger log.Logger) chan bool {
	imageChan := make(chan bool, 1)

	if !config.Images.BuildImage {
		imageChan <- true
		close(imageChan)
		return imageChan
	}

	go func() {
		defer close(imageChan)

		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			logger.Errorf("failed to create docker client: %v", err)
			return
		}
		defer cli.Close()

		imageName := config.Images.ExecutionImageName
		buildImage := true
		if !config.Images.OverrideImage {
			exists, err := image.ImageExists(ctx, cli, imageName)
			if err != nil {
				logger.Errorf("failed to check image existence: %v", err)
				return
			}

			logger.Debugf("Docker image: %s, exist: %v.", imageName, exists)
			if exists {
				buildImage = false
			}
		}

		if buildImage {
			logger.Debugf("build image %s", imageName)

			embeddedPath := "/fine-tuning/execution/transformer"

			if _, err := os.Stat(embeddedPath); err == nil {
				logger.Infof("Found embedded transformer files at %s", embeddedPath)

				bridgeDir := constant.FineTuningDockerfilePath
				if entries, err := os.ReadDir(bridgeDir); err == nil {
					for _, entry := range entries {
						entryPath := filepath.Join(bridgeDir, entry.Name())
						if err := os.RemoveAll(entryPath); err != nil {
							logger.Warnf("failed to remove %s: %v", entryPath, err)
						}
					}
				}

				if err := os.MkdirAll(bridgeDir, 0755); err != nil {
					logger.Errorf("failed to create bridge directory: %v", err)
					return
				}

				logger.Infof("Copying transformer files to bridge directory: %s", bridgeDir)
				if err := copyDirectory(embeddedPath, bridgeDir); err != nil {
					logger.Errorf("failed to copy transformer files: %v", err)
					return
				}
				logger.Infof("Transformer files copied successfully to bridge directory")
			} else {
				logger.Warnf("Embedded transformer files not found at %s, checking bridge directory", embeddedPath)
			}

			logger.Infof("Building image from: %s", constant.FineTuningDockerfilePath)
			err := image.ImageBuild(ctx, cli, constant.FineTuningDockerfilePath, imageName, logger)
			if err != nil {
				logger.Errorf("failed to build image: %v", err)
				return
			}
			logger.Infof("Docker image %s built successfully!", imageName)
		}

		imageChan <- true
	}()

	return imageChan
}

func initializeServices(ctx context.Context, cfg *config.Config, logger log.Logger) (*ApplicationServices, error) {
	database, err := db.NewDB(cfg, logger)
	if err != nil {
		return nil, err
	}
	if err := database.Migrate(); err != nil {
		return nil, err
	}

	storageClient, err := storage.New(cfg, logger)
	if err != nil {
		return nil, err
	}

	var teeClientType tee.ClientType
	switch os.Getenv("NETWORK") {
	case "gcp":
		teeClientType = tee.GCP
	default:
		teeClientType = tee.Phala
	}

	teeService, err := tee.NewTeeService(teeClientType, logger)
	if err != nil {
		return nil, err
	}

	logger.Info("syncing TEE quote during service initialization")
	if err := teeService.SyncQuote(ctx, os.Getenv("NETWORK") != "hardhat"); err != nil {
		return nil, err
	}

	contract, err := providercontract.NewProviderContract(cfg, teeService.Address, logger)
	if err != nil {
		return nil, err
	}

	ctrlInst := ctrl.New(database, cfg, contract, teeService, logger)

	setup, err := services.NewSetup(database, cfg, contract, logger, storageClient, teeService)
	if err != nil {
		return nil, err
	}

	executor, err := services.NewExecutor(database, cfg, contract, logger)
	if err != nil {
		return nil, err
	}

	finalizer, err := services.NewFinalizer(database, cfg, contract, logger, storageClient, teeService)
	if err != nil {
		return nil, err
	}

	settlement, err := services.NewSettlement(database, contract, cfg, teeService, logger)
	if err != nil {
		return nil, err
	}

	return &ApplicationServices{
		db:            database,
		storageClient: storageClient,
		contract:      contract,
		teeService:    teeService,
		ctrl:          ctrlInst,
		setup:         setup,
		executor:      executor,
		finalizer:     finalizer,
		settlement:    settlement,
	}, nil
}

func runApplication(ctx context.Context, cfg *config.Config, svc *ApplicationServices, logger log.Logger, imageChan <-chan bool) error {
	if err := svc.db.MarkInProgressTasksAsFailed(); err != nil {
		return err
	}

	if err := svc.ctrl.SyncServices(ctx); err != nil {
		return err
	}

	if err := svc.finalizer.Start(ctx); err != nil {
		return err
	}

	if err := svc.executor.Start(ctx); err != nil {
		return err
	}

	if err := svc.setup.Start(ctx); err != nil {
		return err
	}

	engine := gin.New()

	var wg sync.WaitGroup

	if cfg.Monitor.Enable {
		monitor.Init(cfg.Service.ServingUrl, ctx)
		engine.GET("/metrics", gin.WrapH(promhttp.Handler()))
		engine.Use(monitor.TrackMetrics())
		wg.Add(1)
		go func() {
			defer wg.Done()
			startTaskStatePoller(ctx, svc.db, logger)
		}()
	}

	var servingProxy *serving.Proxy
	if cfg.Serving.Enable {
		servingMgr := serving.NewManager(svc.db, serving.ServingConfig{
			Enable:              cfg.Serving.Enable,
			BaseModelPath:       cfg.Serving.BaseModelPath,
			InferenceGPUIDs:     cfg.Serving.InferenceGPUIDs,
			VLLMPort:            cfg.Serving.VLLMPort,
			MaxLoraRank:         cfg.Serving.MaxLoraRank,
			MaxLoraModules:      cfg.Serving.MaxLoraModules,
			MaxCpuLoras:         cfg.Serving.MaxCpuLoras,
			LoraModulesDir:      cfg.Serving.LoraModulesDir,
			OffloadAfterMinutes:     cfg.Serving.OffloadAfterMinutes,
			EnableColdStorage:       cfg.Serving.EnableColdStorage,
			ModelLoadTimeoutSeconds: cfg.Serving.ModelLoadTimeoutSeconds,
		}, logger, svc.storageClient)
		if err := servingMgr.Start(ctx); err != nil {
			return err
		}
		defer func() {
			if err := servingMgr.Stop(); err != nil {
				logger.Warnf("failed to stop vLLM: %v", err)
			}
		}()

		registry := serving.NewRegistry(svc.contract, servingMgr, serving.RegistryConfig{
			InputPrice:  cfg.Serving.InputPrice,
			OutputPrice: cfg.Serving.OutputPrice,
		}, logger)
		registry.Start(ctx)

		servingProxy = serving.NewProxy(servingMgr, logger)
		logger.Info("LoRA serving module initialized")
	}

	h := handler.New(svc.ctrl, logger, cfg.RateLimitRPS, cfg.RateLimitBurst, servingProxy)
	h.Register(engine)

	if _, ok := <-imageChan; !ok {
		return errors.New("image build failed")
	}

	if err := svc.settlement.Start(ctx); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Info("starting http server...")
		if err := engine.Run(); err != nil {
			logger.Errorf("HTTP server error: %v", err)
			stop <- os.Interrupt
		}
	}()

	<-stop
	logger.Info("shutting down server...")
	wg.Wait()
	return nil
}

func startTaskStatePoller(ctx context.Context, database *db.DB, logger log.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			counts, err := database.CountTasksByState()
			if err != nil {
				logger.Warnf("failed to count tasks by state for metrics: %v", err)
				continue
			}
			monitor.UpdateTaskStateGauge(counts)
		}
	}
}

func copyDirectory(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		if entry.IsDir() {
			if err := copyDirectory(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyFile(srcPath, dstPath); err != nil {
				return err
			}
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}
