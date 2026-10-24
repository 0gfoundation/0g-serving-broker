package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// In-process instances are the extra provider instances the primary broker and
// event processes serve themselves (config.InstancesDirEnvVar), as opposed to
// the extra broker/event containers InstancesEnvVar declares. The controller
// reads the same variable from its OWN environment, naming its own mount of the
// directory the broker reads; a compose literal, so compose_hash covers which
// directory that is.
//
// Every <name>.yaml in the directory is an instance. The controller never adds
// or removes one; it reads and replaces existing files only. A file appears when
// the deployment's init container writes it, and stays until someone deletes it:
// the init container skips existing files and never removes one, so dropping an
// instance from the deployment's environment does not stop it being served. The
// upstream-set record still covers every file present, so the attestation stays
// true to what runs.
//
// Why the directory and not a list: the containers an instance runs in are the
// primary's, fixed constants, so there is no container name here a list would
// have to pin down — the reason InstancesEnvVar must be a compose literal.

// checkConfigDir refuses an in-process instance directory alongside container
// instances: extra instances run in one place or the other.
//
// Unlike container instances, in-process ones combine with
// controller.recordUpstreamSet: the set is recorded over every instance's config,
// each member tagged with its provider (inProcessUpstreams).
func checkConfigDir(configDir string, instances []Instance) error {
	if configDir != "" && len(instances) > 0 {
		return fmt.Errorf("%s and %s are both set: extra instances run either in the primary processes or in containers of their own, not both", config.InstancesDirEnvVar, InstancesEnvVar)
	}
	return nil
}

// inProcessConfigFile returns the config file of the in-process instance name,
// or false when the controller serves none by that name.
func (c *Ctrl) inProcessConfigFile(name string) (string, bool) {
	if c.configDir == "" || !instanceNamePattern.MatchString(name) {
		return "", false
	}
	path := filepath.Join(c.configDir, name+".yaml")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

// hasInProcessInstances reports whether the configured directory holds any
// instance config.
func (c *Ctrl) hasInProcessInstances() bool {
	if c.configDir == "" {
		return false
	}
	paths, err := filepath.Glob(filepath.Join(c.configDir, "*.yaml"))
	return err == nil && len(paths) > 0
}

// brokerInstanceEnv reads, from the broker container, the two variables that
// decide what the set validation has to assume: its DATABASE_DSN, which names
// every instance's database server, and its CONFIG_DIR. The controller's own
// environment need not carry either the way the broker's does.
func (c *Ctrl) brokerInstanceEnv(ctx context.Context) (dsn, configDir string, err error) {
	env, err := c.dockerClient.GetContainerEnv(ctx, containerBroker, []string{"DATABASE_DSN", config.InstancesDirEnvVar})
	if err != nil {
		return "", "", err
	}
	return env["DATABASE_DSN"], env[config.InstancesDirEnvVar], nil
}

// checkConfigDirAgrees refuses a change when the broker serves in-process
// instances and this controller does not know it, or the reverse. Either way the
// controller would record — and bind the keys to — an upstream set that leaves
// out destinations the broker forwards to, or validate a set the broker does not
// serve. Both come from compose literals, so this is a deployment mistake, and
// the change is refused before anything is recorded.
func (c *Ctrl) checkConfigDirAgrees(brokerConfigDir string) error {
	if (brokerConfigDir != "") == (c.configDir != "") {
		return nil
	}
	return &UnconfirmedConfigError{Err: fmt.Errorf("the broker container has %s=%q but this controller has %s=%q: both must name the instance directory, or neither", config.InstancesDirEnvVar, brokerConfigDir, config.InstancesDirEnvVar, c.configDir)}
}

// validateInProcessSet runs the broker's own check on the whole set the process
// would serve after a change: primaryContent as the primary config and, when
// name is set, content as that instance's. It is called with `changing` held, so
// no other change can move the files it validates against before this one is
// written.
func (c *Ctrl) validateInProcessSet(ctx context.Context, primaryContent []byte, name string, content []byte) error {
	dsn, brokerConfigDir, err := c.brokerInstanceEnv(ctx)
	if err != nil {
		return &UnconfirmedConfigError{Err: fmt.Errorf("reading the broker container's environment, which decides the instances' databases: %w", err)}
	}
	if err := c.checkConfigDirAgrees(brokerConfigDir); err != nil {
		return err
	}
	if err := config.ValidateInstanceSet(c.configDir, primaryContent, name, content, dsn); err != nil {
		return &InvalidConfigError{Err: err}
	}
	return nil
}

// restoreInProcessUpstreamSet records the set the files on disk permit, after a
// change recorded a set for content it then failed to write. Without it the
// ledger would go on naming the set of a config the process does not run, and
// the next start would bind keys to it.
func (c *Ctrl) restoreInProcessUpstreamSet(ctx context.Context, tag string) {
	if !c.config.RecordUpstreamSet {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), restoreTimeout)
	defer cancel()
	primary, err := os.ReadFile(c.config.ConfigFile)
	var members []attest.Upstream
	if err == nil {
		members, err = c.inProcessUpstreams(primary, "", nil)
	}
	if err := c.recordUpstreamSet(ctx, tag, members, err); err != nil {
		c.logger.Errorf("[%s] RTMR3 names the upstream set of a config that was not written, and restoring it failed: %v", tag, err)
	}
}

