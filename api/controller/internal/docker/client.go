package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	dockerimage "github.com/0glabs/0g-serving-broker/common/docker"
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
	cli *client.Client
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

	return &Client{cli: cli}, nil
}

// Close closes the Docker client
func (c *Client) Close() error {
	return c.cli.Close()
}

// GetContainerStatus gets the status of a container by name
// The containerName can be a partial match (e.g., "broker" matches "project-0g-serving-provider-broker-1")
// Matching priority: exact match > substring match (prefer shortest container name to avoid ambiguity)
func (c *Client) GetContainerStatus(ctx context.Context, containerName string) (*ContainerStatus, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	var bestMatch struct {
		cont *container.Summary
		name string
	}

	for i := range containers {
		cont := &containers[i]
		for _, name := range cont.Names {
			cleanName := strings.TrimPrefix(name, "/")

			// Exact match has highest priority
			if cleanName == containerName {
				return c.inspectContainerStatus(ctx, cont.ID, cleanName)
			}

			// Substring match - prefer shortest container name to avoid matching more specific variants
			if strings.Contains(cleanName, containerName) {
				if bestMatch.cont == nil || len(cleanName) < len(bestMatch.name) {
					bestMatch.cont = cont
					bestMatch.name = cleanName
				}
			}
		}
	}

	if bestMatch.cont != nil {
		return c.inspectContainerStatus(ctx, bestMatch.cont.ID, bestMatch.name)
	}

	return nil, nil
}

// inspectContainerStatus gets detailed status for a container
func (c *Client) inspectContainerStatus(ctx context.Context, containerID, containerName string) (*ContainerStatus, error) {
	inspect, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}

	health := "none"
	if inspect.State.Health != nil {
		health = inspect.State.Health.Status
	}

	return &ContainerStatus{
		Name:      containerName,
		ID:        containerID[:12],
		State:     inspect.State.Status,
		Health:    health,
		StartedAt: inspect.State.StartedAt,
		Image:     inspect.Config.Image,
	}, nil
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
	containerID, err := c.unguardedContainerID(ctx, containerName)
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

// unguardedContainerID gets container ID by name.
// The containerName can be a partial match (e.g., "broker" matches "project-0g-serving-provider-broker-1")
// Matching priority: exact match > substring match (prefer shortest container name to avoid ambiguity)
//
// Named for what it lacks: it will happily return the controller's own
// container. Only read paths may call it; writes go through getContainerID.
// GetContainerStatus carries a third copy of this walk and is also read-only.
func (c *Client) unguardedContainerID(ctx context.Context, containerName string) (string, error) {
	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", err
	}

	var bestMatch struct {
		id   string
		name string
	}

	for _, cont := range containers {
		for _, name := range cont.Names {
			cleanName := strings.TrimPrefix(name, "/")

			// Exact match has highest priority
			if cleanName == containerName {
				return cont.ID, nil
			}

			// Substring match - prefer shortest container name to avoid matching more specific variants
			// e.g., searching "prometheus" should match "project-prometheus-1" not "project-prometheus-init-1"
			if strings.Contains(cleanName, containerName) {
				if bestMatch.id == "" || len(cleanName) < len(bestMatch.name) {
					bestMatch.id = cont.ID
					bestMatch.name = cleanName
				}
			}
		}
	}

	if bestMatch.id != "" {
		return bestMatch.id, nil
	}

	return "", &ContainerNotFoundError{Name: containerName}
}

// getContainerID resolves containerName to a container the controller may write
// to, refusing to return the controller's own container.
//
// Resolution falls back to substring matching, so a container whose name merely
// contains a managed name can be selected — including the controller's own, in a
// deployment that names it after the broker. Stopping or removing ourselves
// mid-upgrade aborts the upgrade with the containers already torn down, so the
// docker layer refuses rather than relying on every caller to check.
//
// This holds the obvious name so that a write path added later gets the guard by
// default; read paths ask for unguardedContainerID explicitly.
func (c *Client) getContainerID(ctx context.Context, containerName string) (string, error) {
	containerID, err := c.unguardedContainerID(ctx, containerName)
	if err != nil {
		return "", err
	}

	selfID, err := c.selfContainerID(ctx)
	if err != nil {
		return "", err
	}
	if containerID == selfID {
		return "", &SelfOperationError{Name: containerName}
	}

	return containerID, nil
}

