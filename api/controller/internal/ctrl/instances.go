package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// InstancesEnvVar declares the extra provider instances this controller manages
// beside the primary one, as a JSON array of Instance.
//
// A CVM can carry more than one provider: the chain holds one service per provider
// address, and a broker serves one service type, so a second type on the same machine
// is a second broker with its own wallet, config, event service and ingress. Those run
// the same broker image as the primary one and sign through this controller, so they
// have to be upgraded with it — otherwise an upgrade moves the signing key (derived
// from the primary broker's image) for every instance while only one of them changes
// image.
//
// Read from the controller's OWN environment, never from the config file, for the
// reason the container names above are constants: ApplyCoreConfig rewrites that file,
// so a name read from it would be editable through this controller's API. A compose
// literal is covered by compose_hash instead.
const InstancesEnvVar = "ZG_CONTROLLER_INSTANCES"

// Instance is one extra broker/event pair and the files and proxy that belong to it.
type Instance struct {
	// Name prefixes the aliases the container routes accept for this instance:
	// "<name>-broker", "<name>-event", "<name>-ingress". It also names the instance
	// in its config records, so a reader can tell whose config changed.
	Name string `json:"name"`
	// Broker and Event are exact container names; they must resolve exactly, the same
	// rule the primary pair is held to.
	Broker string `json:"broker"`
	Event  string `json:"event"`
	// Ingress is the proxy in front of Broker, restarted whenever Broker is recreated
	// or restarted so it re-resolves the new address. Optional: absent means nothing
	// in front of it needs re-resolving.
	Ingress string `json:"ingress,omitempty"`
	// ConfigFile is this instance's config as the controller sees it (its own mount of
	// the volume the instance's broker and event read). Required: it is what
	// PUT /v1/config/core?instance=<name> writes.
	ConfigFile string `json:"configFile"`
}

// instanceNamePattern keeps a name usable inside an alias and a record payload: no
// spaces (the record separates name and hash with one), nothing a path would split.
var instanceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// containerNamePattern is docker's own rule for container names.
var containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// parseInstances reads and validates InstancesEnvVar's value. Empty means none.
//
// Refused with recordUpstreamSet on: that record states the whole set of places a
// broker may forward plaintext to, and it is derived from the primary config alone.
// With a second broker the true set is the union, and recording only half of it
// would be a claim a reader believes and that is false. Supporting both means
// deriving the union; until then the combination is refused rather than half-done.
func parseInstances(raw, primaryConfigFile string, recordUpstreamSet bool) ([]Instance, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var list []Instance
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("%s is not a JSON array of instances: %w", InstancesEnvVar, err)
	}
	if len(list) > 0 && recordUpstreamSet {
		return nil, fmt.Errorf("%s declares extra instances, but controller.recordUpstreamSet is on and the recorded set would cover the primary broker only", InstancesEnvVar)
	}

	// Every container name any instance claims, including the fixed ones, so no two
	// declarations can point at the same container and none can take over one the
	// controller already manages under another role.
	taken := map[string]string{
		containerBroker:         "the primary broker",
		containerEvent:          "the primary event service",
		containerIngress:        "the primary ingress",
		containerPrometheusInit: "prometheus-init",
		containerPrometheus:     "prometheus",
	}
	names := map[string]bool{}
	files := map[string]bool{filepath.Clean(primaryConfigFile): true}

	for i, in := range list {
		where := fmt.Sprintf("%s[%d]", InstancesEnvVar, i)
		if !instanceNamePattern.MatchString(in.Name) {
			return nil, fmt.Errorf("%s: name %q must match %s", where, in.Name, instanceNamePattern)
		}
		if names[in.Name] {
			return nil, fmt.Errorf("%s: name %q is declared twice", where, in.Name)
		}
		names[in.Name] = true

		roles := []struct{ role, value string }{{"broker", in.Broker}, {"event", in.Event}}
		if in.Ingress != "" {
			roles = append(roles, struct{ role, value string }{"ingress", in.Ingress})
		}
		for _, r := range roles {
			if !containerNamePattern.MatchString(r.value) {
				return nil, fmt.Errorf("%s: %s container %q is not a container name", where, r.role, r.value)
			}
			if owner, ok := taken[r.value]; ok {
				return nil, fmt.Errorf("%s: %s container %q is already %s", where, r.role, r.value, owner)
			}
			taken[r.value] = fmt.Sprintf("the %s of instance %q", r.role, in.Name)
		}

		if !filepath.IsAbs(in.ConfigFile) || filepath.Clean(in.ConfigFile) != in.ConfigFile {
			return nil, fmt.Errorf("%s: configFile %q must be a clean absolute path", where, in.ConfigFile)
		}
		if files[in.ConfigFile] {
			return nil, fmt.Errorf("%s: configFile %q is already another instance's (or the primary's)", where, in.ConfigFile)
		}
		files[in.ConfigFile] = true
	}
	return list, nil
}

