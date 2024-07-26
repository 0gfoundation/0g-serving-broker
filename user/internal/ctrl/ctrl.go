package ctrl

import (
	usercontract "github.com/0glabs/0g-serving-agent/user/internal/contract"
	"github.com/0glabs/0g-serving-agent/user/internal/db"
	"github.com/patrickmn/go-cache"
)

type Ctrl struct {
	db       *db.DB
	contract *usercontract.UserContract
	svcCache *cache.Cache

	signingKey string
}

func New(db *db.DB, contract *usercontract.UserContract, signingKey string, svcCache *cache.Cache) *Ctrl {
	return &Ctrl{
		db:         db,
		contract:   contract,
		svcCache:   svcCache,
		signingKey: signingKey,
	}
}
