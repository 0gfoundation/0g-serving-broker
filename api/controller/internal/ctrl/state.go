package ctrl

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
)

// The state document (doc/design/report-data-v2.md §3): what a verifier reads about this
// deployment after boot, committed in the quote as report_data = enc_pub ‖ SHA-256(document)
// rather than appended to RTMR3, which dstack 0.6 no longer lets applications write.
//
//	zg-state-v2
//	image <b64url(zg-image-update payload)>
//	config <b64url(zg-config-update payload)> | config none
//	upstreams <b64url(zg-upstream-set payload)> | upstreams unrecorded
//	engines <b64url(zg-engine-set payload)>
//	instance <name> <b64url(zg-instance-config-update payload)>   (0..n, sorted by name)
//	ledger rtmr3|unavailable
//
// Lines are joined by LF with no final LF. Payloads are the exact bytes the RTMR3 records of
// the same kind carry, so a reader parses them with the grammars it already has.
//
// Every line states what is true NOW, computed when asked — not the last record written —
// so a deployment that has changed nothing since boot has a complete document too. The image
// line's keys and the document's identity come from one snapshot, and that identity is what
// the attestation proxy checks a v2 report_data against.
const stateDocumentHeader = "zg-state-v2"

// StateDocument builds the document and reports the key identity it describes.
//
// It refuses rather than guess: a set that cannot be stated (no key is served either), a
// digest it cannot resolve, keys it cannot derive, a container listing that failed, a config
// file it cannot read. A document with a line made up would be signed by the hardware.
func (c *Ctrl) StateDocument(ctx context.Context) ([]byte, attestproxy.KeyIdentity, error) {
	digest, err := c.RunningBrokerDigest(ctx)
	if err != nil {
		return nil, attestproxy.KeyIdentity{}, err
	}
	hash, setPayload, unreadable := c.boundUpstreamSetSnapshot()
	if unreadable {
		return nil, attestproxy.KeyIdentity{}, errors.New("the permitted upstream set cannot be stated")
	}
	id := attestproxy.KeyIdentity{Digest: digest, UpstreamSetHash: hash}

	signer, encPub, err := c.deriver.ImageKeys(ctx, id)
	if err != nil {
		return nil, attestproxy.KeyIdentity{}, fmt.Errorf("deriving the keys the document names: %w", err)
	}

	lines := []string{
		stateDocumentHeader,
		"image " + b64(c.config.ImageRepo+"@"+digest+" "+signer+" "+encPub),
	}

	config, err := fileRecord(c.config.ConfigFile)
	if err != nil {
		return nil, attestproxy.KeyIdentity{}, err
	}
	lines = append(lines, "config "+b64(config))

	// The bound set, and only the bound one: the document may claim a bound on where
	// plaintext goes only when the keys depend on it. A set recorded but not bound (a
	// binding that failed) is stated as invalidated, which a reader treats as no bound.
	switch {
	case !c.recordsUpstreamSet():
		lines = append(lines, "upstreams unrecorded")
	case hash == "":
		lines = append(lines, "upstreams "+b64(upstreamSetInvalidated))
	default:
		lines = append(lines, "upstreams "+b64(setPayload))
	}

	engines, err := c.engineSet(ctx)
	if err != nil {
		return nil, attestproxy.KeyIdentity{}, fmt.Errorf("listing the engines: %w", err)
	}
	enginePayload, err := attest.RenderEngineSet(engines)
	if err != nil {
		return nil, attestproxy.KeyIdentity{}, err
	}
	lines = append(lines, "engines "+b64(enginePayload))

	instances, err := c.instanceConfigFiles()
	if err != nil {
		return nil, attestproxy.KeyIdentity{}, err
	}
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sum, err := fileSHA256(instances[name])
		if err != nil {
			return nil, attestproxy.KeyIdentity{}, fmt.Errorf("instance %q: %w", name, err)
		}
		lines = append(lines, "instance "+name+" "+b64(name+" "+sum))
	}

	ledger := "rtmr3"
	if c.ledger == ledgerNone {
		ledger = "unavailable"
	}
	lines = append(lines, "ledger "+ledger)

	return []byte(strings.Join(lines, "\n")), id, nil
}

// instanceConfigFiles maps each extra instance to its config file: the container instances
// (ZG_CONTROLLER_INSTANCES) and the in-process ones (one file per instance in the config
// directory).
func (c *Ctrl) instanceConfigFiles() (map[string]string, error) {
	out := map[string]string{}
	for _, in := range c.instances {
		out[in.Name] = in.ConfigFile
	}
	if c.configDir != "" {
		paths, err := filepath.Glob(filepath.Join(c.configDir, "*.yaml"))
		if err != nil {
			return nil, err
		}
		for _, p := range paths {
			out[strings.TrimSuffix(filepath.Base(p), ".yaml")] = p
		}
	}
	return out, nil
}

// fileRecord is the zg-config-update payload for a file: its hex sha256.
func fileRecord(path string) (string, error) {
	sum, err := fileSHA256(path)
	if err != nil {
		return "", fmt.Errorf("the config file: %w", err)
	}
	return sum, nil
}

func fileSHA256(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func b64(payload string) string { return base64.RawURLEncoding.EncodeToString([]byte(payload)) }
