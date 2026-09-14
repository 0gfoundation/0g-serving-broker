package ctrl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// engineChangeTimeout bounds one engine create or remove, for the reason
// recordUpstreamSetTimeout bounds an emit: these hold `changing`, which also gates
// start/stop/restart, and an image pull has no deadline of its own.
const engineChangeTimeout = 30 * time.Minute

// engineRevisionLen is the length of a model revision, a git commit sha.
const engineRevisionLen = 40

// EngineSpec is what a caller asks for. Everything absent from it is the controller's to
// decide — see docker.CreateEngineSpec for why.
type EngineSpec struct {
	// Name is the container name, and therefore the host the broker will resolve. It has
	// to be a name attest's record can carry, because a name that record refuses would
	// make the whole engine set unreadable.
	Name string `json:"name"`
	// Image must carry a digest and its repository must be one this controller is
	// configured for — see config.EngineImage.
	Image string `json:"image"`
	// GPUs is the NVIDIA_VISIBLE_DEVICES value. Every card it names must be free.
	GPUs string `json:"gpus"`
	Port int    `json:"port"`
	// Model is separate from Args so the revision can be REQUIRED. Buried in an argument
	// array it would have to be found by parsing, and parsing is where a bypass lives —
	// "--revision=x" and "--revision x" are the same flag and two different strings.
	Model EngineModel `json:"model"`
	// Args is everything else, passed through. The allowlist decides what a caller may
	// not say; it does not decide what a caller must say, because every engine's tuning
	// flags differ and a template could not hold them.
	Args []string `json:"args"`
}

// EngineModel is the weights and the code that comes with them.
type EngineModel struct {
	Repo string `json:"repo"`
	// Revision is REQUIRED, and that is the single most load-bearing rule here.
	//
	// sglang and vLLM both take --trust-remote-code, which executes modeling code out of
	// the model repository. A repo id names what was asked for; only a revision names
	// what arrives. Without it the RTMR3 record would say "this container runs code from
	// repo X" while the code itself could change under that name at any time — so the
	// record would describe nothing.
	Revision string `json:"revision"`
}

// EngineStatus is one engine as the controller sees it.
type EngineStatus struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	GPUs  string `json:"gpus"`
	State string `json:"state"`
}

// GPUClaim is one card and who holds it.
type GPUClaim struct {
	GPU      string `json:"gpu"`
	HeldBy   string `json:"heldBy"`
	ByEngine bool   `json:"byEngine"`
	// Monitoring is true when controller.engineGPUIgnore names this container: it can see
	// the card but is declared not to allocate on it. Reported rather than filtered out,
	// so the answer stays what docker actually says and only the placement DECISION uses
	// the operator's declaration.
	Monitoring bool `json:"monitoring,omitempty"`
}

// GPUAllocation reports which cards are claimed and by whom.
//
// Derived from docker rather than from NVIDIA: assignment is a docker-level fact, and
// asking docker avoids an NVML dependency, a CAP_SYS_ADMIN, and a second source that can
// disagree with the one that actually decides. What docker CANNOT say is how much memory
// a card has left — that is dcgm-exporter's answer, and a caller that needs it asks
// there. This says who holds a card, which is what deciding placement needs.
//
// The key docker.GPUAll collects the containers that hold no PARTICULAR card but can use
// any — every engine in this project's own deployments, which run with
// NVIDIA_VISIBLE_DEVICES=all. A caller looking for a free card has to treat that entry
// as covering the whole machine, which is what gpusAreFree does.
//
// Reports what docker says, including the containers controller.engineGPUIgnore declares
// non-occupying: those are flagged rather than dropped. The two questions are different —
// "which cards can this container see" is docker's answer and this is it, while "is that
// card available" also needs the operator's claim about which visibility is occupancy.
func (c *Ctrl) GPUAllocation(ctx context.Context) (map[string][]GPUClaim, error) {
	containers, err := c.dockerClient.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	monitoring := map[string]bool{}
	for _, name := range c.config.EngineGPUIgnore {
		monitoring[name] = true
	}
	out := map[string][]GPUClaim{}
	for _, cont := range containers {
		for _, gpu := range cont.HeldGPUs() {
			out[gpu] = append(out[gpu], GPUClaim{
				GPU: gpu, HeldBy: cont.Name, ByEngine: cont.Engine,
				Monitoring: monitoring[cont.Name],
			})
		}
	}
	return out, nil
}

