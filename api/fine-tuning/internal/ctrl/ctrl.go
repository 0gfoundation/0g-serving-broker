package ctrl

import (
	"sync"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
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

	teeService       *tee.TeeService
	customizedModels map[ethcommon.Hash]config.CustomizedModel

	taskMutex sync.Mutex
}

func New(db *db.DB, cfg *config.Config, contract *providercontract.ProviderContract, teeService *tee.TeeService, logger log.Logger) *Ctrl {
	p := &Ctrl{
		db:               db,
		contract:         contract,
		config:           cfg,
		teeService:       teeService,
		customizedModels: cfg.Service.GetCustomizedModels(),
		logger:           logger,
	}

	return p
}
