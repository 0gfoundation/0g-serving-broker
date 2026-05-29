package config

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// DeprecationRemovalDate is the deadline after which all deprecated config
// fallbacks introduced by issue #507 should be removed. Cleanup PR should land
// on or after this date.
const DeprecationRemovalDate = "2026-07-23"

// WarnDeprecated emits a stderr warning for a deprecated yaml key. The warning
// is intentionally written via the stdlib log package because loadConfig runs
// before the project's structured logger is initialized.
func WarnDeprecated(oldKey, newKey string) {
	log.Printf("[CONFIG-DEPRECATED] %q is deprecated, will be removed after %s, use %q instead",
		oldKey, DeprecationRemovalDate, newKey)
}

// WarnDeprecatedBothSet warns when a user provides both the deprecated key and
// its replacement. The new key takes precedence.
func WarnDeprecatedBothSet(oldKey, newKey string) {
	log.Printf("[CONFIG-DEPRECATED] %q and %q are both set; %q (deprecated) is ignored",
		oldKey, newKey, oldKey)
}

// RawYAMLKeys parses yaml into a generic map so callers can detect which keys
// the user actually wrote (vs. which were left at their struct defaults). Used
// by deprecated-field migration logic where "the old key was present" is the
// trigger.
//
// Returns an empty map (not nil) if data is empty or unparseable, so callers
// can dereference without nil checks.
func RawYAMLKeys(data []byte) map[string]interface{} {
	out := map[string]interface{}{}
	if len(data) == 0 {
		return out
	}
	// Best-effort: any error here will be surfaced again by the strict
	// unmarshal that runs after this, so we don't propagate it.
	_ = yaml.Unmarshal(data, &out)
	return out
}

// RawHasKey reports whether the nested key path is present in raw. The path is
// walked element by element; intermediate non-map values stop the walk and
// return false.
//
// yaml.v2 decodes mappings into map[interface{}]interface{}, so this helper
// handles both that shape and the top-level map[string]interface{}.
func RawHasKey(raw map[string]interface{}, path ...string) bool {
	if len(path) == 0 || raw == nil {
		return false
	}
	var current interface{} = raw
	for _, key := range path {
		switch m := current.(type) {
		case map[string]interface{}:
			next, ok := m[key]
			if !ok {
				return false
			}
			current = next
		case map[interface{}]interface{}:
			next, ok := m[key]
			if !ok {
				return false
			}
			current = next
		default:
			return false
		}
	}
	return true
}

// RawGet walks the same shape as RawHasKey and returns the value at the leaf
// path along with a presence flag. Mirrors RawHasKey for symmetry; callers
// that need both presence and value use RawGet to avoid two walks.
func RawGet(raw map[string]interface{}, path ...string) (interface{}, bool) {
	if len(path) == 0 || raw == nil {
		return nil, false
	}
	var current interface{} = raw
	for _, key := range path {
		switch m := current.(type) {
		case map[string]interface{}:
			next, ok := m[key]
			if !ok {
				return nil, false
			}
			current = next
		case map[interface{}]interface{}:
			next, ok := m[key]
			if !ok {
				return nil, false
			}
			current = next
		default:
			return nil, false
		}
	}
	return current, true
}

// MigrateIntegerSecondsDuration is the in-place migration helper for fields
// that historically held an integer count of `unit` (e.g. seconds, minutes,
// hours) and now hold a time.Duration. The yaml key is unchanged, so the
// raw-yaml value's type tells us whether the user wrote the legacy integer
// form or the new string form.
//
// If the raw value is a number, target is overwritten with value*unit and a
// deprecation warning is emitted. Strings and missing keys are no-ops — the
// new-style Duration value parsed by yaml.UnmarshalStrict is kept as-is.
func MigrateIntegerSecondsDuration(raw map[string]interface{}, target *time.Duration, unit time.Duration, path ...string) {
	v, ok := RawGet(raw, path...)
	if !ok {
		return
	}
	var n int64
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int64:
		n = x
	case uint:
		n = int64(x)
	case uint64:
		n = int64(x)
	case float64:
		// yaml.v2 sometimes decodes numbers as float64 for large values.
		n = int64(x)
	default:
		return
	}
	*target = time.Duration(n) * unit
	dotted := strings.Join(path, ".")
	log.Printf("[CONFIG-DEPRECATED] %q is an integer count of %s (legacy form); will be removed after %s, use a duration string like \"30s\" / \"1h\" instead",
		dotted, unitName(unit), DeprecationRemovalDate)
}

