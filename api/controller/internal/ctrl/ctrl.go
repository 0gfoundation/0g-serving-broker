package ctrl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"gopkg.in/yaml.v3"

	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

// Names of the containers the controller manages.
//
// Constants rather than configuration: ApplyCoreConfig rewrites the same config
// file the controller loads, so a name read from there is editable through the
// controller's own API.
const (
	containerBroker         = "0g-serving-provider-broker"
	containerEvent          = "0g-serving-provider-event"
	containerIngress        = "broker-ingress"
	containerPrometheusInit = "prometheus-init"
	containerPrometheus     = "prometheus"
)

// Ctrl is the controller for managing containers and configs
type Ctrl struct {
	config       config.ControllerConfig
	fullConfig   *config.Config // Full config for accessing Service configuration
	dockerClient *docker.Client

	// Contract for syncing services
	servingContract *contract.ServingContract
	providerAddress string
	logger          log.Logger

	// Both fixed at startup; no API mutates either. middleware.AuthMiddleware
	// gates the whole /v1 group on the wallet list, so a route that edited it
	// would sit inside the boundary it enforces.
	//
	// adminAddresses is read per request by middleware.AuthMiddleware.
	// allowedIPs is read by nothing that filters — IPWhitelistMiddleware holds
	// its own snapshot of the config slice.
	adminAddresses map[string]bool
	allowedIPs     map[string]bool
}

// NewCtrl creates a new controller
func NewCtrl(fullConfig *config.Config, logger log.Logger) (*Ctrl, error) {
	cfg := fullConfig.Controller

	dockerClient, err := docker.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	// Initialize admin addresses map
	adminAddresses := make(map[string]bool)
	for _, addr := range cfg.AdminAddresses {
		adminAddresses[strings.ToLower(addr)] = true
	}

	// Initialize allowed IPs map
	allowedIPs := make(map[string]bool)
	for _, ip := range cfg.AllowedIPs {
		allowedIPs[ip] = true
	}

	ctrl := &Ctrl{
		config:         cfg,
		fullConfig:     fullConfig,
		dockerClient:   dockerClient,
		adminAddresses: adminAddresses,
		allowedIPs:     allowedIPs,
		logger:         logger,
	}

	// Initialize ServingContract for deleting services (required)
	if fullConfig.ContractAddress == "" {
		return nil, fmt.Errorf("contract address is required for controller")
	}
	if fullConfig.Network.URL == "" {
		return nil, fmt.Errorf("network configuration is required for controller")
	}

	servingContract, err := contract.NewServingContract(
		common.HexToAddress(fullConfig.ContractAddress),
		&fullConfig.Network,
		fullConfig.GasPrice,
		fullConfig.MaxGasPrice,
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize ServingContract for controller: %w", err)
	}

	wallets, err := servingContract.Client.Network.Wallets()
	if err != nil {
		servingContract.Close()
		return nil, fmt.Errorf("failed to get wallets for controller: %w", err)
	}

	ctrl.servingContract = servingContract
	ctrl.providerAddress = wallets.Default().Address()
	logger.Infof("ServingContract initialized for controller. Provider address: %s", ctrl.providerAddress)

	return ctrl, nil
}

// Close closes the controller resources
func (c *Ctrl) Close() error {
	if c.servingContract != nil {
		c.servingContract.Close()
	}
	return c.dockerClient.Close()
}

// IsAdminAddress checks if an address is in the admin whitelist
func (c *Ctrl) IsAdminAddress(addr string) bool {
	return c.adminAddresses[strings.ToLower(addr)]
}

// GetAdminAddresses returns all admin addresses
func (c *Ctrl) GetAdminAddresses() []string {
	addrs := make([]string, 0, len(c.adminAddresses))
	for addr := range c.adminAddresses {
		addrs = append(addrs, addr)
	}
	return addrs
}

