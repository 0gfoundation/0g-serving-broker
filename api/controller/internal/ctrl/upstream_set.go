package ctrl

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/0glabs/0g-serving-broker/common/attest"
	"github.com/0glabs/0g-serving-broker/inference/config"
)

// upstreamSetInvalidated is the payload recorded when this controller can no longer
// say what the permitted set is. It carries no count= header, so parseUpstreamSet
// refuses it and a reader reports UpstreamsUnknown.
//
// The same device abortConfigChange uses for the config record, and for the same
// reason: RTMR3 only appends, so a record that has become wrong cannot be withdrawn —
// it can only be superseded by one that says less.
const upstreamSetInvalidated = "invalidated"

// recordUpstreamSetTimeout bounds the one emit each of the two functions below makes.
//
// It exists because the boot path had none. main.go calls RecordUpstreamSet with
// context.Background() BEFORE it starts the attestation proxy and the HTTP server, and
// the dstack SDK builds every call with http.NewRequestWithContext and adds no deadline
// of its own — so a hung /var/run/dstack.sock blocked the controller forever, and with
// it the proxy the broker needs for quotes and the /health endpoint that would have
// said so. A record of where plaintext may go must not be able to take the deployment
// down to write itself.
//
// 30 seconds, the same as restoreTimeout, which bounds the other paths whose whole
// remaining work is one emit. Long enough that a busy socket is not mistaken for a dead
// one, short enough that a dead one is a slow boot rather than no boot.
//
// Nested under whatever the caller already has: ApplyCoreConfig runs on
// configChangeTimeout, and context.WithTimeout keeps the earlier of the two deadlines,
// so this can only tighten a budget and never extend one.
const recordUpstreamSetTimeout = 30 * time.Second

// RecordUpstreamSet records the set of destinations this deployment permits.
//
// # Why this one is recorded at boot, when the other two are not
//
// The image and config records are written only by a change: a deployment that has
// changed nothing since boot needs neither, because app-compose pins the image and
// covers the config file's reference, so ResolveRunningState falls back to the compose
// pin and reports an empty ConfigSHA256 — both correct.
//
// The permitted set has no such fallback. Which destinations a model resolves to lives
// in the config file's targetUrl values, and NO boot measurement covers that content:
// the file arrives as an encrypted environment variable and an init container writes it
// into a volume, so what compose_hash covers is BROKER_CONFIG=${BROKER_CONFIG:-} and
// never the value. TARGET_URL in the compose is the one exception, and only when it is
// written literally.
//
// So without a record at boot the set is UpstreamsUnrecorded, which means "nothing here
// bounds where plaintext goes" — the fail-open state the record exists to replace. And
// RTMR3 is cleared at every boot, so recording once is not enough; it has to be every
// time this process starts.
//
// # Fail-closed
//
// A set that cannot be rendered is recorded as unreadable rather than left unrecorded.
// Those are not the same answer: unrecorded says no record was ever written, which a
// reader must treat as unbounded; unknown says a record was written and could not be
// read, which a reader treats as no set. Staying silent on a config this cannot
// express would report the weaker of the two, so it records the invalidation and logs
// what was wrong.
//
// The emit failing is different again, and is returned rather than swallowed: nothing
// was recorded, so the caller decides whether a deployment that cannot describe its own
// upstreams should start.
func (c *Ctrl) RecordUpstreamSet(ctx context.Context) error {
	if !c.config.RecordUpstreamSet {
		// Not an error and not a warning. Off is the default and the correct state until
		// every reader of this deployment's quote can read the record — see the field.
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, recordUpstreamSetTimeout)
	defer cancel()

	return c.recordUpstreamSet(ctx, "RecordUpstreamSet", &c.fullConfig.Service)
}

