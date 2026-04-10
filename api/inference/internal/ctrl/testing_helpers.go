package ctrl

import (
	"net/http"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SeedContractAccountCache pre-seeds the contract account cache for integration testing.
// This avoids contract calls during session and balance validation.
// The address should be the checksummed hex form (common.HexToAddress(addr).Hex()).
func (c *Ctrl) SeedContractAccountCache(address string, account *contract.Account) {
	c.contractAccountCache.Set(address, account, cache.DefaultExpiration)
}

// SeedServiceCache pre-seeds the service cache for integration testing.
// This avoids contract calls when fetching service pricing.
func (c *Ctrl) SeedServiceCache(service model.Service) {
	c.serviceCache.Set("current_service", service, cache.DefaultExpiration)
}

// SetHTTPClient replaces the internal HTTP client for integration testing.
// Use this to inject a client that trusts httptest.NewTLSServer certificates.
func (c *Ctrl) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}
