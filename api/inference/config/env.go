package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/0glabs/0g-serving-broker/common/config"
	"github.com/knadh/koanf/v2"
)

// envPrefix is the canonical prefix every broker-aware env var uses. The
// reflection walker produces one entry per leaf field whose name starts with
// this prefix; everything else is ignored (with a warn line) or handled by
// the explicit legacy-alias list inside applyEnvOverrides.
const envPrefix = "BROKER_"

// envEntry describes one mapping from an env var name to a koanf path plus
// the metadata applyEnvOverrides needs to decode the raw string value into
// the right shape before it lands in koanf.
type envEntry struct {
	// path is the koanf path the env var writes to (e.g. "service.inputPrice").
	path string
	// kind tells applyEnvOverrides how to decode the raw env string.
	kind envKind
}

type envKind int

const (
	envScalar      envKind = iota // string / int / bool / float (let mapstructure handle the cast)
	envDuration                   // duration string, "1h" / "30s"
	envStringSlice                // comma-separated, e.g. "0xaaa,0xbbb"
	envJSON                       // []struct, map[string]X, *struct — pass through json.Unmarshal
)

// camelSplitRE matches the two CamelCase boundary kinds we need to honour
// when turning a yaml-tag name into UPPER_SNAKE_CASE:
//   1. lowercase|digit followed by uppercase  ("inputPrice" → "input_Price")
//   2. run-of-uppercase followed by uppercase+lowercase
//      ("CoinGeckoAPIKey" → "Coin_Gecko_API_Key")
var camelSplitRE = regexp.MustCompile(`([a-z0-9])([A-Z])|([A-Z]+)([A-Z][a-z])`)

// toEnvName converts a dotted koanf path (yaml-tag-style) into the
// UPPER_SNAKE_CASE env-var suffix used after envPrefix.
//
//   service.inputPrice          → SERVICE_INPUT_PRICE
//   priceFeed.coinGeckoApiKey   → PRICE_FEED_COIN_GECKO_API_KEY
//   zk.url                      → ZK_URL
func toEnvName(path string) string {
	parts := strings.Split(path, ".")
	for i, p := range parts {
		parts[i] = camelSplitRE.ReplaceAllString(p, "${1}${3}_${2}${4}")
	}
	return strings.ToUpper(strings.Join(parts, "_"))
}

// buildEnvRegistry walks target (a non-nil pointer to a Config-shaped struct)
// and produces one envEntry per non-deprecated, non-runtime leaf field. The
// returned map is keyed by full env name (with envPrefix).
//
// Skipped fields:
//   - yaml:"-"                    — runtime-only, not user-configurable via yaml
//   - deprecated:"true"           — kept in struct for yaml migration, env exposure
//                                   would re-introduce the surface we just retired
//   - Unexported / un-settable    — Go visibility says so
//   - PrivateKeyStore             — runtime, no env entry (the walker stops at the
//                                   *PrivateKeyStore type because it has no yaml tag)
//
// Sub-struct fields recurse with a dotted path. map[string]X, []struct, and
// *struct (when the user supplies the whole subtree as one JSON env value)
// land as envJSON leaves and decode via json.Unmarshal.
func buildEnvRegistry(target any) (map[string]envEntry, error) {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return nil, fmt.Errorf("buildEnvRegistry: target must be non-nil pointer, got %T", target)
	}
	out := map[string]envEntry{}
	if err := walkEnv(v.Elem(), "", out); err != nil {
		return nil, err
	}
	return out, nil
}