// selfContainerID returns the ID of the container the controller runs in.
//
// Docker sets a container's hostname to its own short ID, so the container whose
// ID carries that prefix is us.
//
// The prefix must be at least shortIDLen, since a shorter one is a prefix of many
// IDs. An ambiguous match is refused rather than resolved by list order.
//
// Failing to identify ourselves is an error rather than a shrug: every caller is
// about to write, and "we could not tell whether that container is us" is not a
// safe basis for stopping it. What the controller's deployment must look like for
// this to resolve is in doc/controller-design.md §3.1.
//
// ponytail: not cached — this and unguardedContainerID list separately, so a
// write costs two lists against a local socket.
func (c *Client) selfContainerID(ctx context.Context) (string, error) {
	hostname, err := hostnameFn()
	if err != nil {
		return "", fmt.Errorf("reading hostname to identify the controller's own container: %w", err)
	}
	if len(hostname) < shortIDLen {
		return "", &SelfUnidentifiedError{Hostname: hostname}
	}

	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return "", fmt.Errorf("listing containers to identify the controller's own: %w", err)
	}

	var selfID string
	for _, cont := range containers {
		if !strings.HasPrefix(cont.ID, hostname) {
			continue
		}
		if selfID != "" && selfID != cont.ID {
			return "", &SelfUnidentifiedError{Hostname: hostname, Ambiguous: true}
		}
		selfID = cont.ID
	}
	if selfID == "" {
		return "", &SelfUnidentifiedError{Hostname: hostname}
	}

	return selfID, nil
}

// shortIDLen is the length of the container ID prefix docker uses as a
// container's default hostname.
const shortIDLen = 12

// hostnameFn is os.Hostname, indirected because a test can build fixtures around
// the machine's hostname but cannot choose it, and the short-hostname branch is
// only reachable by choosing one.
var hostnameFn = os.Hostname

// SetHostnameForTests points self-identification at a chosen hostname and returns
// a function restoring the previous source.
//
// Exported only because the ctrl package's tests drive write paths through this
// client, and every write asks which container is us by matching os.Hostname()
// against container IDs — the one input a test cannot arrange for itself. Nothing
// in production calls this; os.Hostname is the default.
func SetHostnameForTests(name string) (restore func()) {
	prev := hostnameFn
	hostnameFn = func() (string, error) { return name, nil }
	return func() { hostnameFn = prev }
}

// SelfOperationError is returned when an operation would modify the controller's
// own container.
type SelfOperationError struct {
	Name string
}

func (e *SelfOperationError) Error() string {
	return "refusing to operate on the controller's own container: " + e.Name +
		" resolved to it"
}

// SelfUnidentifiedError is returned when the controller cannot work out which
// container it is running in, and therefore cannot rule out modifying itself.
type SelfUnidentifiedError struct {
	Hostname  string
	Ambiguous bool // several container IDs carried the hostname as a prefix
}

// Each branch carries its own remedy: the deployment advice that fits the other
// two is useless against an ID collision, where the hostname is already docker's.
func (e *SelfUnidentifiedError) Error() string {
	const prefix = "cannot identify the controller's own container: hostname "
	const asContainer = "; the controller must run as a container with docker's default hostname"
	switch {
	case e.Ambiguous:
		return prefix + e.Hostname + " is a prefix of more than one container ID" +
			"; recreate one of the colliding containers"
	case len(e.Hostname) < shortIDLen:
		return prefix + strconv.Quote(e.Hostname) + " is too short to be a container ID" + asContainer
	default:
		return prefix + e.Hostname + " matches no container ID" + asContainer
	}
}

// GetContainerEnv gets environment variables from a container, filtered by allowed keys
func (c *Client) GetContainerEnv(ctx context.Context, containerName string, allowedKeys []string) (map[string]string, error) {
	containerID, err := c.unguardedContainerID(ctx, containerName)
	if err != nil {
		return nil, err
	}

	inspect, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}

	// Build allowed keys set for fast lookup
	allowedSet := make(map[string]bool)
	for _, key := range allowedKeys {
		allowedSet[key] = true
	}

	// Extract env vars, filtering by allowed keys
	result := make(map[string]string)
	for _, env := range inspect.Config.Env {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 && allowedSet[parts[0]] {
			result[parts[0]] = parts[1]
		}
	}

	return result, nil
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
	Digest  string    `json:"digest"` // Image digest (e.g., sha256:abc123...)
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
	Digest            string                  `json:"digest"` // Image digest (e.g., sha256:abc123...)
	UpdatedContainers []ContainerUpdateResult `json:"updatedContainers"`
	Error             string                  `json:"error,omitempty"`
}

