package ctrl

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// Ctrl is the controller for managing containers and configs
type Ctrl struct {
	config       config.ControllerConfig
	dockerClient *docker.Client

	// Dynamic whitelists (protected by mutex)
	mu             sync.RWMutex
	adminAddresses map[string]bool
	allowedIPs     map[string]bool
}

// NewCtrl creates a new controller
func NewCtrl(cfg config.ControllerConfig) (*Ctrl, error) {
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

	return &Ctrl{
		config:         cfg,
		dockerClient:   dockerClient,
		adminAddresses: adminAddresses,
		allowedIPs:     allowedIPs,
	}, nil
}

// Close closes the controller resources
func (c *Ctrl) Close() error {
	return c.dockerClient.Close()
}

// IsAdminAddress checks if an address is in the admin whitelist
func (c *Ctrl) IsAdminAddress(addr string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.adminAddresses[strings.ToLower(addr)]
}

// GetAdminAddresses returns all admin addresses
func (c *Ctrl) GetAdminAddresses() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	addrs := make([]string, 0, len(c.adminAddresses))
	for addr := range c.adminAddresses {
		addrs = append(addrs, addr)
	}
	return addrs
}

// AddAdminAddress adds an address to the admin whitelist
func (c *Ctrl) AddAdminAddress(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adminAddresses[strings.ToLower(addr)] = true
}

// RemoveAdminAddress removes an address from the admin whitelist
// Returns false if it's the last admin address
func (c *Ctrl) RemoveAdminAddress(addr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Prevent removing the last admin
	if len(c.adminAddresses) <= 1 {
		return false
	}

	delete(c.adminAddresses, strings.ToLower(addr))
	return true
}

// GetAllowedIPs returns all allowed IPs
func (c *Ctrl) GetAllowedIPs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ips := make([]string, 0, len(c.allowedIPs))
	for ip := range c.allowedIPs {
		ips = append(ips, ip)
	}
	return ips
}

// AddAllowedIP adds an IP to the whitelist
func (c *Ctrl) AddAllowedIP(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowedIPs[ip] = true
}

// RemoveAllowedIP removes an IP from the whitelist
func (c *Ctrl) RemoveAllowedIP(ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.allowedIPs, ip)
}

