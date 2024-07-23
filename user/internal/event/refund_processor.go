package event

import (
	"context"
	"log"
	"time"

	"github.com/0glabs/0g-serving-agent/common/contract"
	"github.com/0glabs/0g-serving-agent/user/model"
	"gorm.io/gorm"
)

type RefundProcessor struct {
	db       *gorm.DB
	contract *contract.ServingContract

	address  string
	interval int
}

func NewRefundProcessor(db *gorm.DB, contract *contract.ServingContract, address string, interval int) *RefundProcessor {
	b := &RefundProcessor{
		db:       db,
		contract: contract,
		address:  address,
		interval: interval,
	}
	return b
}

// Start implements controller-runtime/pkg/manager.Runnable interface
func (b RefundProcessor) Start(ctx context.Context) error {
	for {
		time.Sleep(time.Duration(b.interval) * time.Second)
		list := []model.Account{}
		if ret := b.db.Model(model.Account{}).Where("pending_refund > ?", 0).Order("created_at DESC").Find(&list); ret.Error != nil {
			return ret.Error
		}
		log.Println(list[0].Balance)
	}
}