// PullImage pulls an image from the registry and returns the image info.
//
// For a "repo@sha256:D" reference the reported digest is D itself. Docker
// verifies a digest reference against its content on pull, so the registry can
// only serve exactly D or fail. Reverse-looking it up would be worse: the
// daemon orders RepoDigests by repo, so its first entry — the one GetImageInfo
// reads — belongs to whichever repo sorts first among all of them, which for a
// mirrored image is not the one that was asked for. That still applies to the
// unpinned branch below, which has no reference digest to prefer.
//
// D may be an index digest; for a multi-arch reference the bytes on disk are a
// platform-specific child manifest. D is what docker reports for the reference,
// which is the identity being attested.
func (c *Client) PullImage(ctx context.Context, imageRef string) (*ImageInfo, error) {
	reader, err := c.cli.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return nil, fmt.Errorf("pulling %s: %w", imageRef, err)
	}
	defer reader.Close()

	if err := drainPullProgress(reader); err != nil {
		return nil, fmt.Errorf("pulling %s: %w", imageRef, err)
	}

	info, err := c.GetImageInfo(ctx, imageRef)
	if err != nil {
		return nil, fmt.Errorf("inspecting %s after pull: %w", imageRef, err)
	}
	if _, digest, pinned := strings.Cut(imageRef, "@"); pinned {
		info.Digest = digest
	}

	return info, nil
}

