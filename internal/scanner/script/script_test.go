package script

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	runtimeutil "github.com/eoctet/tendkit/pkg/runtime"
)

func TestPythonPackageInfoContainsRequiredDiscoveryPaths(t *testing.T) {
	for _, value := range []string{"metadata.packages_distributions", "util.find_spec", "metadata.distribution", "top_level.txt", "dist.files", "dist.locate_file", "site.getusersitepackages", "sys.prefix"} {
		if !strings.Contains(PythonPackageInfo, value) {
			t.Fatalf("missing %q", value)
		}
	}
}

func TestPythonPackageInfoProducesJSONWhenEnabled(t *testing.T) {
	if os.Getenv("TENDKIT_PLATFORM_TESTS") != "1" {
		t.Skip("set TENDKIT_PLATFORM_TESTS=1")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	result, err := (runtimeutil.Runner{}).Run(context.Background(), runtimeutil.QuoteShell(python)+" -c "+runtimeutil.QuoteShell(PythonPackageInfo), nil)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &value); err != nil {
		t.Fatal(err)
	}
}