// CreateEngine records an engine and then creates it, in that order.
//
// # The order is the whole design
//
// RTMR3 is append-only, so a record written before the act cannot be missing for an act
// that happened; a record written after could be. The one outcome that must not exist is
// a container serving plaintext that the ledger does not name — so the record goes first
// and a failed create leaves a record of a container that does not exist, which is the
// harmless direction and which the next successful record corrects.
//
// # What a caller may not decide
//
// Mounts, capabilities, host namespaces, published ports and the environment are the
// controller's. A caller that could name a mount could reach the host filesystem; one
// that could mount the docker socket could create containers this controller never
// records, which would break the only chain that makes the record worth anything.
func (c *Ctrl) CreateEngine(ctx context.Context, spec EngineSpec) error {
	// Gated on the same switch the upstream record is, and deliberately not on a second
	// one of its own.
	//
	// Both records land in the same append-only log and attest.ResolveRunningState
	// hard-fails on a zg- event it does not recognise, so emitting either one to a reader
	// that predates it makes the CVM unverifiable for EVERY question, not just this one.
	// One switch means one decision — "every consumer of this deployment's quote can read
	// the zg- events this build writes" — taken once. Two switches would let an operator
	// turn on the one whose reader had shipped and be made unverifiable by the other.
	//
	// It also means the container cannot exist unrecorded: with nowhere to write the set,
	// creating one would put a destination inside the CVM that a verifier could not tell
	// from an external vendor.
	//
	// Note what is NOT required here: the attestation proxy. UpdateImages refuses without
	// it because the record it writes names a key derived through it, and the engine
	// record names no key — the engine set deliberately does not enter UpstreamSetHash, so
	// two deployments permitting the same destinations agree on that hash whatever
	// containers they happen to have created. The engine record is readable, and worth
	// exactly what it is worth, on a deployment with no proxy at all.
	if !c.config.RecordUpstreamSet {
		return refusef("cannot create an engine: controller.recordUpstreamSet is off, so there is nowhere to record the set and the container would serve unrecorded")
	}

	img, err := c.engineImageFor(spec.Image)
	if err != nil {
		return err
	}
	if err := c.validateEngineSpec(spec, img); err != nil {
		return err
	}

	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineChangeTimeout)
	defer cancel()

	free, err := c.gpusAreFree(ctx, spec.GPUs)
	if err != nil {
		return err
	}
	if !free {
		return refusef("cannot create %q: it asks for GPU %q and that is already occupied. A container running with %s=all occupies every card; narrow it in the compose, or name it in controller.engineGPUIgnore if it only observes the cards", spec.Name, spec.GPUs, docker.GPUEnvVar)
	}

	// Pulled before the record, because a pull is the long step and a record naming an
	// image that cannot be fetched is a record of something that will never exist.
	if _, err := c.dockerClient.PullImage(ctx, spec.Image); err != nil {
		return fmt.Errorf("pulling %q: %w", spec.Image, err)
	}

	args := img.buildArgs(spec.Model.Repo, spec.Model.Revision, spec.Port, spec.Args)

	// The record is the WHOLE set, so it is built from what docker holds plus this one.
	// Derived rather than remembered: docker is the authority on which containers exist,
	// and a list this process kept could disagree with it.
	if err := c.recordEngineSet(ctx, append(c.engineSetFromDocker(ctx), attest.Engine{
		Name: spec.Name, Image: spec.Image, GPUs: spec.GPUs, Args: strings.Join(args, " "),
	})); err != nil {
		return err
	}

	if err := c.dockerClient.CreateEngine(ctx, docker.CreateEngineSpec{
		Name: spec.Name, Image: spec.Image, GPUs: spec.GPUs, Port: spec.Port,
		Args: args, IPCHost: img.IPCHost, ShmSize: img.ShmSize,
		Network: c.config.EngineNetwork, Volumes: c.config.EngineVolumes,
		Env: c.engineEnv(),
	}); err != nil {
		// The record now names a container that does not exist. Corrected rather than left
		// standing, and the correction is the same snapshot without it — the harmless
		// direction, since over-reporting where plaintext MAY go is never the unsafe one.
		if rerr := c.recordEngineSet(ctx, c.engineSetFromDocker(ctx)); rerr != nil {
			c.logger.Errorf("[CreateEngine] %q was recorded and not created, and the record could not be corrected: %v", spec.Name, rerr)
		}
		return err
	}
	if err := c.dockerClient.StartContainer(ctx, spec.Name); err != nil {
		return fmt.Errorf("starting engine %q: %w", spec.Name, err)
	}
	c.logger.Infof("[CreateEngine] Created %q on GPU %q from %s", spec.Name, spec.GPUs, spec.Image)
	return nil
}

