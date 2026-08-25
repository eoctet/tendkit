package config

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPublicPackageDependencyBoundaries(t *testing.T) {
	allowed := map[string]map[string]bool{
		"metadata":   {"runtime": true, "version": true},
		"downloader": {"errors": true, "runtime": true},
		"logger":     {"i18n": true, "runtime": true},
		"i18n":       {"errors": true, "version": true},
		"runtime":    {"errors": true},
	}
	root := filepath.Join("..", "..", "pkg")
	if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		owner := filepath.Base(filepath.Dir(path))
		for _, imported := range file.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if strings.Contains(value, "/internal/") {
				t.Errorf("public package %s imports internal package %s", owner, value)
			}
			const prefix = "github.com/eoctet/tendkit/pkg/"
			if strings.HasPrefix(value, prefix) {
				dependency := strings.TrimPrefix(value, prefix)
				if !allowed[owner][dependency] {
					t.Errorf("unexpected public dependency %s -> %s", owner, dependency)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLowerLayersDoNotImportUI(t *testing.T) {
	for _, root := range []string{".", "../scanner", "../updater"} {
		if err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return walkErr
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				value, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				if value == "github.com/eoctet/tendkit/internal/ui" || strings.HasPrefix(value, "github.com/eoctet/tendkit/internal/ui/") {
					t.Errorf("lower-layer file %s imports UI: %s", path, value)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
