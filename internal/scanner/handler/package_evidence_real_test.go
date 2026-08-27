package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestPackageEvidenceRealHandlers(t *testing.T) {
	if os.Getenv("TENDKIT_REAL_PACKAGE_EVIDENCE") != "1" {
		t.Skip("set TENDKIT_REAL_PACKAGE_EVIDENCE=1 to scan local package-manager inventories")
	}
	runner := runtimeutil.Runner{IdleTimeout: 2 * time.Minute}
	tests := []struct {
		name          string
		source        string
		handler       Handler
		wantLibraries bool
	}{
		{name: "go", source: "go", handler: NewGo(runner)},
		{name: "uv", source: "uv", handler: NewUV(runner)},
		{name: "node", source: "node", handler: NewNode(runner), wantLibraries: true},
		{name: "python", source: "python", handler: NewPython(runner), wantLibraries: true},
		{name: "ruby", source: "ruby", handler: NewRuby(runner), wantLibraries: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.handler.Scan(context.Background(), Request{})
			if result.Complete != (result.Err == nil) {
				t.Fatalf("real %s inventory completeness mismatch: complete=%t err=%v", test.name, result.Complete, result.Err)
			}
			if result.Err != nil {
				var incomplete *PackageInventoryIncompleteError
				if !errors.As(result.Err, &incomplete) {
					t.Fatalf("real %s inventory failed unexpectedly: %v", test.name, result.Err)
				}
				t.Logf("real %s inventory correctly failed closed: %v", test.name, result.Err)
			}
			evidenceCount := 0
			libraryCount := 0
			for _, candidate := range result.Candidates {
				if candidate.Evidence == nil {
					libraryCount++
					continue
				}
				evidenceCount++
				evidence := candidate.Evidence
				if evidence.Source != test.source || evidence.Package != candidate.Application.Package {
					t.Fatalf("real %s evidence owner mismatch: app=%#v evidence=%#v", test.name, candidate.Application, evidence)
				}
				if evidence.InstallRoot == "" || !filepath.IsAbs(evidence.InstallRoot) {
					t.Fatalf("real %s install root is not absolute: %#v", test.name, evidence)
				}
				if len(evidence.ExecutablePaths) == 0 || !slices.IsSorted(evidence.ExecutablePaths) {
					t.Fatalf("real %s executable paths are empty or unstable: %#v", test.name, evidence.ExecutablePaths)
				}
				seen := map[string]bool{}
				for _, path := range evidence.ExecutablePaths {
					if !filepath.IsAbs(path) || seen[path] {
						t.Fatalf("real %s executable path is relative or duplicated: %q", test.name, path)
					}
					seen[path] = true
					info, err := os.Stat(path)
					if err != nil || !validEvidenceFile(info) {
						t.Fatalf("real %s executable path is not a valid executable: path=%q err=%v info=%v", test.name, path, err, info)
					}
				}
			}
			if evidenceCount == 0 && result.Complete {
				t.Fatalf("real %s inventory produced no executable ownership evidence", test.name)
			}
			if test.wantLibraries && libraryCount == 0 {
				t.Fatalf("real %s inventory did not preserve any package without executable evidence", test.name)
			}
			t.Logf("real %s inventory: complete=%t candidates=%d evidence=%d libraries=%d", test.name, result.Complete, len(result.Candidates), evidenceCount, libraryCount)
		})
	}
}
