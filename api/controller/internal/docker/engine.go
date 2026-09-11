package docker

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

// EngineLabel marks a container this controller created, and it is the whole reason no
// persistent store is needed to remember them.
//
// The set of engines is derived from docker itself rather than from a file the controller
// keeps: docker already holds the authoritative answer, and a second copy would be a
// thing that can disagree with reality — a container removed out of band, or one whose
// creation was recorded and then failed, would leave the file describing something that
// is not there.
//
// A label rather than a name prefix, because a name is the host the compose network
// resolves and an operator picks it; constraining it to carry a marker would leak this
// implementation into the URLs a config names.
const EngineLabel = "zg.engine"

// GPUEnvVar is how a container declares which of the machine's cards it may see.
//
// Reading it off every container is how the controller answers "which card is free"
// without an NVML dependency: GPU assignment is a docker-level fact and docker is
// already the thing being asked. What it CANNOT answer is how much memory a card has
// left — that is dcgm-exporter's job, and a caller that needs it asks there.
const GPUEnvVar = "NVIDIA_VISIBLE_DEVICES"

// nvidiaRuntime is the container runtime that gives a container GPUs when the compose
// asks for them the old way, with `runtime: nvidia` instead of a device reservation.
//
// Both forms are in use in this project's own deployments — one engine service declares
// `deploy.resources.reservations.devices`, another declares `runtime: nvidia` — so
// HasGPU has to accept either. Recognising only one would report a card as free while a
// 300 GB model sat on it.
const nvidiaRuntime = "nvidia"

// Container is one container as the engine bookkeeping needs to see it.
//
// One inspect per container answers all of it, which is why these fields travel together
// rather than in three functions that would each inspect again: the engine set that goes
// into RTMR3 needs the image and the arguments, and the placement decision needs the
// cards.
type Container struct {
	Name string

	// Image is the reference the container was created with — for an engine this
	// controller created, the pinned "<repo>@sha256:<64hex>" it was given.
	//
	// Config.Image and not the image ID: the ID is content, not a name, and the record
	// has to carry a reference a reader can resolve. For a container created by something
	// else this is whatever that something else passed, which may well be a tag — which
	// is one more reason only labelled engines are ever recorded.
	Image string

	// Args is the container's command, which for an engine is its whole flag list.
	Args []string

	// GPUs is the GPUEnvVar value, verbatim, and meaningful only when HasGPU is true.
	GPUs string

	// HasGPU is whether the container was given GPUs at all.
	//
	// The field exists because "no cards" and "all cards" are both spelled by the ABSENCE
	// of a narrowed GPUEnvVar, and conflating them is a bug in whichever direction it
	// falls. A CVM runs a dozen containers with no GPU access — nginx, prometheus, the
	// broker itself — and reading each of them as holding every card would report the
	// whole machine occupied forever. Reading the engine's "all" as holding none would
	// report it free.
	HasGPU bool

	// Engine is true when this controller created the container.
	Engine bool
}

// HeldGPUs reports the cards this container occupies.
//
// The sentinel "all" is returned for a GPU container whose GPUEnvVar does not narrow to
// specific indices, which is the fail-closed reading: such a container can use any card,
// so no card can be called free. A container with no GPU access holds nothing.
func (c Container) HeldGPUs() []string {
	if !c.HasGPU {
		return nil
	}
	v := strings.TrimSpace(c.GPUs)
	if v == "" || strings.EqualFold(v, GPUAll) {
		return []string{GPUAll}
	}
	out := make([]string, 0, 8)
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, err := strconv.Atoi(part); err != nil {
			// A UUID, "void", "none", or anything else this does not understand. Treated as
			// the whole machine rather than as nothing, for the reason HasGPU gives: the
			// unknown spellings include ones that widen the claim, and only the widening
			// reading is safe to be wrong about.
			return []string{GPUAll}
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return []string{GPUAll}
	}
	sort.Strings(out)
	return out
}

// GPUAll is the sentinel for a claim that names no particular card.
const GPUAll = "all"

