package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type actionWorkflow struct {
	Jobs map[string]actionWorkflowJob `yaml:"jobs"`
}

type actionWorkflowJob struct {
	Steps []actionWorkflowStep `yaml:"steps"`
}

type actionWorkflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
}

type actionGoReleaserConfig struct {
	Builds []struct {
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
	} `yaml:"builds"`
}

func TestWorkflowActionContracts(t *testing.T) {
	ciRaw, ci := loadActionWorkflow(t, ".github", "workflows", "ci.yml")
	for _, test := range []struct {
		job           string
		goVersionKey  string
		goVersionWant string
	}{
		{job: "test", goVersionKey: "go-version", goVersionWant: "1.26"},
		{job: "validate-contracts", goVersionKey: "go-version-file", goVersionWant: "go.mod"},
	} {
		job := requireActionWorkflowJob(t, ci, test.job)
		requireActionWorkflowVersion(t, job, "actions/checkout", "v7")
		setup := requireActionWorkflowVersion(t, job, "actions/setup-go", "v7")
		if got := setup.With[test.goVersionKey]; got != test.goVersionWant {
			t.Errorf("CI job %q setup-go %s = %q, want %q", test.job, test.goVersionKey, got, test.goVersionWant)
		}
	}

	releaseRaw, release := loadActionWorkflow(t, ".github", "workflows", "release.yml")
	releaseJob := requireActionWorkflowJob(t, release, "release")
	requireActionWorkflowVersion(t, releaseJob, "actions/checkout", "v7")
	setup := requireActionWorkflowVersion(t, releaseJob, "actions/setup-go", "v7")
	if got := setup.With["go-version"]; got != "1.26" {
		t.Errorf("release setup-go go-version = %q, want 1.26", got)
	}
	requireActionWorkflowVersion(t, releaseJob, "goreleaser/goreleaser-action", "v7")

	publishJob := requireActionWorkflowJob(t, release, "publish-release")
	requireActionWorkflowVersion(t, publishJob, "actions/github-script", "v9")

	notifyJob := requireActionWorkflowJob(t, release, "notify-workflow-registry")
	dispatch := requireActionWorkflowVersion(t, notifyJob, "peter-evans/repository-dispatch",
		"28959ce8df70de7be546dd1250a005dd32156697")
	if got := dispatch.With["event-type"]; got != "plugin-release" {
		t.Errorf("registry dispatch event-type = %q, want plugin-release", got)
	}
	if !strings.Contains(dispatch.With["client-payload"], `"plugin": "agent"`) {
		t.Errorf("registry dispatch client-payload = %q, want agent plugin", dispatch.With["client-payload"])
	}

	for path, raw := range map[string]string{
		".github/workflows/ci.yml":      ciRaw,
		".github/workflows/release.yml": releaseRaw,
	} {
		if strings.Contains(raw, "require('@actions/github')") {
			t.Errorf("%s imports @actions/github instead of using github-script bindings", path)
		}
	}

	raw := readRepositoryFile(t, ".goreleaser.yml")
	var config actionGoReleaserConfig
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatalf("parse .goreleaser.yml: %v", err)
	}
	if len(config.Builds) != 1 {
		t.Fatalf(".goreleaser.yml builds = %d, want 1", len(config.Builds))
	}
	build := config.Builds[0]
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if !slices.Contains(build.Goos, goos) {
			t.Errorf(".goreleaser.yml missing GOOS %q", goos)
		}
	}
	for _, goarch := range []string{"amd64", "arm64"} {
		if !slices.Contains(build.Goarch, goarch) {
			t.Errorf(".goreleaser.yml missing GOARCH %q", goarch)
		}
	}
}

func loadActionWorkflow(t *testing.T, path ...string) (string, actionWorkflow) {
	t.Helper()
	raw := readRepositoryFile(t, path...)
	var workflow actionWorkflow
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatalf("parse %s: %v", filepath.Join(path...), err)
	}
	return string(raw), workflow
}

func readRepositoryFile(t *testing.T, path ...string) []byte {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	raw, err := os.ReadFile(filepath.Join(append([]string{filepath.Dir(file)}, path...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(path...), err)
	}
	return raw
}

func requireActionWorkflowJob(t *testing.T, workflow actionWorkflow, name string) actionWorkflowJob {
	t.Helper()
	job, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("workflow missing job %q", name)
	}
	return job
}

func requireActionWorkflowVersion(t *testing.T, job actionWorkflowJob, action, version string) actionWorkflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, action+"@") {
			if got, want := step.Uses, action+"@"+version; got != want {
				t.Errorf("workflow action = %q, want %q", got, want)
			}
			return step
		}
	}
	t.Fatalf("workflow job missing action %q", action)
	return actionWorkflowStep{}
}
