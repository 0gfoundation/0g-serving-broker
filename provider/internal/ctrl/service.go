package ctrl

import (
	"context"
	"log"

	"github.com/0glabs/0g-serving-agent/common/errors"
	"github.com/0glabs/0g-serving-agent/provider/internal/db"
	"github.com/0glabs/0g-serving-agent/provider/model"
)

func (c *Ctrl) RegisterService(ctx context.Context, service model.Service) error {
	_, err := c.GetService(service.Name)
	if db.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "get service from db")
	}
	if err == nil {
		return errors.New("service already exists")
	}
	if err := c.contract.AddOrUpdateService(ctx, service, c.servingUrl); err != nil {
		return errors.Wrap(err, "add service in contract")
	}
	err = c.db.AddService(service)
	if err != nil {
		if rollBackErr := c.contract.DeleteService(ctx, service.Name); rollBackErr != nil {
			log.Printf("rolling back service in the contract: %s", rollBackErr.Error())
		}
	}
	return errors.Wrap(err, "add service in db")
}

func (c *Ctrl) UpdateService(ctx context.Context, service model.Service) error {
	if err := c.contract.AddOrUpdateService(ctx, service, c.servingUrl); err != nil {
		return errors.Wrap(err, "add service in contract")
	}
	old, err := c.GetService(service.Name)
	if err != nil {
		return err
	}
	err = c.db.AddService(service)
	if err != nil {
		if rollBackErr := c.contract.AddOrUpdateService(ctx, old, c.servingUrl); rollBackErr != nil {
			log.Printf("rolling back service in the contract: %s", rollBackErr.Error())
		}
	}
	return errors.Wrap(err, "add service in db")
}

func (c *Ctrl) GetService(name string) (model.Service, error) {
	svc, err := c.db.GetService(name)
	return svc, errors.Wrap(err, "get service from db")
}

func (c *Ctrl) ListService() ([]model.Service, error) {
	list, err := c.db.ListService()
	if err != nil {
		return nil, errors.Wrap(err, "list service from db")
	}
	return list, nil
}

func (c *Ctrl) DeleteService(ctx context.Context, name string) error {
	if err := c.contract.DeleteService(ctx, name); err != nil {
		return errors.Wrap(err, "delete service in contract")
	}

	return errors.Wrapf(c.db.DeleteService(name), "delete service %s in db", name)
}
