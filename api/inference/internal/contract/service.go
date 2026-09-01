package providercontract

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"

	"github.com/0glabs/0g-serving-broker/common/errors"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/common/util"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

var ErrServiceNotFound = errors.New("service not found")

// isServiceNotFoundMessage reports whether an error message came from the
// RPC's "service not found" response.  The RPC can surface the sentinel
// text bare ("service not found") or prefixed by wrapping layers
// ("execution reverted: service not found", "contract call failed: service
// not found").  We accept both forms and reject messages where the phrase
// is merely embedded in a longer, unrelated message — anchoring prevents
// a sibling error like "nested service not found path" from being
// misclassified as "not registered yet" and incorrectly triggering
// first-time registration.
func isServiceNotFoundMessage(msg string) bool {
	sentinel := ErrServiceNotFound.Error()
	if msg == sentinel {
		return true
	}
	return strings.HasSuffix(msg, ": "+sentinel)
}

// DefaultProviderStake is the default stake amount for first-time service registration (100 0G)
var DefaultProviderStake = new(big.Int).Mul(big.NewInt(100), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// buildAdditionalInfo creates the additionalInfo JSON string for a service
func buildAdditionalInfo(service config.Service, imageName, imageDigest string, tieredPricing config.TieredPricingConfig, cacheTokenBilling config.CacheTokenBillingConfig) (string, error) {
	// Determine TEE verifier based on NETWORK environment variable. A standard
	// provider performs no TEE verification, so it publishes no verifier: leaving
	// TEEVerifier/VerifierURL empty means a client has nothing to call even if it
	// somehow reached the verification path (it does not — verifiability "standard"
	// is already skipped client-side).
	var teeVerifier string
	if !service.IsStandard() {
		teeVerifier = tee.VerifierForNetwork(os.Getenv("NETWORK"))
	}

	verifierURL := service.VerifierURL
	if service.IsStandard() {
		verifierURL = ""
	}

	// Create AdditionalInfo JSON string
	additionalInfo := map[string]interface{}{
		"VerifierURL":      verifierURL,
		"TEEVerifier":      teeVerifier,
		"TargetSeparated":  service.TargetSeparated,
		"TargetTeeAddress": "",
		"ImageName":        imageName,
		"ImageDigest":      imageDigest,
	}

	// Set TargetTeeAddress if TargetSeparated is true
	if service.TargetSeparated {
		additionalInfo["TargetTeeAddress"] = service.TargetTeeAddress
	}

	// Publish the provider class for every forwarder (centralized and standard) so
	// on-chain discovery / SDK can identify the provider type. A standard provider
	// still hides its upstream: ProviderIdentity stays centralized-only.
	if service.IsForwarder() {
		additionalInfo["ProviderType"] = service.ProviderType
	}
	if service.IsCentralized() {
		additionalInfo["ProviderIdentity"] = service.ProviderIdentity
	}

	// Include tiered pricing info so user-broker can display tier prices
	if tieredPricing.Enabled && len(tieredPricing.Tiers) > 0 {
		tiers := make([]map[string]interface{}, len(tieredPricing.Tiers))
		for i, t := range tieredPricing.Tiers {
			inNum, inDen := t.EffectiveInputMultiplier()
			outNum, outDen := t.EffectiveOutputMultiplier()
			tier := map[string]interface{}{
				"maxInputTokens":   t.MaxInputTokens,
				"inputMultiplier":  inNum,
				"outputMultiplier": outNum,
			}
			// Publish denominators only when the multiplier is a non-integer
			// fraction, so a legacy integer-only tier's on-chain info is unchanged;
			// consumers treat a missing denominator as 1.
			if inDen != 1 {
				tier["inputMultiplierDenominator"] = inDen
			}
			if outDen != 1 {
				tier["outputMultiplierDenominator"] = outDen
			}
			tiers[i] = tier
		}
		additionalInfo["tieredPricing"] = map[string]interface{}{
			"tiers": tiers,
		}
	}

	// Include cache token billing info so user-broker can display cache hit prices
	if cacheTokenBilling.Enabled && cacheTokenBilling.Divisor > 0 {
		cacheInfo := map[string]interface{}{
			"divisor": cacheTokenBilling.Divisor,
		}
		// Publish the EFFECTIVE cache-write premium fractions billing applies, so
		// user-broker can display cache-write prices (inputPrice * num / den). An
		// unset 1-hour tier falls back to the default multiplier at billing time
		// (see computeInputFee), so publish that fallback value rather than omitting
		// it — otherwise the advertised 1-hour price would understate what is charged.
		if cacheTokenBilling.WriteMultiplierEnabled() {
			cacheInfo["writeMultiplierNumerator"] = cacheTokenBilling.WriteMultiplierNumerator
			cacheInfo["writeMultiplierDenominator"] = cacheTokenBilling.WriteMultiplierDenominator
		}
		switch {
		case cacheTokenBilling.Write1hMultiplierEnabled():
			cacheInfo["write1hMultiplierNumerator"] = cacheTokenBilling.Write1hMultiplierNumerator
			cacheInfo["write1hMultiplierDenominator"] = cacheTokenBilling.Write1hMultiplierDenominator
		case cacheTokenBilling.WriteMultiplierEnabled():
			cacheInfo["write1hMultiplierNumerator"] = cacheTokenBilling.WriteMultiplierNumerator
			cacheInfo["write1hMultiplierDenominator"] = cacheTokenBilling.WriteMultiplierDenominator
		}
		additionalInfo["cacheTokenBilling"] = cacheInfo
	}

	// Multi-model: publish only a compact summary on-chain — the MultiModel flag and
	// the price denomination. The full per-model pricing table is intentionally NOT
	// written to chain: it is served off-chain via GET /v1/models (the actual
	// consumer, the router's catalog sync, reads pricing/canonical/tiers from there,
	// never from chain — its on-chain ServiceAdditionalInfo struct doesn't even have
	// a modelPricing field), and the structured on-chain inputPrice/outputPrice
	// already carries the max-over-models ceiling that bounds the worst-case charge.
	// Enumerating every model here would bloat contract storage (and gas) unboundedly
	// for providers that proxy hundreds of models; this summary stays O(1) regardless
	// of model count. Clients needing exact per-model prices read /v1/models.
	if service.HasMultiModelPricing() {
		additionalInfo["MultiModel"] = true
		additionalInfo["priceDenomination"] = service.PriceDenomination
	}

	additionalInfoJSON, err := json.Marshal(additionalInfo)
	if err != nil {
		return "", errors.Wrap(err, "marshal additional info")
	}

	return string(additionalInfoJSON), nil
}

func (c *ProviderContract) addOrUpdateService(ctx context.Context, service config.Service, teeSignerAddress common.Address, stakeValue *big.Int, additionalInfoJSON string) error {
	c.logger.Infof("[addOrUpdateService] Starting to add or update service - provider=%s, type=%s, url=%s, model=%s, verifiability=%s",
		c.ProviderAddress, service.Type, service.ServingURL, service.ModelType, service.Verifiability)

	c.logger.Infof("[addOrUpdateService] Price information - inputPrice=%s, outputPrice=%s",
		service.InputPrice, service.OutputPrice)

	if stakeValue != nil {
		c.logger.Infof("[addOrUpdateService] First-time registration with stake - stakeValue=%s", stakeValue.String())
	}

	inputPrice, err := util.ConvertToBigInt(service.InputPrice)
	if err != nil {
		c.logger.Errorf("[addOrUpdateService] Failed to convert input price - inputPrice=%s, error=%v", service.InputPrice, err)
		return errors.Wrap(err, "convert input price")
	}
	outputPrice, err := util.ConvertToBigInt(service.OutputPrice)
	if err != nil {
		c.logger.Errorf("[addOrUpdateService] Failed to convert output price - outputPrice=%s, error=%v", service.OutputPrice, err)
		return errors.Wrap(err, "convert input price")
	}

	c.logger.Infof("[addOrUpdateService] Preparing to send transaction to contract - inputPriceWei=%s, outputPriceWei=%s",
		inputPrice.String(), outputPrice.String())

	c.logger.Infof("[addOrUpdateService] Additional info JSON: %s", additionalInfoJSON)
	c.logger.Infof("[addOrUpdateService] Tee signer address: %s", teeSignerAddress.Hex())

	tx, err := c.Contract.TransactWithValue(ctx,
		nil,
		stakeValue,
		"addOrUpdateService",
		contract.ServiceParams{
			ServiceType:   service.Type,
			Url:           service.ServingURL,
			Model:         service.ModelType,
			Verifiability: service.Verifiability,
			InputPrice:    inputPrice,
			OutputPrice:   outputPrice,
			// AdditionalInfo field stores JSON with TEE verifier info, target separation status, and TEE address
			AdditionalInfo:   additionalInfoJSON,
			TeeSignerAddress: teeSignerAddress,
		},
	)

	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[addOrUpdateService] Failed to send transaction - error=%v", wrappedErr)
		return wrappedErr
	}

	c.logger.Infof("[addOrUpdateService] Transaction sent - txHash=%s", tx.Hash().String())
	fmt.Printf("tx hash: %s\n", tx.Hash().String())

	receipt, err := c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[addOrUpdateService] Failed to wait for transaction receipt - txHash=%s, error=%v", tx.Hash().String(), wrappedErr)
		return errors.Wrapf(wrappedErr, "wait for receipt of tx %s", tx.Hash().String())
	}

	c.logger.Infof("[addOrUpdateService] Transaction successful - txHash=%s, blockNumber=%d, gasUsed=%d",
		tx.Hash().String(), receipt.BlockNumber.Uint64(), receipt.GasUsed)

	return nil
}

