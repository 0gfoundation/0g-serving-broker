package event

import (
	"context"
	"log"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/provider/model"
	"gorm.io/gorm"
)

type SettlementProcessor struct {
	db       *gorm.DB
	contract *contract.ServingContract

	address  string
	interval int
}

func NewSettlementProcessor(db *gorm.DB, contract *contract.ServingContract, address string, interval int) *SettlementProcessor {
	b := &SettlementProcessor{
		db:       db,
		contract: contract,
		address:  address,
		interval: interval,
	}
	return b
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (b SettlementProcessor) Start(ctx context.Context) error {
	for {
		time.Sleep(time.Duration(b.interval) * time.Second)
		list := []model.User{}
		if ret := b.db.Model(model.User{}).Where("unsettled_fee > ?", 0).Order("created_at DESC").Find(&list); ret.Error != nil {
			return ret.Error
		}
		log.Println(*list[0].UnsettledFee)
	}
}