// drainPullProgress consumes docker's pull progress stream and decides whether
// it describes a successful pull.
//
// ImagePull returns a reader, not a result: the daemon reports a failed pull as
// an error frame inside the stream while returning nil itself. Discarding the
// frames therefore reads a failed pull as a successful one. Decode errors are
// fatal for the same reason — a truncated stream is not evidence the pull
// finished — and a body with no frames at all is rejected, since "raised no
// error" and "said nothing" are otherwise indistinguishable.
//
// This is not a completion check: no terminal frame is required, so a clean
// result means "something was said and none of it was a failure".
func drainPullProgress(r io.Reader) error {
	decoder := json.NewDecoder(r)
	frames := 0
	for {
		// Both spellings: docker sends both, but marks "error" deprecated since
		// API v1.4 and slated for removal, with "errorDetail" as the live field.
		// ErrorDetail is a pointer because it is omitempty — presence is the
		// failure signal, and its message is omitempty too, so keying off a
		// non-empty message would walk past a frame carrying only a code.
		var frame struct {
			ErrorDetail *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"errorDetail"`
			Error string `json:"error"`
		}
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				if frames == 0 {
					return errors.New("pull stream carried no frames")
				}
				return nil
			}
			return fmt.Errorf("decoding pull progress: %w", err)
		}
		frames++
		if frame.ErrorDetail != nil {
			if msg := frame.ErrorDetail.Message; msg != "" {
				return errors.New(msg)
			}
			return fmt.Errorf("daemon reported a failure with no detail (code %d)", frame.ErrorDetail.Code)
		}
		if frame.Error != "" {
			return errors.New(frame.Error)
		}
	}
}

// GetImageInfo returns information about an image
func (c *Client) GetImageInfo(ctx context.Context, imageName string) (*ImageInfo, error) {
	info, err := dockerimage.GetImageInfo(ctx, c.cli, imageName)
	if err != nil {
		return nil, err
	}

	return &ImageInfo{
		Image:   info.Image,
		ImageID: info.ImageID,
		Digest:  info.Digest,
		Created: info.Created,
		Size:    info.Size,
	}, nil
}

// imageEnvUpdates splits a pinned reference into the two variables the broker
// reads to know which image it is running.
//
// A recreated container inherits the old one's environment, so without this the
// broker would come up on the new image still announcing the previous digest.
// The contract would see no image change and keep the TEE signer
// acknowledgement that an image change is supposed to drop — the upgrade would
// be invisible in exactly the place it is meant to be visible.
//
// An unpinned reference yields nothing rather than a guess: writing a digest the
// reference does not carry would be inventing the very fact these variables
// exist to state.
func imageEnvUpdates(imageRef string) map[string]string {
	repo, digest, pinned := strings.Cut(imageRef, "@")
	if !pinned {
		return nil
	}
	return map[string]string{
		"IMAGE_REPO":   repo,
		"IMAGE_DIGEST": digest,
	}
}

// RecreateContainer stops, removes, and recreates a container with a new image
// It preserves the original container's configuration, plus the IMAGE_REPO /
// IMAGE_DIGEST pair naming the image it is now on.
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
		Env:          mergeEnv(inspect.Config.Env, imageEnvUpdates(newImage)),
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

// ReloadNginx sends a reload signal to nginx in the specified container
// This is useful when upstream containers are recreated and nginx needs to re-resolve DNS
func (c *Client) ReloadNginx(ctx context.Context, containerName string) error {
	containerID, err := c.getContainerID(ctx, containerName)
	if err != nil {
		return err
	}

	// Execute nginx -s reload to gracefully reload configuration and re-resolve DNS
	execConfig := container.ExecOptions{
		Cmd:          []string{"nginx", "-s", "reload"},
		AttachStderr: true,
		AttachStdout: true,
	}

	execResp, err := c.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return err
	}

	return c.cli.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{})
}

var _ = container.Summary{}

// RerunContainerWithEnv recreates an init container with updated environment variables,
// waits for it to exit, and checks its exit code.
//
// Init containers only. The service-container variant went with the ingress config write
// path: nothing edits a long-running container's environment any more, so the branch that
// started one and returned without waiting had no callers left.
func (c *Client) RerunContainerWithEnv(ctx context.Context, containerName string, envUpdates map[string]string) error {
	// 1. Get container ID
	containerID, err := c.getContainerID(ctx, containerName)
	if err != nil {
		return err
	}

	// 2. Inspect the container to get its full configuration
	inspect, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return err
	}

	// Get the actual container name (without leading slash)
	actualName := strings.TrimPrefix(inspect.Name, "/")

	// 3. Stop the container. A stop error is ignored: an init container has usually
	// exited already.
	timeout := 10
	_ = c.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})

	// 4. Remove the old container
	if err := c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{}); err != nil {
		return err
	}

	// 5. Merge environment variables (new values override old ones)
	newEnv := mergeEnv(inspect.Config.Env, envUpdates)

	// 6. Create new container config with updated environment
	newConfig := &container.Config{
		Image:        inspect.Config.Image,
		Cmd:          inspect.Config.Cmd,
		Env:          newEnv,
		Entrypoint:   inspect.Config.Entrypoint,
		WorkingDir:   inspect.Config.WorkingDir,
		Labels:       inspect.Config.Labels,
		ExposedPorts: inspect.Config.ExposedPorts,
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

	// 7. Create new container
	createResp, err := c.cli.ContainerCreate(ctx, newConfig, hostConfig, networkingConfig, nil, actualName)
	if err != nil {
		return err
	}

	// 8. Start the new container
	if err := c.cli.ContainerStart(ctx, createResp.ID, container.StartOptions{}); err != nil {
		return err
	}

	// 9. Wait for exit and check the exit code
	statusCh, errCh := c.cli.ContainerWait(ctx, createResp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return &InitContainerFailedError{Name: containerName, ExitCode: int(status.StatusCode)}
		}
	}

	return nil
}

// mergeEnv merges environment variables, new values override old ones
func mergeEnv(oldEnv []string, updates map[string]string) []string {
	envMap := make(map[string]string)

	// Parse old env into map
	for _, e := range oldEnv {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Apply updates
	for k, v := range updates {
		envMap[k] = v
	}

	// Convert back to slice
	result := make([]string, 0, len(envMap))
	for k, v := range envMap {
		result = append(result, k+"="+v)
	}

	return result
}

// InitContainerFailedError is returned when an init container exits with non-zero code
type InitContainerFailedError struct {
	Name     string
	ExitCode int
}

func (e *InitContainerFailedError) Error() string {
	return fmt.Sprintf("init container %s failed with exit code %d", e.Name, e.ExitCode)
}
