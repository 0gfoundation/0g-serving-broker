package ctrl

import (
	"time"

	"github.com/0glabs/0g-serving-agent/common/zkclient"
	providercontract "github.com/0glabs/0g-serving-agent/provider/internal/contract"
	"github.com/0glabs/0g-serving-agent/provider/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *providercontract.ProviderContract
	zkclient zkclient.ZKClient

	servingUrl           string
	autoSettleBufferTime time.Duration
}

func New(db *db.DB, contract *providercontract.ProviderContract, zkclient zkclient.ZKClient, servingUrl string, autoSettleBufferTime int) *Ctrl {
	p := &Ctrl{
		autoSettleBufferTime: time.Duration(autoSettleBufferTime) * time.Second,
		db:                   db,
		contract:             contract,
		servingUrl:           servingUrl,
		zkclient:             zkclient,
	}

	return p
}
