package main

import (
	"go/ast"
	"reflect"
	"strings"
)

type FieldInfo struct {
	Name string
	JSON string
}

type Data struct {
	TypeName        string
	WritableFields  []string
	ImmutableFields []FieldInfo
	ReadonlyFields  []FieldInfo
}

const trueValue = "true"

func parseAnonymousField(field *ast.Field, allTypeFields map[string][]*ast.Field, data *Data) {
	fieldExpr, ok := field.Type.(*ast.Ident)
	if !ok {
		return
	}

	fields, ok := allTypeFields[fieldExpr.Name]
	if !ok {
		return
	}

	for _, fld := range fields {
		if len(fld.Names) == 0 {
			parseAnonymousField(fld, allTypeFields, data)
			continue
		}

		parseField(fld, data)
	}
}

func parseField(field *ast.Field, data *Data) {
	fieldName := field.Names[0].Name

	if field.Tag == nil {
		return
	}

	fieldTag := reflect.StructTag(strings.ReplaceAll(field.Tag.Value, "`", ""))

	genTag, ok := fieldTag.Lookup("gen")
	if ok && genTag == "-" {
		return
	}

	jsonTag, ok := fieldTag.Lookup("json")
	if !ok || jsonTag == "-" {
		return
	}

	gormTag, ok := fieldTag.Lookup("gorm")
	if ok && gormTag == "-" {
		return
	}

	info := FieldInfo{
		Name: fieldName,
		JSON: fieldTag.Get("json"),
	}

	readOnly, ok := fieldTag.Lookup("readonly")
	if ok && readOnly == trueValue {
		data.ReadonlyFields = append(data.ReadonlyFields, info)
		return
	}

	data.WritableFields = append(data.WritableFields, fieldName)

	immutable, ok := fieldTag.Lookup("immutable")
	if !ok || immutable == "false" {
		return
	}

	data.ImmutableFields = append(data.ImmutableFields, info)
}
