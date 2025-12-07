package docker

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// ContainerStatus represents the status of a container
type ContainerStatus struct {
	Name      string `json:"name"`
	ID        string `json:"id"`
	State     string `json:"state"`  // running, exited, paused, etc.
	Health    string `json:"health"` // healthy, unhealthy, starting, none
	StartedAt string `json:"startedAt"`
	Image     string `json:"image"`
}

// Client wraps the Docker client
type Client struct {
	cli    *client.Client
	config config.ControllerConfig
}

// NewClient creates a new Docker client
func NewClient(cfg config.ControllerConfig) (*Client, error) {
	opts := []client.Opt{
		client.WithHost(cfg.Docker.Host),
		client.WithAPIVersionNegotiation(),
	}

	if cfg.Docker.APIVersion != "" {
		opts = append(opts, client.WithVersion(cfg.Docker.APIVersion))
	}

	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, err
	}

	return &Client{
		cli:    cli,
		config: cfg,
	}, nil
}

// Close closes the Docker client
func (c *Client) Close() error {
	return c.cli.Close()
}

// GetContainerStatus gets the status of a container by name
// The containerName can be a partial match (e.g., "broker" matches "project-0g-serving-provider-broker-1")
func (c *Client) GetContainerStatus(ctx context.Context, containerName string) (*ContainerStatus, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	for _, cont := range containers {
		for _, name := range cont.Names {
			// Container names have a leading slash, remove it for matching
			cleanName := strings.TrimPrefix(name, "/")
			// Match if name equals containerName or contains it (for docker-compose naming)
			if cleanName == containerName || strings.Contains(cleanName, containerName) {
				// Get detailed inspection
				inspect, err := c.cli.ContainerInspect(ctx, cont.ID)
				if err != nil {
					return nil, err
				}

				health := "none"
				if inspect.State.Health != nil {
					health = inspect.State.Health.Status
				}

				return &ContainerStatus{
					Name:      cleanName, // Return actual container name
					ID:        cont.ID[:12],
					State:     inspect.State.Status,
					Health:    health,
					StartedAt: inspect.State.StartedAt,
					Image:     cont.Image,
				}, nil
			}
		}
	}

	return nil, nil
}

// GetAllContainersStatus gets status of all managed containers
func (c *Client) GetAllContainersStatus(ctx context.Context) ([]ContainerStatus, error) {
	var statuses []ContainerStatus

	// Get broker status
	brokerStatus, err := c.GetContainerStatus(ctx, c.config.Containers.Broker.Name)
	if err != nil {
		return nil, err
	}
	if brokerStatus != nil {
		statuses = append(statuses, *brokerStatus)
	}

	// Get event status
	eventStatus, err := c.GetContainerStatus(ctx, c.config.Containers.Event.Name)
	if err != nil {
		return nil, err
	}
	if eventStatus != nil {
		statuses = append(statuses, *eventStatus)
	}

	return statuses, nil
}

// StartContainer starts a container by name
func (c *Client) StartContainer(ctx context.Context, containerName string) error {
	containerID, err := c.getContainerID(ctx, containerName)
	if err != nil {
		return err
	}

	return c.cli.ContainerStart(ctx, containerID, container.StartOptions{})
}

// StopContainer stops a container by name
func (c *Client) StopContainer(ctx context.Context, containerName string) error {
	containerID, err := c.getContainerID(ctx, containerName)
	if err != nil {
		return err
	}

	timeout := 30 // seconds
	return c.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// RestartContainer restarts a container by name
func (c *Client) RestartContainer(ctx context.Context, containerName string) error {
	containerID, err := c.getContainerID(ctx, containerName)
	if err != nil {
		return err
	}

	timeout := 30 // seconds
	return c.cli.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout})
}

// GetContainerLogs gets logs from a container
func (c *Client) GetContainerLogs(ctx context.Context, containerName string, tail string) (string, error) {
	containerID, err := c.getContainerID(ctx, containerName)
	if err != nil {
		return "", err
	}

	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tail,
	}

	reader, err := c.cli.ContainerLogs(ctx, containerID, options)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	logs, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	return string(logs), nil
}

// getContainerID gets container ID by name
// The containerName can be a partial match (e.g., "broker" matches "project-0g-serving-provider-broker-1")
func (c *Client) getContainerID(ctx context.Context, containerName string) (string, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}

	for _, cont := range containers {
		for _, name := range cont.Names {
			// Container names have a leading slash, remove it for matching
			cleanName := strings.TrimPrefix(name, "/")
			// Match if name equals containerName or contains it (for docker-compose naming)
			if cleanName == containerName || strings.Contains(cleanName, containerName) {
				return cont.ID, nil
			}
		}
	}

	return "", &ContainerNotFoundError{Name: containerName}
}

// GetContainerConfig gets the container config for a given container alias
func (c *Client) GetContainerConfig(alias string) *config.ContainerConfig {
	switch alias {
	case "broker":
		return &c.config.Containers.Broker
	case "event":
		return &c.config.Containers.Event
	default:
		return nil
	}
}

// ContainerNotFoundError is returned when a container is not found
type ContainerNotFoundError struct {
	Name string
}

func (e *ContainerNotFoundError) Error() string {
	return "container not found: " + e.Name
}

