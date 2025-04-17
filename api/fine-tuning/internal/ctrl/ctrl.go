package ctrl

import (
	"sync"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	ethcommon "github.com/ethereum/go-ethereum/common"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract
	config   *config.Config
	logger   log.Logger

	phalaService     *phala.PhalaService
	customizedModels map[ethcommon.Hash]config.CustomizedModel

	taskMutex sync.Mutex
}

func New(db *db.DB, cfg *config.Config, contract *providercontract.ProviderContract, phalaService *phala.PhalaService, logger log.Logger) *Ctrl {
	p := &Ctrl{
		db:               db,
		contract:         contract,
		config:           cfg,
		phalaService:     phalaService,
		customizedModels: cfg.Service.GetCustomizedModels(),
		logger:           logger,
	}

	return p
}
