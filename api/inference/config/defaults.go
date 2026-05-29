package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// applyDefaults walks target and applies `default:"..."` struct tags to any
// field that is currently the zero value for its type. Pointer fields whose
// target type contains nested default tags are auto-instantiated so the
// nested defaults can be filled in.
//
// Supported leaf types: string, int* / uint*, bool, time.Duration, []string
// (comma-separated). Adding a default tag with an unsupported type returns
// a load-time error so the schema and the walker stay in sync.
func applyDefaults(target any) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return fmt.Errorf("applyDefaults: target must be a non-nil pointer, got %T", target)
	}
	return walkDefaults(v.Elem(), "")
}

func walkDefaults(v reflect.Value, path string) error {
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		fv := v.Field(i)
		ft := t.Field(i)
		if !ft.IsExported() || !fv.CanSet() {
			continue
		}
		fieldPath := joinPath(path, ft.Name)
		if def, ok := ft.Tag.Lookup("default"); ok {
			if fv.IsZero() {
				if err := setFromString(fv, def); err != nil {
					return fmt.Errorf("apply default for %s: %w", fieldPath, err)
				}
			}
		}
		switch fv.Kind() {
		case reflect.Struct:
			if err := walkDefaults(fv, fieldPath); err != nil {
				return err
			}
		case reflect.Ptr:
			elemType := ft.Type.Elem()
			if elemType.Kind() != reflect.Struct {
				continue
			}
			if fv.IsNil() {
				if !typeHasDefaults(elemType) {
					continue
				}
				fv.Set(reflect.New(elemType))
			}
			if err := walkDefaults(fv.Elem(), fieldPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeHasDefaults(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if _, ok := f.Tag.Lookup("default"); ok {
			return true
		}
		switch f.Type.Kind() {
		case reflect.Struct:
			if typeHasDefaults(f.Type) {
				return true
			}
		case reflect.Ptr:
			if f.Type.Elem().Kind() == reflect.Struct && typeHasDefaults(f.Type.Elem()) {
				return true
			}
		}
	}
	return false
}

func setFromString(v reflect.Value, raw string) error {
	if v.Type() == reflect.TypeOf(time.Duration(0)) {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("duration %q: %w", raw, err)
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("int %q: %w", raw, err)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("uint %q: %w", raw, err)
		}
		v.SetUint(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("bool %q: %w", raw, err)
		}
		v.SetBool(b)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice element type %s", v.Type().Elem().Kind())
		}
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		v.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("unsupported kind %s", v.Kind())
	}
	return nil
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}