// GetAllowedIPs returns the configured IP whitelist as map keys, so the order
// differs between calls and duplicates are collapsed.
//
// Reporting only, and not the same list as the enforced one: enforcement uses
// IPWhitelistMiddleware's own snapshot, which trims each entry and keeps only
// those that parse as an IP or a CIDR.
func (c *Ctrl) GetAllowedIPs() []string {
	ips := make([]string, 0, len(c.allowedIPs))
	for ip := range c.allowedIPs {
		ips = append(ips, ip)
	}
	return ips
}

// getContainerName returns the container name for a given alias, or "" if the
// alias names nothing the controller manages.
func (c *Ctrl) getContainerName(alias string) string {
	switch alias {
	case "broker":
		return containerBroker
	case "event":
		return containerEvent
	case "ingress":
		return containerIngress
	case "prometheus-init":
		return containerPrometheusInit
	case "prometheus":
		return containerPrometheus
	default:
		return ""
	}
}

// GetAllManagedContainerAliases returns all managed container aliases
func (c *Ctrl) GetAllManagedContainerAliases() []string {
	return []string{"broker", "event", "ingress", "prometheus-init", "prometheus"}
}

// GetContainerStatus gets the status of a specific container
func (c *Ctrl) GetContainerStatus(ctx context.Context, alias string) (*docker.ContainerStatus, error) {
	containerName := c.getContainerName(alias)
	if containerName == "" {
		return nil, &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.GetContainerStatus(ctx, containerName)
}

// GetAllContainersStatus gets the status of all managed containers
func (c *Ctrl) GetAllContainersStatus(ctx context.Context) ([]docker.ContainerStatus, error) {
	var statuses []docker.ContainerStatus

	// Get all container aliases
	aliases := c.GetAllManagedContainerAliases()

	for _, alias := range aliases {
		containerName := c.getContainerName(alias)
		if containerName == "" {
			continue
		}

		status, err := c.dockerClient.GetContainerStatus(ctx, containerName)
		if err != nil {
			// Log error but continue to get other containers
			c.logger.Warnf("Failed to get status for container %s: %v", alias, err)
			continue
		}
		if status != nil {
			statuses = append(statuses, *status)
		}
	}

	return statuses, nil
}

// StartContainer starts a container by alias
func (c *Ctrl) StartContainer(ctx context.Context, alias string) error {
	containerName := c.getContainerName(alias)
	if containerName == "" {
		return &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.StartContainer(ctx, containerName)
}

// StopContainer stops a container by alias
func (c *Ctrl) StopContainer(ctx context.Context, alias string) error {
	containerName := c.getContainerName(alias)
	if containerName == "" {
		return &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.StopContainer(ctx, containerName)
}

// RestartContainer restarts a container by alias
func (c *Ctrl) RestartContainer(ctx context.Context, alias string) error {
	containerName := c.getContainerName(alias)
	if containerName == "" {
		return &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.RestartContainer(ctx, containerName)
}

// GetCoreConfig reads the shared config file and returns its content
// The core config is shared by broker and event containers
func (c *Ctrl) GetCoreConfig() (string, error) {
	data, err := os.ReadFile(c.config.ConfigFile)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// ApplyCoreConfig updates the shared config file and restarts both broker and event containers
// configContent is raw YAML string to avoid parsing issues with hex addresses
func (c *Ctrl) ApplyCoreConfig(ctx context.Context, configContent string) error {
	// Validate YAML format (but don't use parsed result to preserve original content)
	var tmp interface{}
	if err := yaml.Unmarshal([]byte(configContent), &tmp); err != nil {
		return &InvalidConfigError{Err: err}
	}

	if err := os.WriteFile(c.config.ConfigFile, []byte(configContent), 0644); err != nil {
		return err
	}

	// Restart both broker and event since they share the config
	if err := c.RestartContainer(ctx, "broker"); err != nil {
		return fmt.Errorf("failed to restart broker: %w", err)
	}
	if err := c.RestartContainer(ctx, "event"); err != nil {
		return fmt.Errorf("failed to restart event: %w", err)
	}

	return nil
}

// InvalidContainerError is returned when an invalid container alias is provided
type InvalidContainerError struct {
	Alias string
}

func (e *InvalidContainerError) Error() string {
	return "invalid container alias: " + e.Alias
}

// ForbiddenEnvKeyError is returned when trying to modify an env key not in the whitelist
type ForbiddenEnvKeyError struct {
	Key     string
	Allowed []string
}

func (e *ForbiddenEnvKeyError) Error() string {
	return fmt.Sprintf("environment variable '%s' is not allowed, allowed keys: %v", e.Key, e.Allowed)
}

// InvalidConfigError is returned when the config content is not valid YAML
type InvalidConfigError struct {
	Err error
}

func (e *InvalidConfigError) Error() string {
	return "invalid config format: " + e.Err.Error()
}

// GetImageInfo returns information about the current image
func (c *Ctrl) GetImageInfo(ctx context.Context) (*docker.ImageInfo, error) {
	return c.dockerClient.GetImageInfo(ctx, c.config.Image)
}

// GetService gets the current service from the contract
func (c *Ctrl) GetService(ctx context.Context) (*contract.Service, error) {
	callOpts := &bind.CallOpts{Context: ctx}
	providerAddr := common.HexToAddress(c.providerAddress)

	service, err := c.servingContract.GetService(callOpts, providerAddr)
	if err != nil {
		// Check if the error indicates service not found
		if strings.Contains(err.Error(), "ServiceNotExist") ||
			strings.Contains(err.Error(), "service not found") {
			return nil, nil
		}
		return nil, err
	}
	return &service, nil
}

// updateAdditionalInfoImageFields updates only ImageName and ImageDigest in additionalInfo JSON
func updateAdditionalInfoImageFields(oldAdditionalInfo, imageName, imageDigest string) (string, error) {
	// Parse existing additionalInfo
	var info map[string]interface{}
	if oldAdditionalInfo != "" {
		if err := json.Unmarshal([]byte(oldAdditionalInfo), &info); err != nil {
			return "", fmt.Errorf("failed to parse old additionalInfo: %w", err)
		}
	} else {
		info = make(map[string]interface{})
	}

	// Only update image-related fields
	info["ImageName"] = imageName
	info["ImageDigest"] = imageDigest

	newAdditionalInfo, err := json.Marshal(info)
	if err != nil {
		return "", fmt.Errorf("failed to marshal additionalInfo: %w", err)
	}

	return string(newAdditionalInfo), nil
}

// SyncService syncs the service in the contract with the new image digest
// Only updates ImageName and ImageDigest, all other fields remain unchanged
func (c *Ctrl) SyncService(ctx context.Context, imageName, imageDigest string) error {
	c.logger.Infof("[SyncService] Starting to sync service - provider=%s", c.providerAddress)

	// Get existing service from contract
	old, err := c.GetService(ctx)
	if err != nil {
		return fmt.Errorf("failed to get existing service: %w", err)
	}

	if old == nil {
		c.logger.Info("[SyncService] No existing service found in contract, nothing to update")
		return nil
	}

	c.logger.Infof("[SyncService] Found existing service - url=%s, model=%s, type=%s",
		old.Url, old.Model, old.ServiceType)

	// Update only image fields in additionalInfo
	newAdditionalInfo, err := updateAdditionalInfoImageFields(old.AdditionalInfo, imageName, imageDigest)
	if err != nil {
		return fmt.Errorf("failed to update additionalInfo: %w", err)
	}

	// Check if additionalInfo changed
	if old.AdditionalInfo == newAdditionalInfo {
		c.logger.Info("[SyncService] Image info unchanged, no update needed")
		return nil
	}

	c.logger.Infof("[SyncService] Updating service with new image info - ImageName=%s, ImageDigest=%s",
		imageName, imageDigest)

	// Call addOrUpdateService with all old values except additionalInfo
	tx, err := c.servingContract.TransactWithValue(ctx,
		nil,
		nil, // No stake needed for update
		"addOrUpdateService",
		contract.ServiceParams{
			ServiceType:      old.ServiceType,
			Url:              old.Url,
			Model:            old.Model,
			Verifiability:    old.Verifiability,
			InputPrice:       old.InputPrice,
			OutputPrice:      old.OutputPrice,
			AdditionalInfo:   newAdditionalInfo,
			TeeSignerAddress: old.TeeSignerAddress,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to send transaction: %w", err)
	}

	c.logger.Infof("[SyncService] Transaction sent - txHash=%s", tx.Hash().String())

	receipt, err := c.servingContract.WaitForReceipt(ctx, tx.Hash())
	if err != nil {
		return fmt.Errorf("failed to wait for receipt: %w", err)
	}

	c.logger.Infof("[SyncService] Service sync successful - txHash=%s, blockNumber=%d, gasUsed=%d",
		tx.Hash().String(), receipt.BlockNumber.Uint64(), receipt.GasUsed)

	return nil
}

// UpdateImages pulls the latest image and recreates broker and event containers
// The order of operations ensures contract is only updated after containers are successfully recreated:
// 1. Pull image
// 2. Stop containers (event -> broker)
// 3. Recreate containers (broker -> event)
// 4. Sync service to contract (only after containers are running with new image)
func (c *Ctrl) UpdateImages(ctx context.Context) (*docker.ImageUpdateResult, error) {
	result := &docker.ImageUpdateResult{
		Image:             c.config.Image,
		UpdatedContainers: make([]docker.ContainerUpdateResult, 0),
	}

	brokerName := containerBroker
	eventName := containerEvent
	ingressName := containerIngress

	// Step 1: Pull the latest image
	c.logger.Info("[UpdateImages] Pulling latest image...")
	imageInfo, err := c.dockerClient.PullImage(ctx, c.config.Image)
	if err != nil {
		result.Success = false
		result.Error = "failed to pull image: " + err.Error()
		return result, err
	}
	result.ImageID = imageInfo.ImageID
	result.Digest = imageInfo.Digest
	c.logger.Infof("[UpdateImages] Image pulled - ID=%s, Digest=%s", imageInfo.ImageID, imageInfo.Digest)

	// Step 2: Stop containers in reverse dependency order (event -> broker)
	c.logger.Info("[UpdateImages] Stopping containers...")
	if err := c.dockerClient.StopContainer(ctx, eventName); err != nil {
		// Log error but continue - container might not exist
		if _, ok := err.(*docker.ContainerNotFoundError); !ok {
			result.Success = false
			result.Error = "failed to stop event container: " + err.Error()
			return result, err
		}
	}

	// Then stop broker
	if err := c.dockerClient.StopContainer(ctx, brokerName); err != nil {
		if _, ok := err.(*docker.ContainerNotFoundError); !ok {
			result.Success = false
			result.Error = "failed to stop broker container: " + err.Error()
			return result, err
		}
	}

	// Step 3: Recreate containers in dependency order (broker -> event)
	// First recreate broker
	brokerResult, err := c.dockerClient.RecreateContainer(ctx, brokerName, c.config.Image)
	if brokerResult != nil {
		result.UpdatedContainers = append(result.UpdatedContainers, *brokerResult)
	}
	if err != nil {
		result.Success = false
		result.Error = "failed to recreate broker container: " + err.Error()
		return result, err
	}

	// Wait for broker to become healthy before starting event
	if err := c.dockerClient.WaitForHealthy(ctx, brokerName, 2*time.Minute); err != nil {
		result.Success = false
		result.Error = "broker container failed to become healthy: " + err.Error()
		return result, err
	}

	// Reload ingress container (to re-resolve broker's new IP)
	c.logger.Infof("[UpdateImages] Reloading ingress container: %s", ingressName)
	if err := c.dockerClient.ReloadNginx(ctx, ingressName); err != nil {
		// Warn but don't fail: the deployment may have no ingress. This also
		// swallows a SelfOperationError on the ingress name, while the update
		// still reports success. Narrowing it would change what a successful
		// update means to callers; tracked separately.
		c.logger.Warnf("[UpdateImages] Failed to reload ingress container %s: %v", ingressName, err)
	} else {
		c.logger.Info("[UpdateImages] Ingress container reloaded successfully")
	}

	// Then recreate event
	eventResult, err := c.dockerClient.RecreateContainer(ctx, eventName, c.config.Image)
	if eventResult != nil {
		result.UpdatedContainers = append(result.UpdatedContainers, *eventResult)
	}
	if err != nil {
		result.Success = false
		result.Error = "failed to recreate event container: " + err.Error()
		return result, err
	}

	// Step 4: Sync service in the contract with new image digest
	// This is done AFTER containers are successfully recreated to ensure
	// the contract always reflects the actual running state
	c.logger.Info("[UpdateImages] Syncing service with new image digest...")
	if err := c.SyncService(ctx, c.config.Image, imageInfo.Digest); err != nil {
		result.Success = false
		result.Error = "failed to sync service: " + err.Error()
		return result, err
	}

	result.Success = true
	return result, nil
}

// GetConfiguredImage returns the configured image name
func (c *Ctrl) GetConfiguredImage() string {
	return c.config.Image
}

// UpdatePrometheusConfig updates the Prometheus configuration
// It reruns the prometheus-init container with the new config and restarts prometheus
func (c *Ctrl) UpdatePrometheusConfig(ctx context.Context, base64Config string) error {
	// 1. Validate base64 format
	if _, err := base64.StdEncoding.DecodeString(base64Config); err != nil {
		return &InvalidConfigError{Err: fmt.Errorf("invalid base64 encoding: %w", err)}
	}

	initContainer := containerPrometheusInit
	promContainer := containerPrometheus

	c.logger.Infof("[UpdatePrometheusConfig] Rerunning %s with new config", initContainer)

	// 2. Rerun prometheus-init container with new PROMETHEUS_CONFIG env
	err := c.dockerClient.RerunContainerWithEnv(ctx, initContainer, map[string]string{
		"PROMETHEUS_CONFIG": base64Config,
	})
	if err != nil {
		return fmt.Errorf("failed to rerun prometheus-init: %w", err)
	}

	// 3. Restart prometheus container to load new config
	c.logger.Infof("[UpdatePrometheusConfig] Restarting %s", promContainer)
	if err := c.dockerClient.RestartContainer(ctx, promContainer); err != nil {
		return fmt.Errorf("failed to restart prometheus: %w", err)
	}

	c.logger.Info("[UpdatePrometheusConfig] Prometheus config updated successfully")
	return nil
}

// UpdateIngressConfig updates the ingress container environment variables
// Validates env keys against the IngressAllowedEnvKeys whitelist
func (c *Ctrl) UpdateIngressConfig(ctx context.Context, envUpdates map[string]string) error {
	// Validate env keys against whitelist
	for key := range envUpdates {
		allowed := false
		for _, allowedKey := range config.IngressAllowedEnvKeys {
			if key == allowedKey {
				allowed = true
				break
			}
		}
		if !allowed {
			return &ForbiddenEnvKeyError{Key: key, Allowed: config.IngressAllowedEnvKeys}
		}
	}

	ingressName := containerIngress
	c.logger.Infof("[UpdateIngressConfig] Updating ingress container %s with env keys: %v", ingressName, mapKeys(envUpdates))

	if err := c.dockerClient.UpdateContainerEnv(ctx, ingressName, envUpdates); err != nil {
		return fmt.Errorf("failed to update ingress container: %w", err)
	}

	c.logger.Info("[UpdateIngressConfig] Ingress config updated successfully")
	return nil
}

// GetIngressEnv returns the current environment variables of the ingress container
func (c *Ctrl) GetIngressEnv(ctx context.Context) (map[string]string, error) {
	ingressName := containerIngress
	return c.dockerClient.GetContainerEnv(ctx, ingressName, config.IngressAllowedEnvKeys)
}

// GetPrometheusConfig returns the current Prometheus configuration (base64 encoded)
func (c *Ctrl) GetPrometheusConfig(ctx context.Context) (string, error) {
	initContainer := containerPrometheusInit
	env, err := c.dockerClient.GetContainerEnv(ctx, initContainer, []string{"PROMETHEUS_CONFIG"})
	if err != nil {
		return "", err
	}
	return env["PROMETHEUS_CONFIG"], nil
}

// mapKeys returns the keys of a map as a slice
func mapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
