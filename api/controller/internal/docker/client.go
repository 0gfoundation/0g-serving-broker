package docker

import (
	"context"
	"io"
	"strings"

	"github.com/docker/docker/api/types/container"
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

var _ = container.Summary{}
