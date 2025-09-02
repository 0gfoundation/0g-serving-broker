package ctrl

import (
	"math/big"
	"sync"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	providercontract "github.com/0glabs/0g-serving-broker/inference/internal/contract"
	"github.com/0glabs/0g-serving-broker/inference/internal/db"
	"github.com/0glabs/0g-serving-broker/inference/internal/signer"
	"github.com/0glabs/0g-serving-broker/inference/zkclient"
)

type SettleVerifierInput struct {
	contract.VerifierInput
	totalFeeInSettlement map[string]string
}

type Ctrl struct {
	mu       sync.RWMutex
	db       *db.DB
	contract *providercontract.ProviderContract
	zk       zkclient.ZKClient
	svcCache *cache.Cache

	autoSettleBufferTime time.Duration

	Service config.Service

	teeService          *tee.TeeService
	signer              *signer.Signer
	chatCacheExpiration time.Duration

	prepareSettleProgress map[string]string
	settleMu              sync.RWMutex
	settleVerifierInput   *SettleVerifierInput
}

func New(
	db *db.DB,
	providerContract *providercontract.ProviderContract,
	zkclient zkclient.ZKClient,
	cfg *config.Config,
	svcCache *cache.Cache,
	teeService *tee.TeeService,
	signer *signer.Signer,
) *Ctrl {
	p := &Ctrl{
		autoSettleBufferTime:  time.Duration(cfg.Interval.AutoSettleBufferTime) * time.Second,
		db:                    db,
		contract:              providerContract,
		Service:               cfg.Service,
		zk:                    zkclient,
		svcCache:              svcCache,
		teeService:            teeService,
		signer:                signer,
		chatCacheExpiration:   cfg.ChatCacheExpiration,
		prepareSettleProgress: make(map[string]string),
		settleVerifierInput: &SettleVerifierInput{
			VerifierInput: contract.VerifierInput{
				InProof:     []*big.Int{},
				ProofInputs: []*big.Int{},
				NumChunks:   big.NewInt(0),
				SegmentSize: []*big.Int{},
			},
			totalFeeInSettlement: make(map[string]string),
		},
	}

	return p
}
