package main

import (
	"fmt"
	"go/ast"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/template"

	"github.com/0glabs/0g-serving-broker/common/log"
)

func main() {
	all, bind, immutable, scanValue, err := parse("./")
	if err != nil {
		logger := log.GetLogger(log.Settings{})
		logger.Fatal(err)
	}

	genModel(all, bind, scanValue)
	genValidate(all, immutable)
}

func genModel(all map[string][]*ast.Field, bind []string, scanValue map[string]struct{}) {
	f, _ := os.Create("model_generated.go")
	defer f.Close()

	if len(scanValue) == 0 {
		fmt.Fprintf(f, modelTplHeader, filepath.Base(os.Args[0]))
	} else {
		fmt.Fprintf(f, modelTplHeaderWithScanValue, filepath.Base(os.Args[0]))
	}

	sort.Strings(bind)
	for _, key := range bind {
		gen(modelTpl, f, key, all)
	}

	if len(scanValue) == 0 {
		return
	}
	// generate scan & value funcs
	fmt.Fprint(f, scanValueComment)
	keys := []string{}
	for k := range scanValue {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, typ := range keys {
		fmt.Fprintf(f, scanValueTplText, typ)
	}
}

func genValidate(all map[string][]*ast.Field, immutable []string) {
	f, _ := os.Create("validate_generated.go")
	defer f.Close()
	fmt.Fprintf(f, validateUpdateTplHeader, filepath.Base(os.Args[0]))

	sort.Strings(immutable)
	for _, key := range immutable {
		gen(validateUpdateTpl, f, key, all)
	}
}

func gen(tpl *template.Template, w io.Writer, typName string, allFields map[string][]*ast.Field) {
	data := Data{
		TypeName: typName,
	}

	for _, field := range allFields[typName] {
		if len(field.Names) == 0 {
			parseAnonymousField(field, allFields, &data)
			continue
		}

		parseField(field, &data)
	}
	_ = tpl.Execute(w, data)
}