// RemoveEngine removes an engine and then records the set without it.
//
// The reverse of CreateEngine's order, and for the same invariant: the ledger must never
// understate where plaintext may go. Removing the container first means the intervening
// record over-states the set — a destination that no longer exists is named — which is
// the safe direction. Recording first would understate it: a container still serving
// would be absent from the ledger.
// Returns the upstream URLs a config on disk still points at the removed container, so
// the caller learns that a model is now routed at nothing. Advisory: see
// engineStillRouted.
func (c *Ctrl) RemoveEngine(ctx context.Context, name string) ([]string, error) {
	// Same gate as CreateEngine, and reachable only if that gate was once open: with
	// recording off nothing here created an engine, and removing a container this
	// controller did not create is refused one layer down anyway. An operator who turned
	// the switch off with engines running is told that rather than being allowed a removal
	// the ledger would never reflect.
	if !c.config.RecordUpstreamSet {
		return nil, refusef("cannot remove an engine: controller.recordUpstreamSet is off, so the correction could not be recorded and the ledger would go on naming it")
	}
	if !c.changing.TryLock() {
		return nil, ErrChangeInProgress
	}
	defer c.changing.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), engineChangeTimeout)
	defer cancel()

	// Resolved to an EXACT name before anything is removed, because the docker layer
	// resolves a name by substring when no container matches it exactly — a fallback the
	// upgrade path needs, since compose prefixes its containers with the project name.
	//
	// On a destructive endpoint that fallback is the wrong behaviour and not merely a
	// loose one: "no engine called that" would silently become "removed a different
	// engine whose name happens to contain it". DELETE /v1/engines/isper removing
	// `whisper` is a caller getting an outcome they did not ask for, and the outcome is
	// a model going offline.
	//
	// This also moves the label check ahead of the removal: a container this controller
	// did not create is refused before it is touched rather than after it is resolved.
	if err := c.engineExists(ctx, name); err != nil {
		return nil, err
	}

	// Collected before the removal, because afterwards the answer is the same and the
	// reason to want it is gone: the caller is told what they have just disconnected.
	routed := c.engineStillRouted(name)

	if err := c.dockerClient.RemoveEngine(ctx, name); err != nil {
		return nil, err
	}
	if err := c.recordEngineSet(ctx, c.engineSetFromDocker(ctx)); err != nil {
		return routed, fmt.Errorf("%q was removed and the record could not be corrected, so the ledger still names it: %w", name, err)
	}
	if len(routed) > 0 {
		c.logger.Warnf("[RemoveEngine] Removed %q while the config still routes %v at it; those models now reach nothing until the config is changed", name, routed)
	}
	c.logger.Infof("[RemoveEngine] Removed %q", name)
	return routed, nil
}

// engineStillRouted reports the upstream URLs a config on disk still points at a
// container, so a removal can say which models it just disconnected.
//
// Read off the FILE rather than off c.fullConfig, which is a once.Do singleton that
// nothing reloads — after an ApplyCoreConfig it describes the previous config while the
// broker is running the new one. A warning built on the stale copy would be wrong in
// both directions, and a wrong warning on a destructive endpoint is worse than none.
//
// Advisory throughout: every failure returns no answer rather than failing the removal.
// A config this cannot read is a reason not to claim anything about routing, never a
// reason to keep a container the caller asked to remove — and the removal is still
// recorded either way, which is the part that has to be right.
//
// It does NOT refuse the removal. Replacing an engine in place — remove, then create
// under the same name — is a flow where the config pointing at that name is exactly
// correct, and refusing would block the main reason this endpoint exists.
func (c *Ctrl) engineStillRouted(name string) []string {
	if c.config.ConfigFile == "" {
		return nil
	}
	data, err := os.ReadFile(c.config.ConfigFile)
	if err != nil {
		c.logger.Warnf("[RemoveEngine] Could not read %s, so this removal cannot say which models routed at %q: %v", c.config.ConfigFile, name, err)
		return nil
	}
	svc, err := config.ServiceFromYAML(data)
	if err != nil {
		c.logger.Warnf("[RemoveEngine] Could not read the service config, so this removal cannot say which models routed at %q: %v", name, err)
		return nil
	}
	members, err := upstreamsFromConfig(svc)
	if err != nil {
		c.logger.Warnf("[RemoveEngine] Could not collect the config's upstreams, so this removal cannot say which models routed at %q: %v", name, err)
		return nil
	}
	var out []string
	for _, u := range members {
		parsed, err := url.Parse(u.URL)
		if err != nil {
			continue
		}
		if parsed.Hostname() == name {
			out = append(out, u.URL)
		}
	}
	return out
}

