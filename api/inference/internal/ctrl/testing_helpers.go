package ctrl

import (
	"fmt"
	"net/http"
	"time"

	"github.com/patrickmn/go-cache"

	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/inference/contract"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// SetTeeServiceForTest injects a TeeService so tests in other packages (e.g. the
// proxy) can exercise the E2EE unseal path, which reads the unexported field.
func (c *Ctrl) SetTeeServiceForTest(ts *tee.TeeService) {
	c.teeService = ts
}

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

// SetupImageStoreForTest initialises a local image store at dir for proxy-level
// tests that exercise handleImageServeRoute without a full request flow.
func (c *Ctrl) SetupImageStoreForTest(dir string) error {
	store, err := newImageStore(dir, time.Minute)
	if err != nil {
		return fmt.Errorf("setup image store: %w", err)
	}
	c.imageStore = store
	return nil
}

// StoreTestImage saves image bytes into the image store. Call SetupImageStoreForTest first.
func (c *Ctrl) StoreTestImage(chatKey string, images [][]byte) error {
	if c.imageStore == nil {
		return fmt.Errorf("image store not initialised, call SetupImageStoreForTest first")
	}
	return c.imageStore.store(chatKey, images)
}
