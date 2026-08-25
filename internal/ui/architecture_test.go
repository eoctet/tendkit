package ui

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTUIDependencyBoundaries(t *testing.T) {
	if err := filepath.Walk(".", func(path string, info os.FileInfo, walkErr error) error {
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
			if strings.HasPrefix(path, "component"+string(filepath.Separator)) && strings.Contains(value, "github.com/eoctet/tendkit") {
				t.Errorf("component file %s has repository dependency %s", path, value)
			}
			for _, forbidden := range []string{"/internal/config", "/internal/scanner", "/internal/service", "/internal/updater"} {
				if strings.Contains(value, forbidden) {
					t.Errorf("UI file %s bypasses TUIActions with dependency %s", path, value)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