// ListEngines reports the engines this controller created.
func (c *Ctrl) ListEngines(ctx context.Context) ([]EngineStatus, error) {
	containers, err := c.dockerClient.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	var out []EngineStatus
	for _, cont := range containers {
		if !cont.Engine {
			continue
		}
		st := EngineStatus{Name: cont.Name, Image: cont.Image, GPUs: cont.GPUs}
		// The state is a second call because the listing does not carry it. Reported as
		// empty rather than failing the whole list: a container that vanished between the
		// two calls is a race with an operator, and the rest of the answer is still true.
		if s, err := c.dockerClient.GetContainerStatus(ctx, cont.Name); err == nil && s != nil {
			st.State = s.State
		}
		out = append(out, st)
	}
	return out, nil
}

// engineSetFromDocker is the current engine set as docker holds it, which is the snapshot
// that goes into the record.
//
// Derived from docker on every write rather than kept in a field, for the reason
// docker.EngineLabel gives: docker is the authority on which containers exist, and a copy
// this process kept would be a thing that can disagree with reality — after a container
// was removed out of band, or after a create this process recorded and failed.
//
// An error produces a SHORTER set rather than a failure, which is the wrong direction and
// is why it is logged as an error: a set missing an engine understates where plaintext
// may go. It is still preferred to failing, because the caller is mid-change and the
// alternative is a ledger left describing the state before it.
func (c *Ctrl) engineSetFromDocker(ctx context.Context) []attest.Engine {
	containers, err := c.dockerClient.ListContainers(ctx)
	if err != nil {
		c.logger.Errorf("[engineSet] Could not list containers, so the recorded set may understate what runs: %v", err)
		return nil
	}
	var out []attest.Engine
	for _, cont := range containers {
		if !cont.Engine {
			continue
		}
		out = append(out, attest.Engine{
			Name:  cont.Name,
			Image: cont.Image,
			GPUs:  cont.GPUs,
			// Space-joined, which is lossy for an argument containing a space — and no engine
			// takes one. The alternative is a nested quoting grammar inside a tab-separated
			// field, and a reader would then have to parse the writer's quoting to see the
			// flags; the loss is bounded and visible, the grammar's complexity would not be.
			Args: strings.Join(cont.Args, " "),
		})
	}
	return out
}

// engineExists refuses a name that is not, exactly, an engine this controller created.
func (c *Ctrl) engineExists(ctx context.Context, name string) error {
	containers, err := c.dockerClient.ListContainers(ctx)
	if err != nil {
		return err
	}
	var engines []string
	for _, cont := range containers {
		if !cont.Engine {
			continue
		}
		if cont.Name == name {
			return nil
		}
		engines = append(engines, cont.Name)
	}
	return refusef("no engine named %q; this controller created %v", name, engines)
}

// recordEngineSet renders the set and writes it, refusing rather than writing something a
// reader cannot read.
func (c *Ctrl) recordEngineSet(ctx context.Context, engines []attest.Engine) error {
	payload, err := attest.RenderEngineSet(engines)
	if err != nil {
		return fmt.Errorf("the engine set cannot be recorded: %w", err)
	}
	if err := c.emitter.EmitEvent(ctx, attest.EventEngineSet, []byte(payload)); err != nil {
		return fmt.Errorf("recording the engine set in RTMR3: %w", err)
	}
	return nil
}

// engineEnv is the environment every engine gets, and no caller supplies.
//
// HF_TOKEN comes from this process's own environment rather than from a request, which is
// the one env var with a secret in it — a request that could set it would publish it,
// because the whole spec goes into a record RTMR3 serves to anyone.
func (c *Ctrl) engineEnv() map[string]string {
	env := map[string]string{}
	for k, v := range c.config.EngineEnv {
		env[k] = v
	}
	if tok := os.Getenv("HF_TOKEN"); tok != "" {
		env["HF_TOKEN"] = tok
	}
	return env
}

