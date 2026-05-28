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
	log.Printf("[CONFIG-DEPRECATED] %q is an integer (legacy %s); will be removed after %s, use a duration string e.g. \"30s\" / \"1h\"",
		dotted, unit, DeprecationRemovalDate)
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
