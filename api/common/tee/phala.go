package tee

import (
	"context"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/Dstack-TEE/dstack/sdk/go/tappd"
)

type PhalaTappdClient struct{}

func (c *PhalaTappdClient) TdxQuote(ctx context.Context, jsonData []byte) (*TdxQuoteResponse, error) {
	res, err := tappd.NewTappdClient().TdxQuote(ctx, jsonData)
	if err != nil {
		return nil, errors.Wrap(err, "failed to derive key from Tappd client")
	}

	return &TdxQuoteResponse{
		Quote:    res.Quote,
		EventLog: res.EventLog,
	}, nil
}

func (c *PhalaTappdClient) DeriveKey(ctx context.Context, path string) (string, error) {
	res, err := tappd.NewTappdClient().DeriveKey(ctx, path)
	if err != nil {
		return "", errors.Wrap(err, "failed to derive key from Tappd client")
	}

	return res.Key, nil
}
