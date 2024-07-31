package ctrl

import (
	"time"

	providercontract "github.com/0glabs/0g-serving-agent/provider/internal/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract

	servingUrl           string
	AutoSettleBufferTime time.Duration
}

func New(db *db.DB, contract *providercontract.ProviderContract, servingUrl string, AutoSettleBufferTime int) *Ctrl {
	p := &Ctrl{
		db:                   db,
		contract:             contract,
		servingUrl:           servingUrl,
		AutoSettleBufferTime: time.Duration(AutoSettleBufferTime) * time.Second,
	}

	return p
}
