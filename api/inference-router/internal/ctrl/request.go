package ctrl

import (
	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference-router/model"
)

func (c *Ctrl) CreateRequest(req model.Request) error {
	return errors.Wrap(c.db.CreateRequest(req), "create request in db")
}

func (c *Ctrl) ListRequest() ([]model.Request, int, error) {
	list, fee, err := c.db.ListRequest()
	if err != nil {
		return nil, 0, errors.Wrap(err, "list request from db")
	}
	return list, fee, nil
}
