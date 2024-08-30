package ctrl

import (
	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/user/model"
)

func (c *Ctrl) CreateRequest(req model.Request) error {
	return errors.Wrap(c.db.CreateRequest(req), "create request in db")
}

func (c *Ctrl) ListRequest() ([]model.Request, int, error) {
	list, fee, err := c.db.ListRequest()
	if err != nil {
		return nil, 0, errors.Wrap(err, "list service from db")
	}
	return list, fee, nil
}
