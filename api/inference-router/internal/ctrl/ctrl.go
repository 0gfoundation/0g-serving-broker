package ctrl

import (
	"io"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/errors"
	usercontract "github.com/0glabs/0g-serving-broker/inference-router/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference-router/internal/db"
	"github.com/0glabs/0g-serving-broker/inference-router/zkclient"
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

func handleBrokerError(ctx *gin.Context, err error, context string) {
	// TODO: recorded to log system
	info := "User"
	if context != "" {
		info += (": " + context)
	}
	errors.Response(ctx, errors.Wrap(err, info))
}

func handleServiceError(ctx *gin.Context, body io.ReadCloser) {
	respBody, err := io.ReadAll(body)
	if err != nil {
		// TODO: recorded to log system
		log.Println(err)
		return
	}
	ctx.Writer.Write(respBody)
}