// gpusAreFree says whether every card the value names is unoccupied.
//
// Two directions, both of which have to hold: a request for a specific card is refused
// when something occupies docker.GPUAll, and a request for docker.GPUAll is refused when
// anything occupies any card. An unnarrowed holder can use every card, so it collides
// with everything.
//
// That has a consequence worth stating, because it is a DEPLOYMENT requirement and not
// something a request can work around: the engines in this project's own compose run with
// NVIDIA_VISIBLE_DEVICES=all, so on such a machine every card is occupied and nothing can
// be placed until the compose narrows its own engine to the cards it actually uses. The
// second engine naming its cards does not help; the first one has to stop claiming all of
// them.
//
// Containers controller.engineGPUIgnore names are skipped — see that field. Without it
// dcgm-exporter alone, which every deployment here runs with `runtime: nvidia` and
// NVIDIA_VISIBLE_DEVICES=all, would occupy the whole machine forever.
//
// Racy by construction: the check and the create are two calls, and nothing stops a
// compose from starting a container in between. It is the cheap answer that catches the
// mistake an operator actually makes — placing a second engine on an occupied card — and
// docker refuses a name collision on its own. Nothing here relies on it for safety.
func (c *Ctrl) gpusAreFree(ctx context.Context, want string) (bool, error) {
	alloc, err := c.GPUAllocation(ctx)
	if err != nil {
		return false, err
	}
	occupied := func(gpu string) bool {
		for _, claim := range alloc[gpu] {
			if !claim.Monitoring {
				return true
			}
		}
		return false
	}
	for _, gpu := range (docker.Container{HasGPU: true, GPUs: want}).HeldGPUs() {
		if occupied(gpu) || occupied(docker.GPUAll) {
			return false, nil
		}
	}
	return true, nil
}

// validateEngineSpec is the allowlist. Everything it refuses, it refuses because the
// record could not describe the result or because the container could reach past its own
// boundary.
func (c *Ctrl) validateEngineSpec(spec EngineSpec, img engineImageRule) error {
	// A name attest's record refuses would make the whole set unreadable, so the same
	// pattern gates the API. Checked through attest rather than restated, because two
	// copies of the rule that decides the record's readability would drift.
	if !attest.ValidUpstreamName(spec.Name) {
		return refusef("engine name %q is not a lowercase alphanumeric container name (dashes and underscores, 63 bytes max)", spec.Name)
	}
	if spec.Port <= 0 || spec.Port > 65535 {
		return refusef("port %d is not a port", spec.Port)
	}
	if strings.TrimSpace(spec.GPUs) == "" {
		return refusef("an engine must name the GPUs it may see, as an %s value; \"all\" is accepted and means every card, but the field cannot be left out", docker.GPUEnvVar)
	}
	if spec.Model.Repo == "" {
		return refusef("an engine must name a model repository")
	}
	// Refused here rather than left to RenderEngineSet, which also refuses them: by the
	// time the render runs, the image has been pulled — a several-minute wait to be told
	// about a tab. The render keeps its own check because it is the last thing before the
	// ledger and a second caller would inherit none of this.
	for _, f := range []struct{ what, value string }{
		{"GPU list", spec.GPUs},
		{"model repository", spec.Model.Repo},
		{"argument list", strings.Join(spec.Args, "")},
	} {
		if strings.ContainsAny(f.value, "\t\n\r") {
			return refusef("the %s contains a tab or a newline, which are the field and record separators of the %s grammar: a spec carrying one could not be recorded", f.what, attest.EventEngineSet)
		}
	}
	// The rule this whole type exists for. See EngineModel.Revision.
	if len(spec.Model.Revision) != engineRevisionLen || strings.TrimLeft(spec.Model.Revision, "0123456789abcdef") != "" {
		return refusef("model revision %q must be a %d-character lowercase hex commit sha: a repo id names what was asked for, only a revision names what arrives", spec.Model.Revision, engineRevisionLen)
	}
	// Flags the controller sets itself are refused rather than overridden, so a caller
	// cannot quietly disagree with the record. --host in particular: a caller that could
	// set it could bind the engine somewhere the broker is not the only reachable client.
	for _, a := range spec.Args {
		flag, _, _ := strings.Cut(a, "=")
		for _, reserved := range img.reservedFlags() {
			if flag == reserved {
				return refusef("%s is set by the controller and may not be passed: it decides what the record describes", reserved)
			}
		}
	}
	return nil
}

