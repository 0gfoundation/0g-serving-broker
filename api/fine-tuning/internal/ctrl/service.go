package ctrl

import (
	"context"

	"github.com/0glabs/0g-serving-broker/common/errors"
)

func (c *Ctrl) DeleteService(ctx context.Context) error {
	svc, err := c.contract.GetService(ctx)
	if err != nil && err.Error() != "service not found" {
		return errors.Wrap(err, "get service in contract")
	}
	if svc == nil {
		return nil
	}
	if err := c.contract.DeleteService(ctx); err != nil {
		return errors.Wrap(err, "delete service in contract")
	}

	return nil
}

func (c *Ctrl) SyncServices(ctx context.Context) error {
	if err := c.contract.SyncServices(ctx, c.config.Service); err != nil {
		return errors.Wrap(err, "sync services in contract")
	}
	return nil
}
