package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Dstack-TEE/dstack/sdk/go/dstack"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"gopkg.in/yaml.v3"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
	"github.com/0glabs/0g-serving-broker/controller/internal/docker"
	"github.com/0glabs/0g-serving-broker/inference/config"
	"github.com/0glabs/0g-serving-broker/inference/contract"
)

// EventEmitter extends RTMR3 with one runtime event.
//
// RTMR3 is append-only hardware state: an event folded into it cannot be edited
// or removed, and it is covered by the signature over any quote taken afterwards.
// That is the whole reason the controller records a change here before making it
// — the record outlives the process that wrote it and does not depend on the
// process being honest later.
//
// The property the two change paths owe a reader is therefore not "a record exists"
// but: **the last image record names the image the broker will be running when it
// next serves a quote, and the last config record names the file it will read.**
// Recording first is how a change cannot happen unrecorded; appending the truth on
// abort (abortImageChange, abortConfigChange) is how a record cannot outlive the
// change it describes. Both halves are needed — the ledger is append-only, so the
// second cannot be done by rewinding.
//
// An interface only so the ordering below can be tested; production is the dstack
// client over /var/run/dstack.sock.
type EventEmitter interface {
	EmitEvent(ctx context.Context, event string, payload []byte) error
}

// SignerDeriver reports the address of the response-signing key derived from an image.
//
// The record has to carry this, not just the digest. A reader can replay the ledger and can
// verify responses against the address in the quote's report_data, but it cannot check that
// those describe the same thing: report_data is whatever the enclave asked the hardware to
// sign over, and the per-image key is derivable only inside the CVM. So the ledger would
// name an image while the signatures came from somewhere unrelated, and nothing would
// notice — a broker running unreviewed code could publish an address of its own, and a
// record left standing after a change that did not complete would be believed.
//
// Deriving it here closes that, because this side of the socket can do what a reader
// cannot. The controller derives the address the image WILL have, writes it into the
// append-only record, and a reader then requires the quote to name the same one.
//
// An interface for the same reason EventEmitter is one: so the ordering can be tested.
type SignerDeriver interface {
	// ImageKeys returns the response-signing address and the enclave encryption public key
	// (hex, no 0x) that an image's derivation path yields.
	//
	// Both, not just the address, because report_data carries both. A reader that checked
	// only the address would still let an image that is not the recorded one publish an
	// enc_pub of its own — and a client seals its request to that key BEFORE any response
	// signature exists to contradict it. The prompt would reach unreviewed code and the
	// signature check would come too late to matter.
	ImageKeys(ctx context.Context, digest string) (signer, encPub string, err error)
}

// dstackSignerDeriver derives through the guest agent, which is the only thing holding the
// app key the derivation starts from.
type dstackSignerDeriver struct {
	client *dstack.DstackClient
}

func (d dstackSignerDeriver) ImageKeys(ctx context.Context, digest string) (string, string, error) {
	if !imageDigestPattern.MatchString(digest) {
		return "", "", fmt.Errorf("cannot derive keys for %q, which is not a digest", digest)
	}

	signerKey, err := d.client.GetKey(ctx, attestproxy.SignerKeyPath(digest), "")
	if err != nil {
		return "", "", fmt.Errorf("deriving the signing key for %s: %w", digest, err)
	}
	key, err := crypto.HexToECDSA(strings.TrimPrefix(signerKey.Key, "0x"))
	if err != nil {
		return "", "", fmt.Errorf("parsing the derived signing key for %s: %w", digest, err)
	}

	// The same path and the same pass-through the broker uses, or the two derive different
	// keys and every sealed request fails to open.
	encMaterial, err := d.client.GetKey(ctx, attestproxy.EncKeyPath(digest), "")
	if err != nil {
		return "", "", fmt.Errorf("deriving the enc key for %s: %w", digest, err)
	}
	encPub, err := tee.EncPublicKeyFromMaterial(encMaterial.Key)
	if err != nil {
		return "", "", fmt.Errorf("deriving the enc public key for %s: %w", digest, err)
	}

	// Lowercased so the record and a reader's comparison agree on one spelling.
	return strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex()), hex.EncodeToString(encPub), nil
}