// instanceNamed returns the extra instance with this name, or false.
func (c *Ctrl) instanceNamed(name string) (Instance, bool) {
	for _, in := range c.instances {
		if in.Name == name {
			return in, true
		}
	}
	return Instance{}, false
}

// instanceContainer resolves "<name>-broker|event|ingress" to a container name, or "".
func (c *Ctrl) instanceContainer(alias string) string {
	for _, in := range c.instances {
		switch alias {
		case in.Name + "-broker":
			return in.Broker
		case in.Name + "-event":
			return in.Event
		case in.Name + "-ingress":
			return in.Ingress
		}
	}
	return ""
}

// instanceAliases lists every alias the extra instances add, in declaration order.
func (c *Ctrl) instanceAliases() []string {
	var out []string
	for _, in := range c.instances {
		out = append(out, in.Name+"-broker", in.Name+"-event")
		if in.Ingress != "" {
			out = append(out, in.Name+"-ingress")
		}
	}
	return out
}

// InvalidInstanceError is returned when a request names an instance this controller
// does not manage.
type InvalidInstanceError struct {
	Name string
}

func (e *InvalidInstanceError) Error() string {
	return fmt.Sprintf("no instance named %q; this controller manages the primary broker and the ones %s declares", e.Name, InstancesEnvVar)
}

// GetInstanceConfig reads an extra instance's config file.
func (c *Ctrl) GetInstanceConfig(name string) (string, error) {
	in, ok := c.instanceNamed(name)
	if !ok {
		return "", &InvalidInstanceError{Name: name}
	}
	data, err := os.ReadFile(in.ConfigFile)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ApplyInstanceConfig is ApplyCoreConfig for an extra instance: validated by the
// broker's own loader, recorded in RTMR3 before the write, the record restored if the
// write fails, then that instance's broker, event and ingress restarted — and nothing
// of the primary's touched.
//
// Recorded as EventInstanceConfigUpdate, never as EventConfigUpdate — see that event.
// No upstream-set record: parseInstances refuses extra instances whenever that record
// is on.
func (c *Ctrl) ApplyInstanceConfig(ctx context.Context, name, content string) error {
	in, ok := c.instanceNamed(name)
	if !ok {
		return &InvalidInstanceError{Name: name}
	}
	if err := config.ValidateConfigContent([]byte(content)); err != nil {
		return &InvalidConfigError{Err: err}
	}

	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), configChangeTimeout)
	defer cancel()

	// Before the record: the restarts below START a stopped container, so they are held
	// to what the start route is held to.
	if err := c.checkInstanceStartable(ctx, in); err != nil {
		return err
	}

	sum := sha256.Sum256([]byte(content))
	if err := c.emitter.EmitEvent(ctx, attest.EventInstanceConfigUpdate, []byte(in.Name+" "+hex.EncodeToString(sum[:]))); err != nil {
		return fmt.Errorf("recording the config change of instance %q in RTMR3: %w", in.Name, err)
	}

	if err := os.WriteFile(in.ConfigFile, []byte(content), 0644); err != nil {
		return c.abortInstanceConfigChange(ctx, in, err)
	}

	// Straight to the docker client: the Ctrl methods take `changing`, held here.
	if err := c.dockerClient.RestartContainer(ctx, in.Broker); err != nil {
		return fmt.Errorf("failed to restart %s: %w", in.Broker, err)
	}
	if err := c.dockerClient.RestartContainer(ctx, in.Event); err != nil {
		return fmt.Errorf("failed to restart %s: %w", in.Event, err)
	}
	if in.Ingress != "" {
		return c.restartIngress(ctx, in.Ingress)
	}
	return nil
}

