package ctrl

import (
	"context"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (c *Ctrl) GetService(ctx context.Context) (model.Service, error) {
	svc, err := c.contract.GetService(ctx)
	if err != nil {
		return model.Service{}, errors.Wrap(err, "list service from contract")
	}
	return parseService(*svc), nil
}

func (c *Ctrl) SyncService(ctx context.Context) error {
	if err := c.contract.SyncService(ctx, c.Service); err != nil {
		return errors.Wrap(err, "sync services")
	}
	return nil
}

func parseService(svc contract.Service) model.Service {
	return model.Service{
		Model: model.Model{
			CreatedAt: model.PtrOf(time.Unix(svc.UpdatedAt.Int64(), 0)),
			UpdatedAt: model.PtrOf(time.Unix(svc.UpdatedAt.Int64(), 0)),
		},
		Type:          svc.ServiceType,
		URL:           svc.Url,
		ModelType:     svc.Model,
		InputPrice:    svc.InputPrice.String(),
		OutputPrice:   svc.OutputPrice.String(),
		Verifiability: svc.Verifiability,
	}
}
