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
// or removes one: a file appears by redeploying, so the set the CVM serves is
// the set its deployment delivered. It reads and replaces existing files only.
//
// Why the directory and not a list: the containers an instance runs in are the
// primary's, fixed constants, so there is no container name here a list would
// have to pin down — the reason InstancesEnvVar must be a compose literal.

// checkConfigDir refuses an in-process instance directory alongside what it
// cannot be combined with.
func checkConfigDir(configDir string, instances []Instance, recordUpstreamSet bool) error {
	if configDir == "" {
		return nil
	}
	if len(instances) > 0 {
		return fmt.Errorf("%s and %s are both set: extra instances run either in the primary processes or in containers of their own, not both", config.InstancesDirEnvVar, InstancesEnvVar)
	}
	// For the reason parseInstances refuses the combination: the recorded set is
	// derived from the primary config alone.
	if recordUpstreamSet {
		return fmt.Errorf("%s is set, but controller.recordUpstreamSet is on and the recorded set would cover the primary instance only", config.InstancesDirEnvVar)
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

// validateInProcessSet runs the broker's own check on the whole set the process
// would serve after a change: primaryContent as the primary config and, when
// name is set, content as that instance's.
func (c *Ctrl) validateInProcessSet(primaryContent []byte, name string, content []byte) error {
	if err := config.ValidateInstanceSet(c.configDir, primaryContent, name, content); err != nil {
		return &InvalidConfigError{Err: err}
	}
	return nil
}

// applyInProcessInstanceConfig is ApplyInstanceConfig for an in-process
// instance: validated with the whole set, recorded in RTMR3 as
// EventInstanceConfigUpdate before the write, the record restored if the write
// fails, then the primary broker and event restarted — they are the processes
// that serve it, so every instance restarts with them.
func (c *Ctrl) applyInProcessInstanceConfig(ctx context.Context, name, path, content string) error {
	primaryContent, err := os.ReadFile(c.config.ConfigFile)
	if err != nil {
		return fmt.Errorf("reading the primary config %s: %w", c.config.ConfigFile, err)
	}
	if err := c.validateInProcessSet(primaryContent, name, []byte(content)); err != nil {
		return err
	}

	if !c.changing.TryLock() {
		return ErrChangeInProgress
	}
	defer c.changing.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), configChangeTimeout)
	defer cancel()

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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return c.abortInstanceConfigChange(ctx, in, err)
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