// abortInstanceConfigChange is abortConfigChange for an extra instance: the file is
// re-read and its hash recorded, or "unknown" when it cannot be, which a reader refuses.
func (c *Ctrl) abortInstanceConfigChange(ctx context.Context, in Instance, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()

	payload := in.Name + " unknown"
	if content, err := os.ReadFile(in.ConfigFile); err != nil {
		c.logger.Warnf("[ApplyInstanceConfig] Could not re-read %s to restore the RTMR3 record, recording it as unknown: %v", in.ConfigFile, err)
	} else {
		sum := sha256.Sum256(content)
		payload = in.Name + " " + hex.EncodeToString(sum[:])
	}

	if err := c.emitter.EmitEvent(ctx, attest.EventInstanceConfigUpdate, []byte(payload)); err != nil {
		c.logger.Errorf("[ApplyInstanceConfig] RTMR3 names config content instance %q did not apply and the record could not be restored: %v", in.Name, err)
		return errors.Join(cause, fmt.Errorf("RTMR3 still names the config this change did not apply to instance %q, and restoring it failed: %w", in.Name, err))
	}
	c.logger.Infof("[ApplyInstanceConfig] Change to instance %q aborted; RTMR3 record restored to the config on disk", in.Name)
	return cause
}

// instanceContainers lists every extra broker and event container, brokers first.
func (c *Ctrl) instanceContainers() []string {
	var out []string
	for _, in := range c.instances {
		out = append(out, in.Broker)
	}
	for _, in := range c.instances {
		out = append(out, in.Event)
	}
	return out
}

// startAfterFailedStop brings back the extra instances an upgrade stopped before one
// of them refused to stop. Nothing has been recorded or recreated yet, so the ledger
// still names the image they run and leaving them down would only be an outage.
func (c *Ctrl) startAfterFailedStop(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()
	for _, name := range c.instanceContainers() {
		if err := c.dockerClient.StartContainer(ctx, name); err != nil {
			c.logger.Errorf("[UpdateImages] Could not restart %s after a failed stop: %v", name, err)
		}
	}
}

