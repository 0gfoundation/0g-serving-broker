package ctrl

import (
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/common/zkclient"
	usercontract "github.com/0glabs/0g-serving-agent/user/internal/contract"
	"github.com/0glabs/0g-serving-agent/user/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *usercontract.UserContract
	svcCache *cache.Cache
	zkclient zkclient.ZKClient

	signingKey string
}

func New(db *db.DB, contract *usercontract.UserContract, zkclient zkclient.ZKClient, signingKey string, svcCache *cache.Cache) *Ctrl {
	return &Ctrl{
		db:         db,
		contract:   contract,
		svcCache:   svcCache,
		signingKey: signingKey,
		zkclient:   zkclient,
	}
}

func handleError(ctx *gin.Context, err error, context string) {
	errors.Response(ctx, errors.Wrap(err, "User: "+context))
}