func (c *ProviderContract) DeleteService(ctx context.Context) error {
	tx, err := c.Contract.Transact(ctx,
		nil,
		"removeService",
	)
	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[DeleteService] Failed to send transaction - error=%v", wrappedErr)
		return wrappedErr
	}
	_, err = c.Contract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[DeleteService] Failed to wait for receipt - txHash=%s, error=%v", tx.Hash().String(), wrappedErr)
		return wrappedErr
	}
	return nil
}

func (c *ProviderContract) GetService(ctx context.Context) (*contract.Service, error) {
	c.logger.Infof("[GetService] Starting to get service - provider=%s", c.ProviderAddress)

	callOpts := &bind.CallOpts{
		Context: ctx,
	}

	service, err := c.Contract.GetService(callOpts, common.HexToAddress(c.ProviderAddress))
	if err != nil {
		// Wrap error to extract details from rpc.jsonError Data field
		wrappedErr := WrapContractError(err)
		c.logger.Errorf("[GetService] Contract error - provider=%s: %v", c.ProviderAddress, wrappedErr)
		// Callers use errors.Is(err, ErrServiceNotFound) to detect the
		// "service not registered yet" case.  The underlying RPC returns
		// the literal text "service not found" — sometimes bare, sometimes
		// prefixed by wrapping ("execution reverted: service not found").
		// Anchor the match so a sibling error whose message merely contains
		// the phrase (e.g. "nested service not found path") isn't
		// misclassified as a not-registered error and accidentally drive
		// first-time registration.
		if isServiceNotFoundMessage(wrappedErr.Error()) {
			return nil, fmt.Errorf("%w: %v", ErrServiceNotFound, wrappedErr)
		}
		return nil, wrappedErr
	}

	c.logger.Infof("[GetService] Retrieved service from contract - url=%s, model=%s, type=%s",
		service.Url, service.Model, service.ServiceType)

	return &service, nil
}