// Ceilings on how long a recorded change may hold the controller.
//
// Not tuning knobs: they exist so the lock cannot be held forever. Both paths hold a
// lock that also gates start/stop/restart, and an upgrade's pull has no timeout of its
// own, so without these a registry that never answers would leave an operator unable
// to restart the broker to recover. Generous enough for a multi-gigabyte pull plus the
// two-minute health wait.
const (
	upgradeTimeout      = 30 * time.Minute
	configChangeTimeout = 5 * time.Minute

	// restoreTimeout bounds putting the ledger back. Deliberately separate: a restore
	// runs *because* something failed, often the deadline above, and must not inherit
	// the context that expired.
	restoreTimeout = 30 * time.Second
)

// ErrChangeInProgress is returned when a change is refused because another one
// holds the controller.
var ErrChangeInProgress = errors.New("another image or config change is in progress")

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

// imageDigestPattern is what the upgrade entry point accepts as a digest.
//
// Lowercase hex only, because that is what docker's own digest parser accepts:
// anything else names an image no daemon will resolve, and letting it through
// would only move the failure to the pull.
var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// InvalidDigestError is returned when an upgrade is asked for by something
// other than a well-formed image digest.
type InvalidDigestError struct {
	Digest string
}

func (e *InvalidDigestError) Error() string {
	return fmt.Sprintf("invalid image digest %q: expected \"sha256:\" followed by 64 lowercase hex characters", e.Digest)
}

// ValidateDigest checks that digest names an image by content.
func ValidateDigest(digest string) error {
	if !imageDigestPattern.MatchString(digest) {
		return &InvalidDigestError{Digest: digest}
	}
	return nil
}

// validateImageRepo rejects a configured repository that already carries a tag
// or a digest.
//
// UpdateImages builds exactly one reference, imageRepo + "@" + digest. A repo
// carrying either produces a reference docker cannot resolve, and defeats the
// point of a digest-only entry point: which image runs is the caller's digest,
// never something the config or a re-pointed tag decides.
//
// Only the last path segment is checked for ':', since a registry host may
// carry a port — "localhost:5000/0g-serving-broker" is a bare repo.
func validateImageRepo(repo string) error {
	if repo == "" {
		return errors.New("controller.imageRepo is required")
	}
	if strings.Contains(repo, "@") {
		return fmt.Errorf("controller.imageRepo %q must name a repository only, without a digest", repo)
	}
	if strings.Contains(repo[strings.LastIndex(repo, "/")+1:], ":") {
		return fmt.Errorf("controller.imageRepo %q must name a repository only, without a tag", repo)
	}
	return nil
}

