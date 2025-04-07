package ctrl

import (
	"sync"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/phala"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract
	config   *config.Config
	logger   log.Logger

	phalaService *phala.PhalaService

	taskMutex sync.Mutex
}

func New(db *db.DB, config *config.Config, contract *providercontract.ProviderContract, phalaService *phala.PhalaService, logger log.Logger) *Ctrl {
	p := &Ctrl{
		db:           db,
		contract:     contract,
		config:       config,
		phalaService: phalaService,
		logger:       logger,
	}

	return p
}
