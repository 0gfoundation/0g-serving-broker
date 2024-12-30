package ctrl

import (
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract

	services []config.Service
}

func New(db *db.DB, contract *providercontract.ProviderContract, services []config.Service) *Ctrl {
	p := &Ctrl{
		db:       db,
		contract: contract,
		services: services,
	}

	return p
}
