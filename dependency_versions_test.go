package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestSecurityDependencyVersions(t *testing.T) {
	required := map[string]string{
		"github.com/moby/moby/api":    "v1.55.0",
		"github.com/moby/moby/client": "v0.5.0",
		"github.com/ollama/ollama":    "v0.32.4",
		"google.golang.org/grpc":      "v1.82.1",
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	path := filepath.Join(filepath.Dir(file), "go.mod")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	mod, err := modfile.Parse(path, raw, nil)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}

	versions := make(map[string]string, len(required))
	for _, requirement := range mod.Require {
		if _, tracked := required[requirement.Mod.Path]; tracked {
			versions[requirement.Mod.Path] = requirement.Mod.Version
		}
	}
	for module, want := range required {
		if got := versions[module]; got != want {
			t.Errorf("%s version = %q, want %q", module, got, want)
		}
	}

	for _, requirement := range mod.Require {
		if requirement.Mod.Path == "github.com/docker/docker" && !requirement.Indirect {
			t.Error("github.com/docker/docker must not remain a direct requirement")
		}
	}

	for _, replacement := range mod.Replace {
		_, tracked := required[replacement.Old.Path]
		if tracked || replacement.Old.Path == "github.com/docker/docker" {
			t.Errorf("%s must not have a replace directive", replacement.Old.Path)
		}
	}
}
