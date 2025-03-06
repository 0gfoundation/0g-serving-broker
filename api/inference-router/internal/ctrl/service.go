package ctrl

import (
	"context"
	"time"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/inference-router/contract"
	"github.com/0glabs/0g-serving-broker/inference-router/model"
	"github.com/ethereum/go-ethereum/common"
)

func (c *Ctrl) GetService(ctx context.Context, providerAddress string) (model.Service, error) {
	svc, err := c.contract.GetService(ctx, common.HexToAddress(providerAddress))
	if err != nil {
		return model.Service{}, errors.Wrap(err, "get service from contract")
	}

	return parseService(svc), nil
}

func (c *Ctrl) ListService(ctx context.Context) ([]model.Service, error) {
	list, err := c.contract.ListService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "list service from contract")
	}
	ret := make([]model.Service, len(list))
	for i, svc := range list {
		ret[i] = parseService(svc)
	}
	return ret, nil
}

func parseService(svc contract.Service) model.Service {
	return model.Service{
		UpdatedAt:   model.PtrOf(time.Unix(svc.UpdatedAt.Int64(), 0)),
		Provider:    svc.Provider.String(),
		Type:        svc.ServiceType,
		URL:         svc.Url,
		ModelType:   svc.Model,
		InputPrice:  svc.InputPrice.Int64(),
		OutputPrice: svc.OutputPrice.Int64(),
	}
}