// ListContainers reports every container on the daemon, running or not.
//
// Not-running included on purpose: a stopped container still holds its name and its
// configuration, so a card it claims is not free — it reclaims it the moment something
// starts it again, and the restart policy means a daemon restart will.
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	list, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	out := make([]Container, 0, len(list))
	for _, cont := range list {
		// Inspected rather than read off the list, because ContainerList carries neither
		// the environment, the command, nor the host config.
		inspect, err := c.cli.ContainerInspect(ctx, cont.ID)
		if err != nil {
			// A container removed between the list and the inspect is a race with an
			// operator and not a reason to refuse to answer, so that one is skipped.
			//
			// Every OTHER inspect failure is fatal, and that is the correction to an earlier
			// version of this function which skipped them all. A skipped container is one
			// whose claim goes unreported, and under-reporting is the unsafe direction TWICE
			// over: a card that looks free gets a second model placed on it, and an engine
			// left out of the snapshot is a destination the record stops naming. Neither is
			// something the caller can detect, because a short answer looks exactly like a
			// small machine.
			if client.IsErrNotFound(err) {
				continue
			}
			return nil, fmt.Errorf("inspecting %s: %w", cont.ID, err)
		}
		name := ""
		if len(cont.Names) > 0 {
			name = strings.TrimPrefix(cont.Names[0], "/")
		}
		item := Container{Name: name}
		if inspect.Config != nil {
			item.Image = inspect.Config.Image
			item.Args = inspect.Config.Cmd
			for _, kv := range inspect.Config.Env {
				if k, v, ok := strings.Cut(kv, "="); ok && k == GPUEnvVar {
					item.GPUs = v
				}
			}
			_, item.Engine = inspect.Config.Labels[EngineLabel]
		}
		if inspect.HostConfig != nil {
			item.HasGPU = len(inspect.HostConfig.DeviceRequests) > 0 || inspect.HostConfig.Runtime == nvidiaRuntime
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CreateEngineSpec is what CreateEngine needs to build a container.
//
// Everything security-relevant is absent by design: no mounts the caller names, no
// capabilities, no privileged flag, no host namespaces beyond the IPC one the engine
// needs for multi-process tensor parallelism. Those come from config, which lives inside
// app_compose — so a user who reviewed the manifest reviewed them.
//
// "Reviewed the manifest" and not "is guaranteed by compose_hash", which is the stronger
// claim the surrounding documents sometimes make in shorthand. compose_hash is computed
// at launch from the SUBMITTED app_compose; editing the compose inside a running CVM and
// bringing it back up does not change it. So the manifest is what a reader can check, and
// what makes the running controller match it is the boot chain, not this hash.
//
// A caller that could name a mount could reach the host filesystem, and a caller that
// could mount the docker socket could create containers this controller never records —
// which would break the only chain that makes the engine record worth anything, since
// unlike zg-image-update there is no second source to compare it against.
type CreateEngineSpec struct {
	Name    string
	Image   string // must already be pulled; CreateEngine does not pull
	GPUs    string
	Port    int
	Args    []string
	IPCHost bool
	ShmSize string
	Network string // the compose network to join, so the broker can resolve Name
	// Volumes the controller decides on, as "source:target" pairs. Named volumes only —
	// CreateEngine refuses a source that looks like a host path.
	Volumes []string
	// Env the controller decides on. A caller does not supply these: HF_TOKEN lives here
	// and comes from this process's own environment, and the cache-directory variables
	// have to agree with Volumes.
	Env map[string]string
}

// CreateEngine creates a labelled container on the cards the spec names.
//
// It does NOT pull, start-and-wait, or record anything. Pulling is the caller's so the
// long operation is not inside whatever lock the caller holds, and recording is the
// caller's because the record has to be written BEFORE this runs — a container that
// exists and is not in the ledger is the one outcome the whole design refuses.
func (c *Client) CreateEngine(ctx context.Context, spec CreateEngineSpec) error {
	if spec.Name == "" || spec.Image == "" {
		return fmt.Errorf("an engine needs a name and an image")
	}
	// Refused here as well as at the API boundary, because this function is the last thing
	// between a spec and a running container and a second caller would otherwise inherit
	// none of the API's checks.
	for _, v := range spec.Volumes {
		src, _, ok := strings.Cut(v, ":")
		if !ok {
			return fmt.Errorf("volume %q is not source:target", v)
		}
		if strings.HasPrefix(src, "/") || strings.HasPrefix(src, ".") {
			return fmt.Errorf("volume %q names a host path; an engine gets named volumes only", v)
		}
	}

	env := make([]string, 0, len(spec.Env)+1)
	env = append(env, GPUEnvVar+"="+spec.GPUs)
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic, so two identical specs build identical containers
	for _, k := range keys {
		if k == GPUEnvVar {
			continue // the spec's GPUs decide this one
		}
		env = append(env, k+"="+spec.Env[k])
	}

	port := nat.Port(fmt.Sprintf("%d/tcp", spec.Port))
	cfg := &container.Config{
		Image:        spec.Image,
		Cmd:          spec.Args,
		Env:          env,
		Labels:       map[string]string{EngineLabel: "true"},
		ExposedPorts: nat.PortSet{port: struct{}{}},
	}

	host := &container.HostConfig{
		// Unless-stopped rather than always, matching what the compose gives its own
		// engines: a container an operator stopped stays stopped across a daemon restart.
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Runtime:       nvidiaRuntime,
		Resources: container.Resources{DeviceRequests: []container.DeviceRequest{{
			Driver:       "nvidia",
			Capabilities: [][]string{{"gpu"}},
			Count:        -1, // -1 is "all the container can see", which GPUEnvVar narrows
		}}},
		// No ports published to the host. The broker reaches the engine over the compose
		// network by name, and publishing would put an unauthenticated inference server on
		// the machine's interfaces.
		PortBindings: nat.PortMap{},
	}
	if spec.IPCHost {
		host.IpcMode = container.IPCModeHost
	}
	if spec.ShmSize != "" {
		size, err := parseShmSize(spec.ShmSize)
		if err != nil {
			return err
		}
		host.ShmSize = size
	}
	for _, v := range spec.Volumes {
		host.Binds = append(host.Binds, v)
	}

	netCfg := &network.NetworkingConfig{}
	if spec.Network != "" {
		netCfg.EndpointsConfig = map[string]*network.EndpointSettings{
			spec.Network: {Aliases: []string{spec.Name}},
		}
	}

	if _, err := c.cli.ContainerCreate(ctx, cfg, host, netCfg, nil, spec.Name); err != nil {
		return fmt.Errorf("creating engine %q: %w", spec.Name, err)
	}
	return nil
}

// RemoveEngine stops and removes a container this controller created.
//
// It refuses a container without the label, which is what keeps this from becoming a
// general "delete any container" endpoint: the compose's own services are not the
// controller's to remove, and a reboot would bring them back anyway.
func (c *Client) RemoveEngine(ctx context.Context, name string) error {
	id, err := c.getContainerID(ctx, name)
	if err != nil {
		return err
	}
	inspect, err := c.cli.ContainerInspect(ctx, id)
	if err != nil {
		return fmt.Errorf("inspecting %q: %w", name, err)
	}
	if _, ok := inspect.Config.Labels[EngineLabel]; !ok {
		return fmt.Errorf("%q is not an engine this controller created, so it is not this controller's to remove", name)
	}
	if err := c.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("removing engine %q: %w", name, err)
	}
	return nil
}

// parseShmSize reads the compose spelling ("128gb", "32g", "1024m") into bytes.
func parseShmSize(s string) (int64, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(v, "gb"):
		mult, v = 1<<30, strings.TrimSuffix(v, "gb")
	case strings.HasSuffix(v, "g"):
		mult, v = 1<<30, strings.TrimSuffix(v, "g")
	case strings.HasSuffix(v, "mb"):
		mult, v = 1<<20, strings.TrimSuffix(v, "mb")
	case strings.HasSuffix(v, "m"):
		mult, v = 1<<20, strings.TrimSuffix(v, "m")
	}
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("shm size %q is not a size", s)
	}
	return n * mult, nil
}