func (c *ProviderContract) SyncService(ctx context.Context, new config.Service, tieredPricing config.TieredPricingConfig, cacheTokenBilling config.CacheTokenBillingConfig) error {
	if new.HasMultiModelPricing() {
		c.logger.Infof("[SyncService] Multi-model pricing configured (%d models), on-chain prices set to max(inputPrice=%s, outputPrice=%s)",
			len(new.ModelPricing), new.InputPrice, new.OutputPrice)
	}
	c.logger.Infof("[SyncService] Starting to sync service - provider=%s, newURL=%s, newModel=%s, newType=%s, inputPrice=%s, outputPrice=%s",
		c.ProviderAddress, new.ServingURL, new.ModelType, new.Type, new.InputPrice, new.OutputPrice)

	old, err := c.GetService(ctx)
	if err != nil && errors.Is(err, ErrServiceNotFound) {
		c.logger.Info("[SyncService] No existing service found in contract")
	} else if old != nil {
		c.logger.Infof("[SyncService] Found existing service - url=%s, model=%s, type=%s, inputPrice=%s, outputPrice=%s",
			old.Url, old.Model, old.ServiceType, old.InputPrice.String(), old.OutputPrice.String())
	}

	if old == nil && new.ServingURL == "" {
		c.logger.Info("[SyncService] No action needed: no old service and new service URL is empty")
		return nil
	}
	if old != nil && new.ServingURL == "" {
		c.logger.Info("[SyncService] Deleting service: new service URL is empty")
		return c.DeleteService(ctx)
	}

	// Get image info if Docker is configured
	imageName, imageDigest := c.GetImageInfo(ctx)

	// Build additionalInfo for comparison
	newAdditionalInfo, err := buildAdditionalInfo(new, imageName, imageDigest, tieredPricing, cacheTokenBilling)
	if err != nil {
		c.logger.Errorf("[SyncService] Failed to build additional info - error=%v", err)
		return err
	}

	if old != nil && identicalService(*old, new, c.TeeSignerAddress, newAdditionalInfo) {
		c.logger.Info("[SyncService] Service is identical, no update needed")
		return nil
	}

	// Determine stake value for first-time registration
	var stakeValue *big.Int
	if old == nil {
		// First-time registration: need to stake
		stakeValue = DefaultProviderStake
		if new.ProviderStake != "" {
			stakeValue, err = util.ConvertToBigInt(new.ProviderStake)
			if err != nil {
				c.logger.Errorf("[SyncService] Failed to convert provider stake - stake=%s, error=%v", new.ProviderStake, err)
				return errors.Wrap(err, "convert provider stake")
			}
		}
		c.logger.Infof("[SyncService] First-time registration, stake amount: %s", stakeValue.String())
	}

	c.logger.Info("[SyncService] Preparing to add or update service to contract")
	if err := c.addOrUpdateService(ctx, new, c.TeeSignerAddress, stakeValue, newAdditionalInfo); err != nil {
		c.logger.Errorf("[SyncService] Failed to add or update service - error=%v", err)
		return errors.Wrap(err, "add or update service in contract")
	}

	c.logger.Info("[SyncService] Service sync successful")
	return nil
}

