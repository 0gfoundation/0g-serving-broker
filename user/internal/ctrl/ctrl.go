package ctrl

import (
	"github.com/0glabs/0g-serving-agent/common/errors"
	usercontract "github.com/0glabs/0g-serving-agent/user/internal/contract"
	"github.com/0glabs/0g-serving-agent/user/internal/db"
	"github.com/gin-gonic/gin"
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

func handleError(ctx *gin.Context, err error, context string) {
	errors.Response(ctx, errors.Wrap(err, "User: "+context))
}
