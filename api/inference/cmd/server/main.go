package server

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/monitor"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/0glabs/0g-serving-broker/inference/config"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/ctrl"
	database "github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/handler"
	"github.com/0glabs/0g-serving-broker/inference/internal/proxy"
	"github.com/0glabs/0g-serving-broker/inference/internal/signer"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
)

//go:generate swag fmt
//go:generate swag init --dir ./,../../ --output ../../doc

//	@title			0G Serving Provider Broker API
//	@version		0.1.0
//	@description	These APIs allow providers to manage services and user accounts. The host is localhost, and the port is configured in the provider's configuration file, defaulting to 3080.
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

	zk := zkclient.NewZKClient(config.ZKProver.Provider, config.ZKProver.RequestLength)
	engine := gin.New()

	if config.Monitor.Enable {
		monitor.PrometheusInit(config.Service.ServingURL)
		engine.GET("/metrics", gin.WrapH(promhttp.Handler()))
	}

	svcCache := cache.New(5*time.Minute, 10*time.Minute)

	var teeClientType tee.ClientType
	switch os.Getenv("NETWORK") {
	case "hardhat":
		teeClientType = tee.Mock
	default:
		teeClientType = tee.Phala
	}

	teeService, err := tee.NewTeeService(teeClientType)
	if err != nil {
		panic(err)
	}

	ctx := context.Background()
	if err := teeService.SyncQuote(ctx); err != nil {
		panic(err)
	}

	if config.NvGPU {
		if err := util.CheckPythonEnv(util.NvTrustPackages, nil); err != nil {
			panic(err)
		}

		if err := teeService.SyncGPUPayload(ctx, teeClientType == tee.Mock); err != nil {
			log.Printf("err %v", err)
		}
	}

	signer, _ := signer.NewSigner()
	encryptedKey, err := signer.InitialKey(ctx, contract, zk, teeService.ProviderSigner)
	if err != nil {
		panic(err)
	}
	contract.EncryptedPrivKey = encryptedKey

	ctrl := ctrl.New(db, contract, zk, config, svcCache, teeService, signer)

	if err := ctrl.SyncUserAccounts(ctx); err != nil {
		panic(err)
	}
	settleFeesErr := ctrl.SettleFees(ctx)
	if settleFeesErr != nil {
		panic(settleFeesErr)
	}
	if err := ctrl.SyncService(ctx); err != nil {
		panic(err)
	}
	proxy := proxy.New(ctrl, engine, config.AllowOrigins, config.Monitor.Enable)
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
