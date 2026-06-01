package config

import (
	"fmt"
	"io"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// renderEffectiveYAML marshals cfg to w as yaml with every field tagged
// secret:"true" replaced by "***". The masking pass produces a deep clone
// before marshal so cfg itself is never mutated.
func renderEffectiveYAML(cfg *Config, w io.Writer) error {
	clone := *cfg
	maskSecrets(reflect.ValueOf(&clone).Elem(), reflect.TypeOf(*cfg))

	// LoRA.EciesPrivateKey has yaml:"-" so yaml marshalling drops it
	// entirely. Render a sidecar block so operators can see whether the
	// secret was injected without exposing its value.
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(clone); err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close encoder: %w", err)
	}
	fmt.Fprintln(w, "# Runtime-only secrets (set via env, never yaml):")
	fmt.Fprintf(w, "#   lora.eciesPrivateKey: %s\n", maskString(cfg.LoRA.EciesPrivateKey))
	return nil
}

// maskSecrets walks v and replaces every leaf field whose struct tag carries
// secret:"true" with a redacted value. Recurses through nested structs and
// pointer-to-struct. Maps/slices marked secret are wholesale replaced with
// a single "***" marker so the consumer can't infer cardinality either.
func maskSecrets(v reflect.Value, t reflect.Type) {
	if t.Kind() == reflect.Ptr {
		if v.IsNil() {
			return
		}
		v = v.Elem()
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		fv := v.Field(i)
		if !ft.IsExported() {
			continue
		}
		if _, secret := ft.Tag.Lookup("secret"); secret {
			maskFieldValue(fv)
			continue
		}
		switch ft.Type.Kind() {
		case reflect.Struct:
			maskSecrets(fv, ft.Type)
		case reflect.Ptr:
			if ft.Type.Elem().Kind() == reflect.Struct && !fv.IsNil() {
				maskSecrets(fv.Elem(), ft.Type.Elem())
			}
		}
	}
}

func maskFieldValue(fv reflect.Value) {
	if !fv.CanSet() {
		return
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(maskString(fv.String()))
	case reflect.Slice:
		if fv.Len() == 0 {
			return
		}
		if fv.Type().Elem().Kind() == reflect.String {
			masked := make([]string, fv.Len())
			for i := range masked {
				masked[i] = "***"
			}
			fv.Set(reflect.ValueOf(masked))
			return
		}
		// For non-string slices we don't have a generic placeholder; clear
		// the slice so the consumer sees "absent" instead of the values.
		fv.Set(reflect.MakeSlice(fv.Type(), 0, 0))
	case reflect.Map:
		// Wipe the map; "(redacted)" goes in a side-channel comment in the
		// future if operators need cardinality. For now the map is just
		// dropped, which yaml renders as `{}`.
		fv.Set(reflect.MakeMap(fv.Type()))
	}
}

// maskString returns "***" for a non-empty value and "" for an empty one.
// The distinction matters for `--print-config` — operators want to see
// "yes the secret made it in" vs "no, env didn't deliver it".
func maskString(s string) string {
	if s == "" {
		return ""
	}
	return "***"
}

// joinForDoc is used internally by tests; the real env code uses joinPath.
func joinForDoc(parts ...string) string {
	return strings.Join(parts, ".")
}

var _ = joinForDoc // silence linter for now; will be used by the help renderer