// engineImageFor resolves the configured rule for an image, refusing an image no rule
// covers.
//
// The rule carries how the image takes a model, which is why an unconfigured image cannot
// be run at all: without it the controller would not know which flag to put the repo on,
// and a record built from a guess would describe something else. Configuring an image is
// therefore the same act as declaring it permitted.
func (c *Ctrl) engineImageFor(image string) (engineImageRule, error) {
	repo, digest, pinned := strings.Cut(image, "@")
	if !pinned || !imageDigestPattern.MatchString(digest) {
		return engineImageRule{}, refusef("image %q must pin a digest as <repo>@sha256:<64hex>: a tag names what was asked for, not what arrives", image)
	}
	for _, e := range c.config.Engines {
		if e.ImageRepo == repo {
			rule := engineImageRule{EngineImage: e}
			return rule, rule.validate()
		}
	}
	configured := make([]string, 0, len(c.config.Engines))
	for _, e := range c.config.Engines {
		configured = append(configured, e.ImageRepo)
	}
	return engineImageRule{}, refusef("image repository %q is not configured under controller.engines (configured: %v); a rule there says how the image takes a model, and without one no record could describe what it runs", repo, configured)
}

// engineImageRule is one config.EngineImage after the controller has checked it is
// usable, and the place that knows how to turn a request into an argument list.
//
// A wrapper type rather than methods on the config struct, so the config package stays
// free of the controller's decisions — which flags are forced, and in what order.
type engineImageRule struct {
	config.EngineImage
}

// validate refuses a rule that cannot describe what it would run.
//
// Checked when the rule is USED, not at config load, and that is on purpose: the config
// struct is shared with the broker and event binaries, which never create engines, so a
// malformed entry must not be able to keep them from booting. The cost is that a bad
// entry is discovered by the first request instead of at startup, and the error says
// which key is wrong.
func (r engineImageRule) validate() error {
	switch {
	case r.ModelFlag == "":
		return refusef("controller.engines entry for %q sets no modelFlag, so there is nowhere to put the model repository", r.ImageRepo)
	case r.RevisionFlag == "":
		return refusef("controller.engines entry for %q sets no revisionFlag: an engine that cannot be pinned to a model revision cannot be recorded, because the record would name code that can change under that name", r.ImageRepo)
	case r.PortFlag == "":
		return refusef("controller.engines entry for %q sets no portFlag, so the broker would not know where to reach it", r.ImageRepo)
	}
	return nil
}

// engineBindAddress is what HostFlag is forced to.
//
// 0.0.0.0 inside a container with no published ports is reachable only from the compose
// network, which is where the broker is. Forced rather than defaulted, because a caller
// that could narrow it could make the engine unreachable, and one that could widen it
// could not — there is nothing wider.
const engineBindAddress = "0.0.0.0"

// buildArgs assembles the container's argument list: the flags the controller decides,
// then the caller's.
//
// The controller's come FIRST so a duplicate the validator somehow let through would be
// overridden by the caller's rather than silently winning — engines take the last
// occurrence of a repeated flag. That ordering makes the failure visible in the record
// (both occurrences are recorded) instead of producing a container that disagrees with
// what the record says it runs.
func (r engineImageRule) buildArgs(repo, revision string, port int, extra []string) []string {
	args := []string{
		r.ModelFlag, repo,
		r.RevisionFlag, revision,
		r.PortFlag, strconv.Itoa(port),
	}
	if r.HostFlag != "" {
		args = append(args, r.HostFlag, engineBindAddress)
	}
	return append(args, extra...)
}

// reservedFlags are the flags a request may not pass, which is exactly the set the
// controller sets itself.
//
// Derived from the rule rather than listed separately, so a new forced flag cannot be
// added to buildArgs without becoming reserved — the drift that would otherwise let a
// caller quietly disagree with the record.
func (r engineImageRule) reservedFlags() []string {
	flags := []string{r.ModelFlag, r.RevisionFlag, r.PortFlag}
	if r.HostFlag != "" {
		flags = append(flags, r.HostFlag)
	}
	return flags
}

// ErrEngineRefused marks an error that is about the REQUEST or about the deployment,
// rather than about a failure to carry the request out.
//
// The distinction exists for the status code, and the status code matters because it is
// what tells a client whether to retry. A spec the allowlist rejects and a deployment
// with no attestation proxy are both permanent until something changes outside this
// process; a docker daemon that could not be reached is not. Mapping the two together
// would either invite a retry into the same refusal or suppress one that would succeed.
var ErrEngineRefused = errors.New("refused")

// refusef builds a refusal. The %w goes first so errors.Is works and the message reads
// as its own sentence.
func refusef(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrEngineRefused, fmt.Sprintf(format, a...))
}