// upgradeInstances moves every extra instance onto ref: broker, health, event, ingress.
// Called by UpdateImages after the record and after the primary broker is on ref, with
// every extra container already stopped. One instance failing does not stop the next;
// the failures come back joined.
func (c *Ctrl) upgradeInstances(ctx context.Context, ref string, result *docker.ImageUpdateResult) error {
	var errs []error
	for _, in := range c.instances {
		if err := c.upgradeInstance(ctx, in, ref, result); err != nil {
			c.logger.Errorf("[UpdateImages] Instance %q: %v", in.Name, err)
			errs = append(errs, fmt.Errorf("instance %q: %w", in.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Ctrl) upgradeInstance(ctx context.Context, in Instance, ref string, result *docker.ImageUpdateResult) error {
	r, err := c.dockerClient.RecreateContainer(ctx, in.Broker, ref)
	if r != nil {
		result.UpdatedContainers = append(result.UpdatedContainers, *r)
	}
	if err != nil {
		return fmt.Errorf("recreating %s: %w", in.Broker, err)
	}
	if err := c.dockerClient.WaitForHealthy(ctx, in.Broker, 2*time.Minute); err != nil {
		return fmt.Errorf("%s did not become healthy: %w", in.Broker, err)
	}
	r, err = c.dockerClient.RecreateContainer(ctx, in.Event, ref)
	if r != nil {
		result.UpdatedContainers = append(result.UpdatedContainers, *r)
	}
	if err != nil {
		return fmt.Errorf("recreating %s: %w", in.Event, err)
	}
	if in.Ingress != "" {
		if err := c.verifyExactContainer(ctx, in.Ingress); err != nil {
			return fmt.Errorf("%w — the new image runs, but its ingress cannot be restarted to reach it", err)
		}
		if err := c.restartIngress(ctx, in.Ingress); err != nil {
			return fmt.Errorf("%w — the new image runs, but nothing outside the CVM reaches it until this is done", err)
		}
	}
	return nil
}

// checkInstanceStartable holds an instance to what starting it requires: every declared
// container resolves by its exact name, and the broker and event are on the image the
// ledger names.
func (c *Ctrl) checkInstanceStartable(ctx context.Context, in Instance) error {
	names := []string{in.Broker, in.Event}
	if in.Ingress != "" {
		names = append(names, in.Ingress)
	}
	for _, name := range names {
		if err := c.verifyExactContainer(ctx, name); err != nil {
			return fmt.Errorf("instance %q: %w", in.Name, err)
		}
	}
	for _, name := range []string{in.Broker, in.Event} {
		if err := c.checkOnRef(ctx, name); err != nil {
			return fmt.Errorf("instance %q: %w", in.Name, err)
		}
	}
	return nil
}

// checkInstanceExact refuses an alias of an extra instance whose container does not
// resolve by its exact name. Docker lookup falls back to the shortest name CONTAINING
// the one asked for, and for an instance whose container is gone that is somebody
// else's container. The primary's aliases are left as they were.
func (c *Ctrl) checkInstanceExact(ctx context.Context, alias string) error {
	name := c.instanceContainer(alias)
	if name == "" {
		return nil
	}
	return c.verifyExactContainer(ctx, name)
}

// checkInstanceOnRef is checkInstanceExact plus, for an instance's broker or event, the
// rule that starting it must not bring up an image the ledger does not name.
//
// The case it exists for: an upgrade whose recreate of an instance failed BEFORE the
// old container was removed leaves that container stopped on the old image, while the
// record already names the new one. Starting it then — by this route, by a config
// change's restart — would put exactly the broker the record's invariant forbids back
// in service, signing under the new image's key.
func (c *Ctrl) checkInstanceOnRef(ctx context.Context, alias string) error {
	if err := c.checkInstanceExact(ctx, alias); err != nil {
		return err
	}
	for _, in := range c.instances {
		if alias == in.Name+"-broker" || alias == in.Name+"-event" {
			return c.checkOnRef(ctx, c.instanceContainer(alias))
		}
	}
	return nil
}

// checkOnRef requires container name to be pinned to the digest the primary broker runs,
// which is the image every key and record on this controller is derived from.
func (c *Ctrl) checkOnRef(ctx context.Context, name string) error {
	want, err := c.RunningBrokerDigest(ctx)
	if err != nil {
		return fmt.Errorf("refusing to start %s: %w", name, err)
	}
	status, err := c.dockerClient.GetContainerStatus(ctx, name)
	if err != nil {
		return fmt.Errorf("refusing to start %s: reading its image: %w", name, err)
	}
	if status == nil || status.Name != name {
		return &AmbiguousContainerError{Want: name}
	}
	if _, got, pinned := strings.Cut(status.Image, "@"); !pinned || got != want {
		return fmt.Errorf("refusing to start %s: it is on %q, but the broker (and the ledger) is on %s — upgrade to that digest instead", name, status.Image, want)
	}
	return nil
}