// RecordUpstreamSetFromContent records the set a config that is about to be written
// permits, rather than the set the running one does.
//
// # Why this is not just RecordUpstreamSet
//
// c.fullConfig is a once.Do singleton over the file on disk and nothing reloads it, so
// during ApplyCoreConfig it still describes the OLD file while the broker is about to
// restart onto the new one. Recording from it would state a bound that is not the
// deployment's.
//
// The previous version superseded the record with one a reader refuses instead — honest,
// but it left every config change reading as UpstreamsUnknown until the next boot, which
// is exactly the case this whole feature exists for: adding an upstream IS a config
// change, and "you can add one without a restart" is worth little if the record cannot
// say what you added until you restart anyway.
//
// # Order
//
// Called BEFORE the file is written, for the reason the config hash is: the record has to
// be in place before the broker restarts onto the content, or a quote taken in between
// names a set the deployment has already moved past. So the content arrives as bytes
// rather than being read back from disk.
//
// # Fail-closed, and what is deliberately NOT closed
//
// Content the loader would refuse is recorded as unreadable rather than skipped. A config
// whose keys the broker cannot parse is a config whose permitted set nobody can state,
// and unknown is that answer; staying silent would leave the OLD record standing, which
// is the one outcome that must not happen — a record that lies is worse than no record,
// because a reader trusts it.
//
// The config change itself is not refused on a parse failure. ApplyCoreConfig validates
// YAML shape and not schema today, so refusing here would make the controller accept or
// reject the same config depending on whether this switch is on — a validation rule
// hiding inside a recording switch. Tightening ApplyCoreConfig's own validation is worth
// doing and is not this.
//
// # Why ApplyCoreConfig is the only caller
//
// It is the only path that can change the effective set. The other two that touch a
// container leave it alone, and that rests on something worth naming because it could
// change: docker.imageEnvUpdates rewrites exactly IMAGE_REPO and IMAGE_DIGEST and
// mergeEnv preserves everything else, so an upgrade cannot move TARGET_URL — which is
// the one input to the set that does not live in the config file. UpdatePrometheusConfig
// rewrites only PROMETHEUS_CONFIG.
//
// So if imageEnvUpdates ever grows to include TARGET_URL, UpdateImages needs this call
// too, and until then adding it there would record a change that did not happen.
func (c *Ctrl) RecordUpstreamSetFromContent(ctx context.Context, content string) error {
	// Gated on the same switch as the boot record, and it has to be: a deployment that
	// never recorded a set has nothing to correct, and writing anything here would be the
	// FIRST zg-upstream-set event it ever emitted — which is exactly the hard-fail the
	// switch exists to keep off.
	if !c.config.RecordUpstreamSet {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, recordUpstreamSetTimeout)
	defer cancel()

	svc, err := config.ServiceFromYAML([]byte(content))
	if err != nil {
		c.logger.Errorf("[ApplyCoreConfig] The new config cannot be read the way the broker reads it, so the set it permits is unknown: %v", err)
		return c.invalidateUpstreamSet(ctx, "ApplyCoreConfig")
	}
	return c.recordUpstreamSet(ctx, "ApplyCoreConfig", svc)
}

// recordUpstreamSet is the body both entry points share: derive, render, emit, and fall
// back to the invalidation on anything that makes the set unstateable.
//
// Unexported because it assumes the two things a caller must not be able to skip — the
// switch has been checked, and the context is bounded.
//
// tag is the caller's log prefix, so an operator reading the journal can tell a boot
// record from one a config change wrote.
func (c *Ctrl) recordUpstreamSet(ctx context.Context, tag string, svc *config.Service) error {
	members, err := upstreamsFromConfig(svc)
	if err != nil {
		c.logger.Errorf("[%s] Cannot express this config's upstreams as a set, recording it as unreadable: %v", tag, err)
		return c.invalidateUpstreamSet(ctx, tag)
	}

	payload, err := attest.RenderUpstreamSet(members)
	if err != nil {
		// The reader's own refusal, surfaced here rather than by a verifier. Recorded as
		// unreadable for the same reason as above — the config permits destinations this
		// grammar cannot name, and that is not "permits nothing".
		c.logger.Errorf("[%s] This config's upstreams cannot be recorded, recording the set as unreadable: %v", tag, err)
		return c.invalidateUpstreamSet(ctx, tag)
	}

	if err := c.emitter.EmitEvent(ctx, attest.EventUpstreamSet, []byte(payload)); err != nil {
		return fmt.Errorf("recording the permitted upstream set in RTMR3: %w", err)
	}
	c.logger.Infof("[%s] Recorded %d permitted upstream(s)", tag, len(members))
	return nil
}

// invalidateUpstreamSet records a set a reader refuses, which is the honest answer when
// this process cannot state one.
//
// Unknown and unrecorded are different answers: unrecorded says no record was ever
// written, which a reader must treat as unbounded; unknown says a record was written and
// says no set. Leaving a previous record standing would be a third thing and the only
// unacceptable one — a bound that is no longer the deployment's.
//
// RTMR3 only appends, so this is the only way to withdraw a record: supersede it with one
// that says less.
func (c *Ctrl) invalidateUpstreamSet(ctx context.Context, tag string) error {
	if err := c.emitter.EmitEvent(ctx, attest.EventUpstreamSet, []byte(upstreamSetInvalidated)); err != nil {
		return fmt.Errorf("recording the upstream set as unreadable: %w", err)
	}
	c.logger.Infof("[%s] Recorded the upstream set as unreadable; the next boot or config change records a readable one", tag)
	return nil
}