func identicalService(old contract.Service, new config.Service, teeSignerAddress common.Address, newAdditionalInfo string) bool {
	if !identicalServiceExceptPrice(old, new, teeSignerAddress, newAdditionalInfo) {
		return false
	}
	if old.InputPrice.String() != new.InputPrice {
		return false
	}
	if old.OutputPrice.String() != new.OutputPrice {
		return false
	}
	return true
}

// identicalServiceExceptPrice mirrors identicalService but ignores the
// InputPrice / OutputPrice fields.  Used by the USD-startup drift gate to
// decide whether a pure-rate-drift restart can skip the on-chain push.
func identicalServiceExceptPrice(old contract.Service, new config.Service, teeSignerAddress common.Address, newAdditionalInfo string) bool {
	if old.Model != new.ModelType {
		return false
	}
	if old.Verifiability != new.Verifiability {
		return false
	}
	if old.ServiceType != new.Type {
		return false
	}
	if old.Url != new.ServingURL {
		return false
	}
	if old.TeeSignerAddress != teeSignerAddress {
		return false
	}
	if old.AdditionalInfo != newAdditionalInfo {
		return false
	}
	return true
}

// CompareServiceExceptPrice reports whether the on-chain service matches the
// supplied config across all fields EXCEPT InputPrice / OutputPrice.  It
// returns the current on-chain service so callers can compare prices
// themselves (typically via pricefeed.DriftBps).
//
// If the service is not yet registered on-chain, returns (false, nil,
// ErrServiceNotFound) — callers that want "not equal, no error" should
// check errors.Is(err, ErrServiceNotFound) and translate.
func (c *ProviderContract) CompareServiceExceptPrice(ctx context.Context, new config.Service, tieredPricing config.TieredPricingConfig, cacheTokenBilling config.CacheTokenBillingConfig) (bool, *contract.Service, error) {
	old, err := c.GetService(ctx)
	if err != nil {
		return false, nil, err
	}
	imageName, imageDigest := c.GetImageInfo(ctx)
	newAdditionalInfo, err := buildAdditionalInfo(new, imageName, imageDigest, tieredPricing, cacheTokenBilling)
	if err != nil {
		return false, old, errors.Wrap(err, "build additional info")
	}
	return identicalServiceExceptPrice(*old, new, c.TeeSignerAddress, newAdditionalInfo), old, nil
}