func walkEnv(v reflect.Value, path string, out map[string]envEntry) error {
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		fv := v.Field(i)
		ft := t.Field(i)
		if !ft.IsExported() {
			continue
		}
		if _, dep := ft.Tag.Lookup("deprecated"); dep {
			continue
		}
		yamlTag := ft.Tag.Get("yaml")
		yamlName := strings.SplitN(yamlTag, ",", 2)[0]
		if yamlName == "-" || yamlName == "" {
			// `yaml:"-"` is runtime-only; missing tag means the field is
			// intentionally not yaml-bound (PrivateKeyStore et al.).
			continue
		}
		fieldPath := joinPath(path, yamlName)

		// Decide whether to recurse or emit a leaf entry.
		kind, recurse := classifyEnvField(ft.Type)
		if recurse {
			if fv.Kind() == reflect.Ptr {
				// Ensure we walk into the pointee type even when the
				// runtime value is nil — the type's nested yaml tags are
				// what we need for the registry, the value doesn't
				// matter here.
				if fv.IsNil() {
					if err := walkEnvType(ft.Type.Elem(), fieldPath, out); err != nil {
						return err
					}
					continue
				}
				if err := walkEnv(fv.Elem(), fieldPath, out); err != nil {
					return err
				}
				continue
			}
			if err := walkEnv(fv, fieldPath, out); err != nil {
				return err
			}
			continue
		}
		envName := envPrefix + toEnvName(fieldPath)
		if existing, ok := out[envName]; ok {
			return fmt.Errorf("duplicate env name %s for paths %q and %q", envName, existing.path, fieldPath)
		}
		out[envName] = envEntry{path: fieldPath, kind: kind}
	}
	return nil
}

// walkEnvType is the type-only variant used when we need to recurse into a nil
// *struct pointer at registry-build time (production code never has a fully
// nil Config, but tests and the help command want a complete registry).
func walkEnvType(t reflect.Type, path string, out map[string]envEntry) error {
	if t.Kind() != reflect.Struct {
		return nil
	}
	zero := reflect.New(t).Elem()
	return walkEnv(zero, path, out)
}

// classifyEnvField returns the env decoding kind for ft.Type, and whether the
// walker should recurse into the field instead of emitting a leaf entry.
//
// Recurse:
//   - struct (named or anonymous), or *struct: recurse so nested leaves get
//     individual env names.
//
// Leaf:
//   - time.Duration: special, decoded by mapstructure's hook from string.
//   - []string: comma-separated env value.
//   - map[string]X, []struct, []map: single JSON-encoded env value (these
//     are types the reflection walker can't deconstruct into individual
//     leaves, so the operator-friendly fallback is one big JSON string).
//   - everything else (string, int, bool, etc): scalar.
func classifyEnvField(t reflect.Type) (envKind, bool) {
	durType := reflect.TypeOf(time.Duration(0))
	if t == durType {
		return envDuration, false
	}
	switch t.Kind() {
	case reflect.Struct:
		return envScalar, true
	case reflect.Ptr:
		if t.Elem().Kind() == reflect.Struct && t.Elem() != durType {
			// Note: *time.Duration would hit this branch, but the config
			// schema doesn't use it — Durations are passed by value.
			// Recurse so e.g. *ModelInfo's children get env names too.
			return envScalar, true
		}
		return envJSON, false
	case reflect.Slice:
		elem := t.Elem()
		if elem.Kind() == reflect.String {
			return envStringSlice, false
		}
		// []int / []float / []struct / []map all fall here.
		return envJSON, false
	case reflect.Map:
		return envJSON, false
	default:
		return envScalar, false
	}
}

