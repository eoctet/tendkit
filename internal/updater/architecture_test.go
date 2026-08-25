package updater

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestRootPackageExportsOnlyUpdaterFacade(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]struct{}{
		"type:Options": {}, "type:RunOptions": {}, "type:Updater": {},
		"func:New": {}, "func:PreflightDownloadAssetCandidates": {}, "method:Updater.Add": {}, "method:Updater.Run": {}, "method:Updater.DownloadAssetCandidates": {},
	}
	var got []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || filepath.Ext(name) == ".go" && len(name) > 8 && name[len(name)-8:] == "_test.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, exported := range exportedDeclarations(file) {
			if _, ok := allowed[exported]; !ok {
				got = append(got, name+":"+exported)
			}
			delete(allowed, exported)
		}
	}
	for missing := range allowed {
		got = append(got, "missing "+missing)
	}
	sort.Strings(got)
	if len(got) > 0 {
		t.Fatalf("root updater exports violate facade boundary: %v", got)
	}
}

func exportedDeclarations(file *ast.File) []string {
	var result []string
	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			for _, spec := range declaration.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					if spec.Name.IsExported() {
						result = append(result, "type:"+spec.Name.Name)
					}
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						if name.IsExported() {
							result = append(result, "value:"+name.Name)
						}
					}
				}
			}
		case *ast.FuncDecl:
			if !declaration.Name.IsExported() {
				continue
			}
			if declaration.Recv == nil {
				result = append(result, "func:"+declaration.Name.Name)
				continue
			}
			if receiverName(declaration) == "Updater" {
				result = append(result, "method:Updater."+declaration.Name.Name)
			}
		}
	}
	return result
}

func receiverName(declaration *ast.FuncDecl) string {
	if declaration.Recv == nil || len(declaration.Recv.List) != 1 {
		return ""
	}
	switch receiver := declaration.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return receiver.Name
	case *ast.StarExpr:
		if named, ok := receiver.X.(*ast.Ident); ok {
			return named.Name
		}
	}
	return ""
}
