package version

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestVersionPackageWasFullyMigratedToPkg(t *testing.T) {
	if _, err := os.Stat(filepath.Join("..", "..", "internal", "utils")); !os.IsNotExist(err) {
		t.Fatalf("legacy internal/utils directory still exists: %v", err)
	}
	err := filepath.Walk(filepath.Join("..", ".."), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || filepath.Ext(path) != ".go" {
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
			if strings.HasSuffix(value, "/internal/version") || strings.HasSuffix(value, "/internal/utils") {
				return &legacyVersionImportError{path: path}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type legacyVersionImportError struct{ path string }

func (err *legacyVersionImportError) Error() string {
	return "legacy version import remains in " + err.path
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"1.2.0", "1.1.9", true}, {"1.2", "1.2.0", false}, {"v4.0.1", "3.9.9", true},
		{"1.2.0-beta2", "1.2.0-beta1", true}, {"1.2.0", "1.2.0-rc1", true},
		{"1.2.0-rc1", "1.2.0-beta9", true}, {"1.2.0-beta1", "1.2.0", false},
		{Available, "1.2.0", true},
	}
	for _, tc := range cases {
		if got := IsNewer(tc.latest, tc.current); got != tc.want {
			t.Errorf("IsNewer(%q, %q)=%v", tc.latest, tc.current, got)
		}
	}
}

func TestAtLeastDoesNotTreatDifferentUnparseableVersionsAsOrdered(t *testing.T) {
	if AtLeast("nightly-b", "nightly-a") {
		t.Fatal("unparseable unequal versions must not satisfy update verification")
	}
}

func TestVersionEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		left, right string
		comparison  int
		comparable  bool
	}{
		{"single segment", "2", "1", 0, false},
		{"unparseable", "nightly-a", "nightly-b", 0, false},
		{"release prefix", "release-2.0.0", "1.9.9", 1, true},
		{"prerelease rank", "2.0.0-rc1", "2.0.0-beta9", 1, true},
		{"build metadata", "2.0.0+darwin", "2.0.0", 0, true},
		{"downgrade", "1.9.9", "2.0.0", -1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			comparison, comparable := Compare(test.left, test.right)
			if comparison != test.comparison || comparable != test.comparable {
				t.Fatalf("Compare(%q, %q) = (%d, %v)", test.left, test.right, comparison, comparable)
			}
		})
	}
	if _, err := Extract("no version here"); !errors.Is(err, ErrExtractFailed) {
		t.Fatalf("extract error = %v", err)
	}
}
