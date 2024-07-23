package event

import (
	"os"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ethereum/go-ethereum/common"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/0glabs/0g-serving-agent/common/config"
	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/event"
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

	c, err := contract.NewServingContract(common.HexToAddress(config.ContractAddress), config, os.Getenv("NETWORK"))
	if err != nil {
		panic(err)
	}
	defer c.Close()

	cfg := &rest.Config{}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{})
	if err != nil {
		panic(err)
	}

	settlementProcessor := event.NewSettlementProcessor(db, c, config.Address, config.Interval.SettlementProcessor)
	if err := mgr.Add(settlementProcessor); err != nil {
		panic(err)
	}

	ctx := ctrl.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		panic(err)
	}
}