// GetContainerStatus gets the status of a specific container
func (c *Ctrl) GetContainerStatus(ctx context.Context, alias string) (*docker.ContainerStatus, error) {
	containerConfig := c.dockerClient.GetContainerConfig(alias)
	if containerConfig == nil {
		return nil, &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.GetContainerStatus(ctx, containerConfig.Name)
}

// GetAllContainersStatus gets the status of all managed containers
func (c *Ctrl) GetAllContainersStatus(ctx context.Context) ([]docker.ContainerStatus, error) {
	return c.dockerClient.GetAllContainersStatus(ctx)
}

// StartContainer starts a container by alias (broker/event)
func (c *Ctrl) StartContainer(ctx context.Context, alias string) error {
	containerConfig := c.dockerClient.GetContainerConfig(alias)
	if containerConfig == nil {
		return &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.StartContainer(ctx, containerConfig.Name)
}

// StopContainer stops a container by alias
func (c *Ctrl) StopContainer(ctx context.Context, alias string) error {
	containerConfig := c.dockerClient.GetContainerConfig(alias)
	if containerConfig == nil {
		return &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.StopContainer(ctx, containerConfig.Name)
}

// RestartContainer restarts a container by alias
func (c *Ctrl) RestartContainer(ctx context.Context, alias string) error {
	containerConfig := c.dockerClient.GetContainerConfig(alias)
	if containerConfig == nil {
		return &InvalidContainerError{Alias: alias}
	}

	return c.dockerClient.RestartContainer(ctx, containerConfig.Name)
}

// GetContainerConfig reads the shared config file and returns its content
// The alias is just for validation (broker/event)
func (c *Ctrl) GetContainerConfig(alias string) (string, error) {
	// Validate alias
	if alias != "broker" && alias != "event" {
		return "", &InvalidContainerError{Alias: alias}
	}

	// All containers share the same config file
	data, err := os.ReadFile(c.config.ConfigFile)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// UpdateContainerConfig updates the shared config file
// The alias is just for validation (broker/event)
// configContent is raw YAML string to avoid parsing issues with hex addresses
func (c *Ctrl) UpdateContainerConfig(alias string, configContent string) error {
	// Validate alias
	if alias != "broker" && alias != "event" {
		return &InvalidContainerError{Alias: alias}
	}

	// Validate YAML format (but don't use parsed result to preserve original content)
	var tmp interface{}
	if err := yaml.Unmarshal([]byte(configContent), &tmp); err != nil {
		return &InvalidConfigError{Err: err}
	}

	return os.WriteFile(c.config.ConfigFile, []byte(configContent), 0644)
}

// ApplyContainerConfig updates config and restarts the container
func (c *Ctrl) ApplyContainerConfig(ctx context.Context, alias string, configContent string) error {
	if err := c.UpdateContainerConfig(alias, configContent); err != nil {
		return err
	}

	return c.RestartContainer(ctx, alias)
}

// InvalidContainerError is returned when an invalid container alias is provided
type InvalidContainerError struct {
	Alias string
}

func (e *InvalidContainerError) Error() string {
	return "invalid container alias: " + e.Alias + ", must be 'broker' or 'event'"
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

// UpdateImages pulls the latest image and recreates broker and event containers
func (c *Ctrl) UpdateImages(ctx context.Context) (*docker.ImageUpdateResult, error) {
	result := &docker.ImageUpdateResult{
		Image:             c.config.Image,
		UpdatedContainers: make([]docker.ContainerUpdateResult, 0),
	}

	// Step 1: Pull the latest image
	imageID, err := c.dockerClient.PullImage(ctx, c.config.Image)
	if err != nil {
		result.Success = false
		result.Error = "failed to pull image: " + err.Error()
		return result, err
	}
	result.ImageID = imageID

	// Step 2: Stop containers in reverse dependency order (event -> broker)
	// First stop event
	eventConfig := c.dockerClient.GetContainerConfig("event")
	if eventConfig != nil {
		if err := c.dockerClient.StopContainer(ctx, eventConfig.Name); err != nil {
			// Log error but continue - container might not exist
			if _, ok := err.(*docker.ContainerNotFoundError); !ok {
				result.Success = false
				result.Error = "failed to stop event container: " + err.Error()
				return result, err
			}
		}
	}

	// Then stop broker
	brokerConfig := c.dockerClient.GetContainerConfig("broker")
	if brokerConfig != nil {
		if err := c.dockerClient.StopContainer(ctx, brokerConfig.Name); err != nil {
			if _, ok := err.(*docker.ContainerNotFoundError); !ok {
				result.Success = false
				result.Error = "failed to stop broker container: " + err.Error()
				return result, err
			}
		}
	}

	// Step 3: Recreate containers in dependency order (broker -> event)
	// First recreate broker
	if brokerConfig != nil {
		brokerResult, err := c.dockerClient.RecreateContainer(ctx, brokerConfig.Name, c.config.Image)
		if brokerResult != nil {
			result.UpdatedContainers = append(result.UpdatedContainers, *brokerResult)
		}
		if err != nil {
			result.Success = false
			result.Error = "failed to recreate broker container: " + err.Error()
			return result, err
		}

		// Wait for broker to become healthy before starting event
		if err := c.dockerClient.WaitForHealthy(ctx, brokerConfig.Name, 2*time.Minute); err != nil {
			result.Success = false
			result.Error = "broker container failed to become healthy: " + err.Error()
			return result, err
		}
	}

	// Then recreate event
	if eventConfig != nil {
		eventResult, err := c.dockerClient.RecreateContainer(ctx, eventConfig.Name, c.config.Image)
		if eventResult != nil {
			result.UpdatedContainers = append(result.UpdatedContainers, *eventResult)
		}
		if err != nil {
			result.Success = false
			result.Error = "failed to recreate event container: " + err.Error()
			return result, err
		}
	}

	result.Success = true
	return result, nil
}

// GetConfiguredImage returns the configured image name
func (c *Ctrl) GetConfiguredImage() string {
	return c.config.Image
}