// applyEnvOverrides reads os.Environ(), looks each BROKER_* var up in the
// registry built from cfg, and writes the (decoded) value into k. Legacy
// (pre-#452) env names like LORA_ECIES_PRIVATE_KEY are handled here too so
// existing TEE deployments keep booting.
//
// Unknown BROKER_* vars are logged as warnings — invaluable when an operator
// has a typo (no error, but at least the line in stderr points at the typo).
func applyEnvOverrides(k *koanf.Koanf, cfg *Config) error {
	registry, err := buildEnvRegistry(cfg)
	if err != nil {
		return err
	}
	// Track the canonical secret env name so the registry-driven loop
	// reports it as "known" instead of warning the operator about a typo.
	// The actual write lands directly on cfg.LoRA.EciesPrivateKey in
	// applyEnvSecrets after unmarshal — it can't go through koanf because
	// the field has yaml:"-" and mapstructure (ErrorUnused) would reject
	// the unknown key.
	registry["BROKER_LORA_ECIES_PRIVATE_KEY"] = envEntry{path: "", kind: envScalar}

	// Build a secret-path lookup once so the audit log can mask values
	// without re-walking the struct for each env var.
	secretPaths := secretPathSet()

	// Sort the env iteration so the startup audit log is deterministic.
	// os.Environ order varies between runs / TEE attestation snapshots;
	// stable output makes diffs across deployments useful.
	envs := os.Environ()
	sort.Strings(envs)

	applied := []string{}
	for _, kv := range envs {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		name, rawVal := kv[:idx], kv[idx+1:]
		if !strings.HasPrefix(name, envPrefix) {
			continue
		}
		entry, ok := registry[name]
		if !ok {
			log.Printf("[CONFIG-ENV] %q has no matching config field, ignoring", name)
			continue
		}
		if entry.path == "" {
			// Secret field handled out-of-band by applyEnvSecrets; still
			// record the name in the audit log below.
			applied = append(applied, auditEntry(name, rawVal, true))
			continue
		}
		decoded, err := decodeEnvValue(rawVal, entry)
		if err != nil {
			return fmt.Errorf("env %s: %w", name, err)
		}
		if err := k.Set(entry.path, decoded); err != nil {
			return fmt.Errorf("env %s -> %s: %w", name, entry.path, err)
		}
		applied = append(applied, auditEntry(name, rawVal, secretPaths[entry.path]))
	}
	if len(applied) > 0 {
		log.Printf("[CONFIG-ENV] applied %d env override(s): %s", len(applied), strings.Join(applied, ", "))
	}
	return nil
}

// auditEntry formats one env override for the startup audit log. Secret
// values are masked to "***" so the log line is safe even if shipped to
// log aggregation. Non-secret values are truncated for readability.
func auditEntry(name, value string, secret bool) string {
	if secret {
		return name + "=***"
	}
	if len(value) > 40 {
		return name + "=" + value[:37] + "..."
	}
	return name + "=" + value
}

// secretPathSet returns every koanf path whose field carries secret:"true".
// Built lazily on first call via WalkConfigFields (which already walks the
// schema for doc generation).
func secretPathSet() map[string]bool {
	out := map[string]bool{}
	for _, d := range WalkConfigFields() {
		if d.Secret {
			out[d.Path] = true
		}
	}
	return out
}

// applyEnvSecrets fills in fields that carry yaml:"-" (runtime-only secrets)
// from their env vars. Called after the main unmarshal so the writes don't
// clash with mapstructure's ErrorUnused check.
//
// Currently this only handles LoRA.EciesPrivateKey, with the legacy
// LORA_ECIES_PRIVATE_KEY (no prefix) honoured for compat + a deprecation
// warn pointing at BROKER_LORA_ECIES_PRIVATE_KEY.
func applyEnvSecrets(cfg *Config) {
	const legacy = "LORA_ECIES_PRIVATE_KEY"
	const canonical = "BROKER_LORA_ECIES_PRIVATE_KEY"
	if v, ok := os.LookupEnv(canonical); ok && v != "" {
		cfg.LoRA.EciesPrivateKey = v
		if _, legacySet := os.LookupEnv(legacy); legacySet {
			log.Printf("[CONFIG-DEPRECATED] %s and %s are both set; %s (deprecated) is ignored", legacy, canonical, legacy)
		}
		return
	}
	if v := os.Getenv(legacy); v != "" {
		cfg.LoRA.EciesPrivateKey = v
		log.Printf("[CONFIG-DEPRECATED] %s is deprecated, will be removed after %s, use %s instead", legacy, config.DeprecationRemovalDate, canonical)
	}
}

func decodeEnvValue(raw string, entry envEntry) (any, error) {
	switch entry.kind {
	case envScalar, envDuration:
		// mapstructure will coerce string → int/bool/duration via the
		// decode hooks already wired into unmarshalStrict.
		return raw, nil
	case envStringSlice:
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts, nil
	case envJSON:
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
		return v, nil
	}
	return nil, fmt.Errorf("unhandled env kind %d", entry.kind)
}