// applyInProcessInstanceConfig is ApplyInstanceConfig for an in-process
// instance: validated with the whole set, recorded in RTMR3 as
// EventInstanceConfigUpdate before the write — and, with recordUpstreamSet on,
// the process's whole upstream set recorded again — the config record restored
// if either fails, then the primary broker and event restarted: they are the
// processes that serve it, so every instance restarts with them.
func (c *Ctrl) applyInProcessInstanceConfig(ctx context.Context, name, path, content string) error {
	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), configChangeTimeout)
	defer cancel()

	primaryContent, err := os.ReadFile(c.config.ConfigFile)
	if err != nil {
		return fmt.Errorf("reading the primary config %s: %w", c.config.ConfigFile, err)
	}
	if err := c.validateInProcessSet(ctx, primaryContent, name, []byte(content)); err != nil {
		return err
	}

	// Under the lock, for the reason ApplyCoreConfig gives: the processes
	// restarting onto the file are the primary pair.
	if err := c.checkIgnoredKeysAreSafe(ctx, content, primaryTargets); err != nil {
		return &UnconfirmedConfigError{Err: err}
	}

	sum := sha256.Sum256([]byte(content))
	if err := c.emitter.EmitEvent(ctx, attest.EventInstanceConfigUpdate, []byte(name+" "+hex.EncodeToString(sum[:]))); err != nil {
		return fmt.Errorf("recording the config change of instance %q in RTMR3: %w", name, err)
	}

	in := Instance{Name: name, ConfigFile: path}
	// Before the write, as ApplyCoreConfig records the primary's: the set covers every
	// instance, so this change can move it, and the record must not still name the old
	// set once the processes restart onto the new file.
	if err := c.recordInProcessInstanceUpstreamSet(ctx, name, content); err != nil {
		return c.abortInstanceConfigChange(ctx, in, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		err = c.abortInstanceConfigChange(ctx, in, err)
		c.restoreInProcessUpstreamSet(ctx, "ApplyInstanceConfig "+name)
		return err
	}

	// Straight to the docker client: the Ctrl methods take `changing`, held here.
	if err := c.dockerClient.RestartContainer(ctx, containerBroker); err != nil {
		return fmt.Errorf("failed to restart broker: %w", err)
	}
	if err := c.dockerClient.RestartContainer(ctx, containerEvent); err != nil {
		return fmt.Errorf("failed to restart event: %w", err)
	}
	return c.reloadIngress(ctx)
}