// ImageInfo represents information about a Docker image
type ImageInfo struct {
	Image   string    `json:"image"`
	ImageID string    `json:"imageId"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
}

// ContainerUpdateResult represents the result of updating a container
type ContainerUpdateResult struct {
	Name           string `json:"name"`
	OldContainerID string `json:"oldContainerId"`
	NewContainerID string `json:"newContainerId"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
}

// ImageUpdateResult represents the result of an image update operation
type ImageUpdateResult struct {
	Success           bool                    `json:"success"`
	Image             string                  `json:"image"`
	ImageID           string                  `json:"imageId"`
	UpdatedContainers []ContainerUpdateResult `json:"updatedContainers"`
	Error             string                  `json:"error,omitempty"`
}

// PullImage pulls an image from the registry
func (c *Client) PullImage(ctx context.Context, imageName string) (string, error) {
	reader, err := c.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return "", err
	}
	defer reader.Close()

	// Read all output to ensure the pull completes
	// Parse the output to get the final digest
	decoder := json.NewDecoder(reader)
	var lastStatus string
	for {
		var event map[string]interface{}
		if err := decoder.Decode(&event); err != nil {
			if err == io.EOF {
				break
			}
			// Ignore JSON decode errors, just continue
			continue
		}
		if status, ok := event["status"].(string); ok {
			lastStatus = status
		}
	}
	_ = lastStatus // We don't need lastStatus but it shows the pull completed

	// Get the image ID after pull
	inspect, _, err := c.cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return "", err
	}

	return inspect.ID, nil
}

// GetImageInfo returns information about an image
func (c *Client) GetImageInfo(ctx context.Context, imageName string) (*ImageInfo, error) {
	inspect, _, err := c.cli.ImageInspectWithRaw(ctx, imageName)
	if err != nil {
		return nil, err
	}

	created, _ := time.Parse(time.RFC3339Nano, inspect.Created)

	return &ImageInfo{
		Image:   imageName,
		ImageID: inspect.ID,
		Created: created,
		Size:    inspect.Size,
	}, nil
}

// RecreateContainer stops, removes, and recreates a container with a new image
// It preserves the original container's configuration
func (c *Client) RecreateContainer(ctx context.Context, containerName string, newImage string) (*ContainerUpdateResult, error) {
	result := &ContainerUpdateResult{
		Name: containerName,
	}

	// Get container ID
	containerID, err := c.getContainerID(ctx, containerName)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, err
	}
	result.OldContainerID = containerID[:12]

	// Inspect the container to get its full configuration
	inspect, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, err
	}

	// Get the actual container name (without leading slash)
	actualName := strings.TrimPrefix(inspect.Name, "/")

	// Stop the container
	timeout := 30
	if err := c.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		result.Status = "failed"
		result.Error = "failed to stop container: " + err.Error()
		return result, err
	}

	// Remove the container
	if err := c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{}); err != nil {
		result.Status = "failed"
		result.Error = "failed to remove container: " + err.Error()
		return result, err
	}

	// Create new container config with the new image
	newConfig := &container.Config{
		Image:        newImage,
		Cmd:          inspect.Config.Cmd,
		Env:          inspect.Config.Env,
		ExposedPorts: inspect.Config.ExposedPorts,
		Labels:       inspect.Config.Labels,
		WorkingDir:   inspect.Config.WorkingDir,
		Entrypoint:   inspect.Config.Entrypoint,
		Healthcheck:  inspect.Config.Healthcheck,
	}

	// Host config
	hostConfig := inspect.HostConfig

	// Network config - preserve network settings
	networkingConfig := &network.NetworkingConfig{
		EndpointsConfig: make(map[string]*network.EndpointSettings),
	}
	for netName, netSettings := range inspect.NetworkSettings.Networks {
		networkingConfig.EndpointsConfig[netName] = &network.EndpointSettings{
			Aliases:   netSettings.Aliases,
			NetworkID: netSettings.NetworkID,
		}
	}

	// Create new container
	createResp, err := c.cli.ContainerCreate(ctx, newConfig, hostConfig, networkingConfig, nil, actualName)
	if err != nil {
		result.Status = "failed"
		result.Error = "failed to create container: " + err.Error()
		return result, err
	}
	result.NewContainerID = createResp.ID[:12]

	// Start the new container
	if err := c.cli.ContainerStart(ctx, createResp.ID, container.StartOptions{}); err != nil {
		result.Status = "failed"
		result.Error = "failed to start container: " + err.Error()
		return result, err
	}

	result.Status = "running"
	return result, nil
}

// WaitForHealthy waits for a container to become healthy
func (c *Client) WaitForHealthy(ctx context.Context, containerName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		status, err := c.GetContainerStatus(ctx, containerName)
		if err != nil {
			return err
		}
		if status == nil {
			return &ContainerNotFoundError{Name: containerName}
		}

		// Check if container is healthy or has no healthcheck
		if status.Health == "healthy" || status.Health == "none" {
			return nil
		}

		// Check if container is running but unhealthy - might need more time
		if status.State != "running" {
			return &ContainerNotHealthyError{Name: containerName, State: status.State}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			// Continue checking
		}
	}

	return &ContainerNotHealthyError{Name: containerName, State: "timeout"}
}

// ContainerNotHealthyError is returned when a container fails to become healthy
type ContainerNotHealthyError struct {
	Name  string
	State string
}

func (e *ContainerNotHealthyError) Error() string {
	return "container " + e.Name + " is not healthy: " + e.State
}

// GetImage returns the configured image
func (c *Client) GetImage() string {
	return c.config.Image
}

var _ = container.Summary{}
