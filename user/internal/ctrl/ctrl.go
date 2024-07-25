package ctrl

import (
	usercontract "github.com/0glabs/0g-serving-agent/user/internal/contract"
	"github.com/0glabs/0g-serving-agent/user/internal/db"
)

type Ctrl struct {
	db       *db.DB
	contract *usercontract.UserContract
}

func New(db *db.DB, contract *usercontract.UserContract) *Ctrl {
	return &Ctrl{
		db:       db,
		contract: contract,
	}
}