// upstreamsFromConfig collects the destinations a service config permits, one member
// per distinct URL.
//
// # Where they come from
//
// service.targetUrl is the default destination, and each modelPricing entry may name
// its own — the pair (targetUrl, providerIdentity) that config already validates as
// set-together, and whose whole purpose is fanning one provider out to several
// upstreams. Both are collected, because a model resolving to either means plaintext
// can reach either.
//
// Deduplicated by URL, not by model: several models sharing one vendor are one
// destination. Measured on the 32 mainnet deployments (2026-09-09), that is the common
// case — the largest config has 11 targetUrl entries over 4 distinct URLs.
//
// # Naming
//
// The record's grammar needs a name per member, and config has no field for one, so it
// is derived — and the derivation must not depend on set membership. An ordinal suffix
// would: inserting a URL renumbers the others, and upstreamChanges diffs by name, so
// adding one upstream would report rewrites of the others that never happened, which
// corrupts the one mechanism built to catch a deceptive rewrite.
//
//   - providerIdentity when set. Already required alongside a per-model targetUrl, and
//     it is the same lowercase-alphanumeric-with-hyphens shape the record's identity
//     field takes.
//   - otherwise the URL's hostname. Measured: the 5 deployments with no service-level
//     providerIdentity all target an in-CVM container, and all 5 hostnames
//     (phala-inference-guard, 0gm-sglang, qwavity-sia-vllm, api, vllm) are compose
//     service names that the record's name pattern already admits. So this names the
//     container, which is what a reader is trying to identify anyway.
//   - otherwise refused, with the fix in the message. A hostname that is not a valid
//     name means a dotted public FQDN — an external vendor with no identity — which is
//     a config nobody has today and which providerIdentity exists to describe.
//
// Two distinct URLs deriving one name is refused rather than disambiguated: the writer
// cannot say which is which, so it must not claim it can. Measured zero occurrences —
// in all 32 deployments providerIdentity is already one-to-one with the upstream URL.
func upstreamsFromConfig(svc *config.Service) ([]attest.Upstream, error) {
	type candidate struct {
		url      string
		identity string
	}
	// Ordered, so the refusal below names whichever URL config listed first rather than
	// whichever the map iterated to.
	var order []string
	byURL := map[string]candidate{}

	add := func(rawURL, identity, where string) error {
		if rawURL == "" {
			return nil
		}
		prev, seen := byURL[rawURL]
		if !seen {
			order = append(order, rawURL)
			byURL[rawURL] = candidate{url: rawURL, identity: identity}
			return nil
		}
		// One URL reached under two identities is a config that cannot say which provider
		// serves it, and the record would have to pick one. Refused for the same reason a
		// duplicate name is: the set would be a claim nobody made.
		if prev.identity != identity {
			return fmt.Errorf("%s names %s with providerIdentity %q, but it is already recorded with %q: one destination has one identity", where, rawURL, identity, prev.identity)
		}
		return nil
	}

	if err := add(svc.TargetURL, svc.ProviderIdentity, "service.targetUrl"); err != nil {
		return nil, err
	}
	for i := range svc.ModelPricing {
		e := &svc.ModelPricing[i]
		// The entry's identity falling back to the service-level one. This restates
		// config.Service.effectiveIdentityOf, which is unexported — and it is the real
		// semantics rather than a defensive default: config only WARNS when an entry sets
		// targetUrl without providerIdentity ("will use the service-level value for the
		// other"), so an entry can reach here with an empty one.
		//
		// Measured zero occurrences across the 32 mainnet deployments, so this branch is
		// unexercised in production. Naming it anyway, because the alternative is deriving
		// the name from the host of a per-model vendor URL, which is a dotted FQDN and
		// would be refused — turning a config config itself accepts into an unreadable set.
		identity := e.ProviderIdentity
		if identity == "" {
			identity = svc.ProviderIdentity
		}
		where := fmt.Sprintf("service.modelPricing[%q]", e.Model)
		if err := add(e.TargetURL, identity, where); err != nil {
			return nil, err
		}
	}

	members := make([]attest.Upstream, 0, len(order))
	byName := map[string]string{}
	for _, u := range order {
		c := byURL[u]
		name, err := upstreamName(c.url, c.identity)
		if err != nil {
			return nil, err
		}
		// Redundant for the OUTCOME, and kept for the message. RenderUpstreamSet parses its
		// own output, and parseUpstreamSet refuses a name spelled twice — so deleting these
		// three lines changes nothing a reader sees, and a mutation doing so fails no test.
		//
		// What it changes is what an operator is told. This names both URLs and the field
		// to edit; the reader's refusal says a name appears twice, about a record the
		// operator never wrote. The condition is a config mistake, so the message is the
		// whole value.
		if other, dup := byName[name]; dup {
			return nil, fmt.Errorf("upstreams %s and %s both derive the name %q: give each a distinct service.providerIdentity or modelPricing[].providerIdentity so the record can name them apart", other, c.url, name)
		}
		byName[name] = c.url
		members = append(members, attest.Upstream{Name: name, URL: c.url, Identity: c.identity})
	}
	// Not sorted here. RenderUpstreamSet sorts, and it is the only consumer that looks at
	// anything but the length — a sort whose sole justification was a caller that logs
	// both lists was dead weight, and a mutation removing it failed no test.
	return members, nil
}

// upstreamName derives the record's name for one destination. See upstreamsFromConfig
// for why it may not depend on the rest of the set.
func upstreamName(rawURL, identity string) (string, error) {
	if identity != "" {
		return identity, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("targetUrl %q does not parse, so no name can be derived for it: %w", rawURL, err)
	}
	host := u.Hostname()
	if !attest.ValidUpstreamName(host) {
		return "", fmt.Errorf("targetUrl %q has no providerIdentity and its host %q cannot be a record name (lowercase alphanumeric, dashes and underscores, 63 bytes max): set providerIdentity for this upstream", rawURL, host)
	}
	return host, nil
}
