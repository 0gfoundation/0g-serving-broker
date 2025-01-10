package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pkg/errors"
)

func parse(searchDir string) (map[string][]*ast.Field, []string, []string, map[string]struct{}, error) {
	objects, err := parsePackageFiles(searchDir)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	allTypeSpec, allTypeFields := parseObjects(objects)

	bind := []string{}
	immutable := []string{}
	requireScanValue := map[string]struct{}{}
	embedded := map[string]struct{}{}

	jsonData, _ := json.Marshal(allTypeFields)
	_ = os.WriteFile("/tmp/type.json", jsonData, 0644)

	for typ, fields := range allTypeFields {
		if _, ok := embedded[typ]; ok {
			continue
		}
		needBind := false
		needImmutable := false
		for _, field := range fields {
			if len(field.Names) == 0 {
				embedded[fmt.Sprintf("%v", field.Type)] = struct{}{}
				continue
			}

			if field.Tag == nil {
				continue
			}

			tag := reflect.StructTag(strings.ReplaceAll(field.Tag.Value, "`", ""))
			if tag.Get("gorm") == "" || tag.Get("gorm") == "-" {
				continue
			}
			needBind = true

			if tag.Get("immutable") == "true" || tag.Get("readonly") == "true" {
				needImmutable = true
			}

			parseScanValue(allTypeSpec, field, requireScanValue)
		}
		if needBind {
			bind = append(bind, typ)
		}
		if needImmutable {
			immutable = append(immutable, typ)
		}
	}

	filterNested(bind, embedded)
	filterNested(immutable, embedded)

	return allTypeFields, bind, immutable, requireScanValue, nil
}

func parsePackageFiles(searchDir string) (map[string]*ast.Object, error) {
	objects := map[string]*ast.Object{}
	fileSet := token.NewFileSet()

	err := filepath.Walk(searchDir, func(path string, f os.FileInfo, _ error) error {
		if f.IsDir() {
			return nil
		}

		if strings.HasPrefix(strings.ToLower(path), "gen/") {
			return nil
		}

		if strings.HasSuffix(strings.ToLower(path), "_test.go") || filepath.Ext(path) != ".go" {
			return nil
		}

		astFile, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			return errors.Wrapf(err, "failed to parse file %s", path)
		}

		for k, obj := range astFile.Scope.Objects {
			objects[k] = obj
		}
		return nil
	})
	return objects, err
}

func parseObjects(objects map[string]*ast.Object) (map[string]struct{}, map[string][]*ast.Field) {
	allTypeSpec := map[string]struct{}{}
	allTypeFields := map[string][]*ast.Field{}
	for key, obj := range objects {
		dec, ok := obj.Decl.(*ast.TypeSpec)
		if !ok {
			continue
		}

		switch expr := dec.Type.(type) {
		// JobStatus, MetaSchemaType
		case *ast.Ident:
			break

		case *ast.StructType:
			allTypeFields[key] = expr.Fields.List
			allTypeSpec[key] = struct{}{}

		// *ast.ArrayType, *ast.MapType,
		// ResourceSlice, StringSlice, MetaSchemaBody, MetaViews, MetaData
		default:
			allTypeSpec[key] = struct{}{}
		}
	}
	return allTypeSpec, allTypeFields
}

func parseScanValue(allTypeSpec map[string]struct{}, field *ast.Field, requireScanValue map[string]struct{}) {
	switch fieldExpr := field.Type.(type) {
	// type Foo Baz
	case *ast.Ident:
		if _, ok := allTypeSpec[fieldExpr.Name]; ok {
			requireScanValue[fieldExpr.Name] = struct{}{}
		}

	// type Foo *Baz
	case *ast.StarExpr:
		typName := fmt.Sprintf("%v", fieldExpr.X)
		if _, ok := allTypeSpec[typName]; ok {
			requireScanValue[typName] = struct{}{}
		}

	// type Foo []Baz
	case *ast.ArrayType:
		requireScanValue[fmt.Sprintf("%v", fieldExpr.Elt)] = struct{}{}

	// type Foo map[string]Bar
	case *ast.MapType:
		requireScanValue[fmt.Sprintf("%v", fieldExpr.Value)] = struct{}{}
	}
}

func filterNested(s []string, f map[string]struct{}) []string {
	ret := []string{}
	for _, k := range s {
		if _, ok := f[k]; !ok {
			ret = append(ret, k)
		}
	}
	return ret
}
