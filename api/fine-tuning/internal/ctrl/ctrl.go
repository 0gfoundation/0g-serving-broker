package ctrl

import (
	"crypto/ecdsa"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/fine-tuning/config"
	providercontract "github.com/0glabs/0g-serving-broker/fine-tuning/internal/contract"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/db"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/storage"
	"github.com/0glabs/0g-serving-broker/fine-tuning/internal/verifier"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract
	storage  *storage.Client
	services []config.Service
	verifier *verifier.Verifier
	logger   log.Logger

	providerSigner *ecdsa.PrivateKey
	quote          string
}

func New(db *db.DB, config *config.Config, contract *providercontract.ProviderContract, storage *storage.Client, verifier *verifier.Verifier, logger log.Logger) *Ctrl {
	p := &Ctrl{
		db:       db,
		contract: contract,
		storage:  storage,
		services: config.Services,
		verifier: verifier,
		logger:   logger,
	}

	return p
}
