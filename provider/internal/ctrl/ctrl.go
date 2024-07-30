package ctrl

import (
	providercontract "github.com/0glabs/0g-serving-agent/provider/internal/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract

	servingUrl string
}

func New(db *db.DB, contract *providercontract.ProviderContract, servingUrl string) *Ctrl {
	p := &Ctrl{
		db:         db,
		contract:   contract,
		servingUrl: servingUrl,
	}

	return p
}
