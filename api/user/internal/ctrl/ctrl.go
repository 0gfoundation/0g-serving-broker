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
	zk       zkclient.ZKClient
}

func New(db *db.DB, contract *usercontract.UserContract, zk zkclient.ZKClient, svcCache *cache.Cache) *Ctrl {
	return &Ctrl{
		db:       db,
		contract: contract,
		svcCache: svcCache,
		zk:       zk,
	}
}

func handleError(ctx *gin.Context, err error, context string) {
	errors.Response(ctx, errors.Wrap(err, "User: "+context))
}