func unitName(d time.Duration) string {
	switch d {
	case time.Nanosecond:
		return "nanoseconds"
	case time.Microsecond:
		return "microseconds"
	case time.Millisecond:
		return "milliseconds"
	case time.Second:
		return "seconds"
	case time.Minute:
		return "minutes"
	case time.Hour:
		return "hours"
	default:
		return d.String()
	}
}

// MigrateDurationFromInt is the migration helper for fields whose deprecated
// form was an integer-with-unit-suffix yaml key (e.g. "offloadAfterMinutes")
// and whose new form is a time.Duration under a renamed key (e.g.
// "offloadAfter"). The behaviour is:
//
//   - if the user wrote only the new key (or neither), no-op
//   - if the user wrote only the deprecated key, copy oldValue*unit into
//     target and emit a deprecation warning
//   - if both are present, the new key wins; a "both set" warning is emitted
//     so operators can spot the inconsistency
func MigrateDurationFromInt(raw map[string]interface{}, oldPath, newPath []string, target *time.Duration, oldValue int64, unit time.Duration) {
	if !RawHasKey(raw, oldPath...) {
		return
	}
	oldDotted := strings.Join(oldPath, ".")
	newDotted := strings.Join(newPath, ".")
	if RawHasKey(raw, newPath...) {
		WarnDeprecatedBothSet(oldDotted, newDotted)
		return
	}
	WarnDeprecated(oldDotted, newDotted)
	*target = time.Duration(oldValue) * unit
}

// MigrateStringRename is the migration helper for keys that were simply
// renamed (e.g. "database.provider" → "database.dsn"). New key wins when both
// are present.
func MigrateStringRename(raw map[string]interface{}, oldPath, newPath []string, target *string, oldValue string) {
	if !RawHasKey(raw, oldPath...) {
		return
	}
	oldDotted := strings.Join(oldPath, ".")
	newDotted := strings.Join(newPath, ".")
	if RawHasKey(raw, newPath...) {
		WarnDeprecatedBothSet(oldDotted, newDotted)
		return
	}
	WarnDeprecated(oldDotted, newDotted)
	*target = oldValue
}

// PickLegacyNetwork chooses a single entry out of a legacy Networks map. For
// 1-entry maps the only entry wins. For multi-entry maps (pre-#507 dev
// configs that shipped with both ethereumHardhat and ethereum0g side by side)
// the pre-#507 NETWORK env var is honored so existing CI/dev workflows keep
// working during the deprecation window: NETWORK=hardhat selects the
// ethereumHardhat entry, anything else selects ethereum0g. Both the NETWORK
// coupling and this whole helper go away at the cleanup deadline.
func PickLegacyNetwork(networks Networks) (*NetworkConfig, error) { //nolint:staticcheck // intentional reference to deprecated Networks for the #507 fallback window
	if len(networks) == 0 {
		return nil, fmt.Errorf("invalid config: 'networks' is set but empty")
	}
	if len(networks) == 1 {
		for _, nc := range networks {
			if nc == nil {
				return nil, fmt.Errorf("invalid config: 'networks' entry has no value; either delete it or fill in url/chainID/privateKeys")
			}
			return nc, nil
		}
	}
	wanted := "ethereum0g"
	if os.Getenv("NETWORK") == "hardhat" {
		wanted = "ethereumHardhat"
	}
	if nc, ok := networks[wanted]; ok && nc != nil {
		return nc, nil
	}
	return nil, fmt.Errorf("invalid config: 'networks' contains %d entries; flatten to a single 'network' block (multi-network was never used in production)", len(networks))
}

// ReadConfigFile is a convenience wrapper that returns the config bytes and an
// "is missing" flag, so callers can keep their existing "no file = use
// defaults" semantics without duplicating os.IsNotExist boilerplate.
func ReadConfigFile(path string) (data []byte, missing bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("read config %q: %w", path, err)
	}
	return b, false, nil
}
