package ctrl

import (
	"context"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

func (c *Ctrl) DeleteAllService(ctx context.Context) error {
	svcs, err := c.contract.ListService(ctx)
	if err != nil {
		return errors.Wrap(err, "delete service in contract")
	}
	for _, svc := range svcs {
		if err := c.contract.DeleteService(ctx, svc.Name); err != nil {
			return errors.Wrapf(err, "delete service in contract %s", svc.Name)
		}
	}

	return nil
}

func (c *Ctrl) SyncServices(ctx context.Context) error {
	if err := c.contract.SyncServices(ctx, c.services); err != nil {
		return errors.Wrap(err, "sync services in contract")
	}
	return nil
}