// Ctrl is the controller for managing containers and configs
type Ctrl struct {
	config       config.ControllerConfig
	fullConfig   *config.Config // Full config for accessing Service configuration
	dockerClient *docker.Client
	emitter      EventEmitter
	deriver      SignerDeriver

	// Serializes the two paths that record into RTMR3 and then act.
	//
	// RTMR3 is a ledger, and two upgrades interleaving would write one whose
	// events do not describe the order things happened in: the last
	// zg-image-update is what a reader believes is running, and with concurrent
	// callers the last event and the last container created can come from
	// different requests. Held for the whole operation, not just the emit.
	changing sync.Mutex

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

	if err := validateImageRepo(cfg.ImageRepo); err != nil {
		return nil, err
	}

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

	// One client for both jobs it does over that socket: appending to RTMR3, and deriving
	// the per-image key whose address goes into the record.
	dstackClient := dstack.NewDstackClient()

	ctrl := &Ctrl{
		config:       cfg,
		fullConfig:   fullConfig,
		dockerClient: dockerClient,
		// Talks to /var/run/dstack.sock, which the controller's compose entry has
		// to mount. Not dialled here: the client is lazy, and a controller that
		// refused to start without it would take the read-only endpoints down too.
		// An upgrade attempted without the socket fails at the emit, before
		// anything is touched, which is the outcome that matters.
		emitter:        dstackClient,
		deriver:        dstackSignerDeriver{client: dstackClient},
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

	// Under the same lock as the recorded changes, even though this records
	// nothing itself. Starting the broker is what seals a quote around whatever
	// the ledger says at that moment, so a start let through the middle of an
	// upgrade would hand a reader a quote describing an image the upgrade had
	// announced but not yet installed.
	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

	return c.dockerClient.StartContainer(ctx, containerName)
}

// StopContainer stops a container by alias
func (c *Ctrl) StopContainer(ctx context.Context, alias string) error {
	containerName := c.getContainerName(alias)
	if containerName == "" {
		return &InvalidContainerError{Alias: alias}
	}

	// Under the same lock as the recorded changes, even though this records
	// nothing itself. Starting the broker is what seals a quote around whatever
	// the ledger says at that moment, so a start let through the middle of an
	// upgrade would hand a reader a quote describing an image the upgrade had
	// announced but not yet installed.
	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

	return c.dockerClient.StopContainer(ctx, containerName)
}

// RestartContainer restarts a container by alias
func (c *Ctrl) RestartContainer(ctx context.Context, alias string) error {
	containerName := c.getContainerName(alias)
	if containerName == "" {
		return &InvalidContainerError{Alias: alias}
	}

	// Under the same lock as the recorded changes, even though this records
	// nothing itself. Starting the broker is what seals a quote around whatever
	// the ledger says at that moment, so a start let through the middle of an
	// upgrade would hand a reader a quote describing an image the upgrade had
	// announced but not yet installed.
	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

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

// ApplyCoreConfig updates the shared config file and restarts both broker and
// event containers.
//
// configContent is a raw YAML string, to avoid parsing issues with hex addresses.
// It is hashed and recorded in RTMR3 before the file is written, so a reader can
// tell whether the running configuration is still the one the compose_hash
// pinned — and if not, which content replaced it. A failure to record aborts
// before the write: an unrecorded change is a change nobody can see. A write that
// then fails restores the record, for the reasons in abortConfigChange.
//
// The restarts are not only how the new config takes effect, they are also what
// publishes the record — the broker serves a quote it took at startup, so an event
// emitted while it runs reaches no reader until it restarts.
func (c *Ctrl) ApplyCoreConfig(ctx context.Context, configContent string) error {
	// Validate YAML format (but don't use parsed result to preserve original content)
	var tmp interface{}
	if err := yaml.Unmarshal([]byte(configContent), &tmp); err != nil {
		return &InvalidConfigError{Err: err}
	}

	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

	// Detached and bounded for the same reasons as UpdateImages.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), configChangeTimeout)
	defer cancel()

	sum := sha256.Sum256([]byte(configContent))
	if err := c.emitter.EmitEvent(ctx, attest.EventConfigUpdate, []byte(hex.EncodeToString(sum[:]))); err != nil {
		return fmt.Errorf("recording the config change in RTMR3: %w", err)
	}

	if err := os.WriteFile(c.config.ConfigFile, []byte(configContent), 0644); err != nil {
		return c.abortConfigChange(ctx, err)
	}

	// Restart both broker and event since they share the config.
	//
	// Straight to the docker client, not through the Ctrl methods: those take
	// `changing`, which this call already holds.
	if err := c.dockerClient.RestartContainer(ctx, containerBroker); err != nil {
		return fmt.Errorf("failed to restart broker: %w", err)
	}
	if err := c.dockerClient.RestartContainer(ctx, containerEvent); err != nil {
		return fmt.Errorf("failed to restart event: %w", err)
	}

	return nil
}

// AmbiguousContainerError is returned when a container the upgrade must act on cannot
// be found under its exact name.
type AmbiguousContainerError struct {
	Want string
	Got  string // the name resolution settled on, or "" if nothing matched
}

func (e *AmbiguousContainerError) Error() string {
	if e.Got == "" {
		return fmt.Sprintf("no container named %q; set container_name: %s in the compose file", e.Want, e.Want)
	}
	return fmt.Sprintf("no container named %q — %q matched instead, and acting on it would recreate the wrong service; set container_name: %s in the compose file", e.Want, e.Got, e.Want)
}

// verifyExactContainer refuses to proceed unless name resolves to a container of
// exactly that name.
func (c *Ctrl) verifyExactContainer(ctx context.Context, name string) error {
	status, err := c.dockerClient.GetContainerStatus(ctx, name)
	if err != nil {
		return fmt.Errorf("looking up container %s: %w", name, err)
	}
	if status == nil {
		return &AmbiguousContainerError{Want: name}
	}
	if status.Name != name {
		return &AmbiguousContainerError{Want: name, Got: status.Name}
	}
	return nil
}

// abortImageChange restores the image record and returns the error to report.
//
// One caller: the recreate. Everything before it — the pull, the two stops, the
// record itself — runs before the record exists or leaves nothing to correct;
// everything after it has the broker on the new image, so the record is already right
// and restoring would replace a true record with a false one.
func (c *Ctrl) abortImageChange(ctx context.Context, cause error) error {
	// Its own context, not the upgrade's. The upgrade's deadline may be exactly what
	// aborted it, and a restore that inherits an expired one cannot run at all —
	// which leaves the ledger overstating on precisely the path that exists to stop
	// that. Two local calls, so the budget is small.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()

	if err := c.restoreImageRecord(ctx); err != nil {
		c.logger.Errorf("[UpdateImages] RTMR3 names an image the broker is not running and the record could not be restored: %v", err)
		return errors.Join(cause, fmt.Errorf("RTMR3 still names the image this upgrade did not reach, and restoring it failed: %w", err))
	}
	c.logger.Info("[UpdateImages] Upgrade aborted; RTMR3 record restored to the image the broker is running")
	return cause
}

// restoreImageRecord appends a record naming the image the broker is actually on.
//
// RTMR3 cannot be edited or rewound, so a record that turned out not to describe
// reality is corrected the only way an append-only ledger allows: by appending the
// truth after it. A reader takes the last image record as what is running, which is
// what this restores.
//
// Leaving the overstatement instead is not the conservative direction, which is what
// makes this load-bearing rather than tidy. The stale record names the digest the
// caller asked for — for an attacker, the digest a verifier is looking for — while
// the broker keeps running whatever it ran before. "A reader would reject it" only
// holds when the stale record names something the reader was not looking for.
//
// It follows that this must fail CLOSED. Every outcome emits something, and when the
// truth cannot be established the payload deliberately names no digest, which
// attest.ResolveRunningState refuses. An earlier version returned an error instead
// for those cases, on the reasoning that a broker whose reference carries no digest
// belongs to a deployment attest cannot verify anyway — which is wrong: the
// compose-pinned fallback is only consulted when there is no image record at all, so
// the stale one would have made an unverifiable deployment verifiably wrong.
//
// The broker's reference is re-read rather than captured beforehand, because the
// abort paths differ in how far they got: one leaves it stopped on the old image,
// another created-but-not-started on the new one, another with no container at all.
func (c *Ctrl) restoreImageRecord(ctx context.Context) error {
	// Names no digest, so a reader refuses rather than believing the record this is
	// replacing. Used whenever the broker's own reference cannot be established.
	unknown := c.config.ImageRepo

	payload := unknown
	switch status, err := c.dockerClient.GetContainerStatus(ctx, containerBroker); {
	case err != nil:
		c.logger.Warnf("[UpdateImages] Could not read the broker's image to restore the RTMR3 record, recording it as unknown: %v", err)
	case status == nil:
		// The abort removed the container and did not get one back.
		c.logger.Warnf("[UpdateImages] No %s container to read an image from, recording the running image as unknown", containerBroker)
	case status.Name != containerBroker:
		// GetContainerStatus falls back to a shortest-substring match, so a
		// deployment that does not pin container_name can resolve this to a
		// neighbour — "0g-serving-provider-broker-db" contains the broker's whole
		// name. Fine for a status endpoint, not for something a reader treats as
		// the attested image.
		c.logger.Warnf("[UpdateImages] %q resolved to container %q, recording the running image as unknown", containerBroker, status.Name)
	default:
		// The record must bind the address too, or a reader refuses it — which is the
		// right outcome when the truth cannot be established, and the wrong one here,
		// where it can. Derived from the digest this container actually runs, so the
		// record describes the process that will answer the next quote.
		// On its own budget, a fraction of the restore's, because the emit below still has
		// to happen. The derivation and the emit talk to the SAME dstack socket, so the
		// failure that most plausibly requires a restore is also the one that hangs the
		// derivation — and letting it consume the whole deadline would leave the emit
		// failing instantly on an expired context, with the stale record standing as the
		// ledger's last word. That is the fail-open this function exists to prevent,
		// reintroduced one line further in.
		lookupCtx, cancelLookup := context.WithTimeout(ctx, restoreTimeout/3)
		defer cancelLookup()

		digest, digestErr := c.RunningBrokerDigest(lookupCtx)
		if digestErr != nil {
			c.logger.Warnf("[UpdateImages] Could not resolve the broker's digest to restore the RTMR3 record, recording it as unknown: %v", digestErr)
			break
		}
		signer, encPub, keyErr := c.deriver.ImageKeys(lookupCtx, digest)
		if keyErr != nil {
			c.logger.Warnf("[UpdateImages] Could not derive the keys for %s to restore the RTMR3 record, recording it as unknown: %v", digest, keyErr)
			break
		}
		payload = c.config.ImageRepo + "@" + digest + " " + signer + " " + encPub
	}

	return c.emitter.EmitEvent(ctx, attest.EventImageUpdate, []byte(payload))
}

// abortConfigChange restores the config record and returns the error to report.
//
// Same reasoning as restoreImageRecord, including the fail-closed part: the recorded
// hash would otherwise name content that was never applied, and the caller picks
// which content that is. When the file cannot be read the payload deliberately names
// no hash, which attest.ResolveRunningState refuses.
//
// The file is re-read rather than assumed unchanged, because os.WriteFile truncates
// before it writes — a failure part-way through leaves content that is neither the
// old nor the new, and that is what the record has to name.
func (c *Ctrl) abortConfigChange(ctx context.Context, cause error) error {
	// Its own context, for the reason given in abortImageChange.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()

	// Not a hex sha256, so a reader refuses rather than believing the record this
	// is replacing.
	payload := "unknown"
	if content, err := os.ReadFile(c.config.ConfigFile); err != nil {
		c.logger.Warnf("[ApplyCoreConfig] Could not re-read the config file to restore the RTMR3 record, recording it as unknown: %v", err)
	} else {
		sum := sha256.Sum256(content)
		payload = hex.EncodeToString(sum[:])
	}

	if err := c.emitter.EmitEvent(ctx, attest.EventConfigUpdate, []byte(payload)); err != nil {
		c.logger.Errorf("[ApplyCoreConfig] RTMR3 names config content that was not applied and the record could not be restored: %v", err)
		return errors.Join(cause, fmt.Errorf("RTMR3 still names the config this change did not apply, and restoring it failed: %w", err))
	}
	c.logger.Info("[ApplyCoreConfig] Change aborted; RTMR3 record restored to the config on disk")
	return cause
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

// GetImageInfo returns information about the image the broker is running.
//
// It reads the reference off the broker container rather than off the config.
// The config now holds a bare repository, and docker resolves a bare name to
// its :latest tag — a tag a digest-pinned deployment need not have locally at
// all, and one that pulling repo@digest does not create. Inspecting the config
// would therefore answer with a different image than the one running, or fail
// outright, exactly on the deployments this change exists to serve.
func (c *Ctrl) GetImageInfo(ctx context.Context) (*docker.ImageInfo, error) {
	status, err := c.dockerClient.GetContainerStatus(ctx, containerBroker)
	if err != nil {
		return nil, err
	}
	if status == nil {
		return nil, &docker.ContainerNotFoundError{Name: containerBroker}
	}

	return c.dockerClient.GetImageInfo(ctx, status.Image)
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

// UpdateImages recreates broker and event on imageRepo@digest.
//
// Which image runs is decided by the caller's digest, not by a tag: a tag is a
// registry-side pointer, so pulling one twice can yield two different images
// and nothing downstream can tell which one is live. The digest is the image,
// so the reference built here is the only description of what runs — which is
// what lets it later be recorded as one, in a log that cannot be rewritten.
//
// The order of the container work is unchanged, and still leaves the contract
// for last so it is only updated once the containers are running the new image.
// The RTMR3 record goes in front of all of it:
// 0. Record the reference in RTMR3
// 1. Pull image
// 2. Stop containers (event -> broker)
// 3. Recreate containers (broker -> event)
// 4. Sync service to contract (only after containers are running with new image)
func (c *Ctrl) UpdateImages(ctx context.Context, digest string) (*docker.ImageUpdateResult, error) {
	if err := ValidateDigest(digest); err != nil {
		return nil, err
	}

	if !c.changing.TryLock() {
		return nil, ErrChangeInProgress
	}
	defer c.changing.Unlock()

	// Detached from the caller's request, and bounded.
	//
	// Detached because a client disconnect must not abort an upgrade half way, and
	// must not be able to make the record and the restore fail selectively — that is
	// the caller choosing which half of the accounting happens. Bounded because this
	// call holds the lock that now also gates start/stop/restart, and PullImage has no
	// timeout of its own: an unbounded pull would otherwise leave an operator unable
	// to so much as restart the broker to recover.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), upgradeTimeout)
	defer cancel()

	// The one reference this upgrade runs on. Built once so the record, the pull,
	// the recreate and the contract sync cannot end up describing different images.
	ref := c.config.ImageRepo + "@" + digest

	result := &docker.ImageUpdateResult{
		Image:             ref,
		UpdatedContainers: make([]docker.ContainerUpdateResult, 0),
	}

	brokerName := containerBroker
	eventName := containerEvent
	ingressName := containerIngress

	// Both containers must resolve by their exact names before anything is touched.
	//
	// Container lookup falls back to a shortest-substring match, which is fine for a
	// status endpoint and destructive here. Once a previous attempt has removed the
	// broker container and failed to create a replacement, the shortest remaining name
	// containing "0g-serving-provider-broker" is "0g-serving-provider-broker-db" — the
	// database in this project's own compose file. A retry would then stop it, record
	// an image change, and recreate the database container running the broker image,
	// reporting success.
	//
	// It is also what the record means: a reader treats the last image record as a
	// statement about the broker, so the upgrade has to know it acted on the broker.
	if err := c.verifyExactContainer(ctx, brokerName); err != nil {
		return nil, err
	}
	if err := c.verifyExactContainer(ctx, eventName); err != nil {
		return nil, err
	}

	// The address the broker's signing key will have once it runs ref, derived before
	// anything is touched.
	//
	// It goes into the record, because a digest alone says nothing about the process that
	// ends up holding the key — see SignerDeriver. Derived here rather than beside the emit
	// so that a guest agent that cannot answer costs nothing: the containers are still up,
	// nothing has been pulled, and the caller gets an error against an untouched deployment.
	//
	// Failing rather than recording the digest without an address is the fail-closed
	// direction, and the only one available. A record naming an image with nothing bound to
	// it is exactly as plausible as a correct one and exactly as unverifiable, so a reader
	// refuses it — which would take the deployment down as surely as this does, but after
	// the upgrade rather than before, and with the ledger permanently carrying the claim.
	// Refuse outright if this controller is not serving the attestation proxy.
	//
	// The record binds an address derived per image, and that is only the address the broker
	// publishes when the broker signs through this controller. Without the proxy the broker
	// derives its key at a fixed path instead, so every quote it serves after this would name
	// an address the record does not — and a reader would refuse the CVM permanently, because
	// no later record can name the right address either. Unlike an unreadable record, that is
	// unrecoverable, so it must not be reachable by asking for an upgrade.
	if os.Getenv(attestproxy.SocketEnvVar) == "" {
		return nil, fmt.Errorf("refusing to upgrade: %s is unset, so the broker does not sign through this controller and any record written here would name an address no quote can match. On a TEE node, regenerate the deployment so the controller serves the attestation proxy. Elsewhere there is no dstack guest agent to record a change against, and in-place upgrade is not available at all", attestproxy.SocketEnvVar)
	}

	signer, encPub, err := c.deriver.ImageKeys(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("deriving the keys for %s, which the RTMR3 record must bind: %w", ref, err)
	}

	// Step 1: Pull the latest image
	c.logger.Info("[UpdateImages] Pulling latest image...")
	imageInfo, err := c.dockerClient.PullImage(ctx, ref)
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

	// Recorded here: after both containers are stopped, immediately before the only
	// step that changes which image the broker runs.
	//
	// The placement is the security property, not a detail. A reader believes the
	// last image record, so the ledger has to be true at every instant a quote can be
	// taken — and a quote can be taken by whatever image is *currently running*,
	// which is the image under suspicion: the broker mounts /var/run/dstack.sock (it
	// needs it for GetQuote), and report_data carries no nonce, so any quote it
	// collects can be replayed forever. Locking the start/stop/restart routes does
	// not help against that, because the adversary is the live process itself.
	//
	// So the record must not exist while a broker is alive that is not on ref. Here,
	// none is: both containers were just stopped, docker's restart policy does not
	// fire on a deliberate stop, and the routes that could start one are behind the
	// lock this call holds. The next broker to exist is the one created on ref below.
	//
	// Earlier placements all failed that test. At the top of the function, or after
	// the pull, the still-running old image could collect a quote naming an image it
	// was not running — with the pull being unbounded and failable on demand, that
	// was a window a caller could hold open.
	//
	// Still before the change, so a change cannot happen unrecorded: stopping a
	// container does not alter its image, and the create below is the first thing
	// that does.
	if err := c.emitter.EmitEvent(ctx, attest.EventImageUpdate, []byte(ref+" "+signer+" "+encPub)); err != nil {
		// The broker is stopped and nothing was recorded, so the ledger is still
		// truthful — but leaving it down would turn a dstack hiccup into an outage.
		// Best effort: on failure the caller gets an error either way.
		// Its own context, for the reason in abortImageChange: if the record failed
		// because this call's deadline expired, so would the restart, and the broker
		// would be left stopped for exactly the reason this branch exists to avoid.
		startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
		defer cancelStart()
		// Both, not just the broker. This branch runs after both were stopped, so starting
		// one leaves settlement and event processing down while the broker answers
		// requests — an outage in the half nobody is watching, reported as an error that
		// mentions only RTMR3.
		for _, name := range []string{brokerName, eventName} {
			if startErr := c.dockerClient.StartContainer(startCtx, name); startErr != nil {
				c.logger.Errorf("[UpdateImages] Could not restart %s after failing to record the change: %v", name, startErr)
			}
		}
		result.Success = false
		result.Error = "failed to record the image change in RTMR3: " + err.Error()
		return result, fmt.Errorf("recording the image change to %s in RTMR3: %w", ref, err)
	}

	// Step 3: Recreate containers in dependency order (broker -> event)
	// First recreate broker
	brokerResult, err := c.dockerClient.RecreateContainer(ctx, brokerName, ref)
	if brokerResult != nil {
		result.UpdatedContainers = append(result.UpdatedContainers, *brokerResult)
	}
	if err != nil {
		// Reassigned first, so a restore that failed reaches the caller: the
		// handler reports result.Error, not the returned error, when the result
		// is non-nil — and "RTMR3 is left overstating" is the half an operator
		// has to act on.
		err = c.abortImageChange(ctx, err)
		result.Success = false
		result.Error = "failed to recreate broker container: " + err.Error()
		return result, err
	}

	// Past here the broker is on ref, so the record already says the right thing
	// and the abort paths below must NOT restore it. Everything that remains —
	// the health wait, the ingress reload, the event container, the contract sync
	// — can fail without changing which image the broker is running.

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
	eventResult, err := c.dockerClient.RecreateContainer(ctx, eventName, ref)
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
	//
	// ImageName is the repository, not ref, because additionalInfo has a second
	// writer: the broker rewrites both image fields on every start, now from the
	// IMAGE_REPO / IMAGE_DIGEST pair this upgrade just wrote into its container.
	// The two have to agree — the contract treats each flip of these fields as
	// an image change and un-acknowledges the provider for it — so this writes
	// the same halves the broker will read back, not a joined reference.
	c.logger.Info("[UpdateImages] Syncing service with new image digest...")
	if err := c.SyncService(ctx, c.config.ImageRepo, imageInfo.Digest); err != nil {
		result.Success = false
		result.Error = "failed to sync service: " + err.Error()
		return result, err
	}

	result.Success = true
	return result, nil
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

// RunningBrokerDigest reports the digest of the image the broker container runs.
//
// The attestation proxy derives per-image keys from it, so it refuses anything it cannot pin
// down exactly: a name that only matched by substring, a reference carrying no digest, or no
// container at all. A key derived from a guess would still produce signatures that verify,
// which is the one outcome worse than refusing to sign.
func (c *Ctrl) RunningBrokerDigest(ctx context.Context) (string, error) {
	status, err := c.dockerClient.GetContainerStatus(ctx, containerBroker)
	if err != nil {
		return "", fmt.Errorf("reading the broker's image: %w", err)
	}
	if status == nil {
		return "", fmt.Errorf("no %s container", containerBroker)
	}
	// Container lookup falls back to a shortest-substring match, which is fine for a status
	// endpoint and not for this: a neighbour's digest would key a signature the client
	// attributes to the broker.
	if status.Name != containerBroker {
		return "", fmt.Errorf("%q resolved to container %q, not the broker", containerBroker, status.Name)
	}
	// A reference that pins a digest already names the image the container was created on,
	// and no lookup can improve on it.
	if _, digest, pinned := strings.Cut(status.Image, "@"); pinned {
		if !imageDigestPattern.MatchString(digest) {
			return "", fmt.Errorf("the broker runs %q, whose digest is malformed", status.Image)
		}
		return digest, nil
	}

	// Otherwise resolve the image the container is RUNNING, by ID.
	//
	// Not by the reference string: a shipping compose file names the image by tag, and
	// asking the daemon what that tag points at answers "what would start now", which
	// `docker pull repo:tag` changes underneath a live container. The key would then be
	// derived from an image that is not the one serving requests — and it would be derived
	// from the image a reviewer approved while the unreviewed one answered, which is the
	// exact substitution this whole arrangement exists to prevent.
	if status.ImageID == "" {
		return "", fmt.Errorf("the broker runs %q, which pins no digest, and the daemon reported no image ID", status.Image)
	}
	info, err := c.dockerClient.GetImageInfo(ctx, status.ImageID)
	if err != nil {
		return "", fmt.Errorf("resolving the running image %s to a digest: %w", status.ImageID, err)
	}
	if !imageDigestPattern.MatchString(info.Digest) {
		// Fail closed. An image built locally and never pushed carries no digest at all, and
		// a signature under a key derived from a guess is worse than no signature, because
		// it would verify.
		return "", fmt.Errorf("the running image %s resolves to no digest", status.ImageID)
	}
	// An image known under several repositories (a mirror as well as the origin) can carry
	// more than one manifest digest, and inspecting by ID gives no repository to prefer. If
	// the entry picked here is not the one the RTMR3 record names, the signer address a
	// client derives will not match the one in report_data and the client rejects — wrong,
	// but wrong in the direction that refuses service rather than the one that accepts an
	// unreviewed image.
	return info.Digest, nil
}
