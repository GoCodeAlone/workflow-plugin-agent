package policy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGlobMatcherNativeSeparators(t *testing.T) {
	root := filepath.Join(t.TempDir(), "workspace")
	slashRoot := filepath.ToSlash(root)
	cases := []struct {
		name  string
		allow []string
		deny  []string
		path  string
		want  Action
	}{
		{"native path slash allow", []string{slashRoot + "/**"}, nil, filepath.Join(root, "src", "main.go"), Allow},
		{"slash path native allow", []string{filepath.Join(root, "**")}, nil, slashRoot + "/src/main.go", Allow},
		{"mixed separators", []string{slashRoot + "/**"}, nil, root + "/src/" + filepath.FromSlash("pkg/main.go"), Allow},
		{"native exact match", []string{filepath.Join(root, "main.go")}, nil, slashRoot + "/main.go", Allow},
		{"slash deny wins", []string{slashRoot + "/**"}, []string{"**/.git/**"}, filepath.Join(root, ".git", "config"), Deny},
		{"native deny wins", []string{slashRoot + "/**"}, []string{filepath.Join(root, ".git", "**")}, slashRoot + "/.git/config", Deny},
		{"secret suffix deny", []string{slashRoot + "/**"}, []string{"**/*.secret"}, filepath.Join(root, "secrets", "prod.secret"), Deny},
		{"single star stops at directory", []string{filepath.Join(root, "*")}, nil, filepath.Join(root, "src", "main.go"), Ask},
		{"cleaned path enters deny", []string{slashRoot + "/**"}, []string{slashRoot + "/.git/**"}, root + "/src/../.git/./config", Deny},
		{"malformed native exact fallback", []string{filepath.Join(root, "[")}, nil, slashRoot + "/[", Allow},
		{"malformed native deny fallback", []string{slashRoot + "/**"}, []string{filepath.Join(root, "[")}, slashRoot + "/[", Deny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewGlobMatcher(tc.allow, tc.deny).Check(tc.path); got != tc.want {
				t.Errorf("Check(%q) = %v, want %v (allow %q, deny %q)", tc.path, got, tc.want, tc.allow, tc.deny)
			}
		})
	}
}

func TestEvaluatePathNativeTildeDenyOverridesAllow(t *testing.T) {
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	resolvedHome, err := os.UserHomeDir()
	if err != nil || resolvedHome != home {
		t.Fatalf("home fixture: got %q, %v; want %q", resolvedHome, err, home)
	}
	te := NewTrustEngine("custom", []TrustRule{
		{Pattern: "path:**", Action: Allow},
		{Pattern: "path:~/.ssh/*", Action: Deny},
	}, nil)
	denied := filepath.Join(home, ".ssh", "id_rsa")
	if got := te.EvaluatePath(denied); got != Deny {
		t.Errorf("native home path %q = %v, want Deny despite broad allow", denied, got)
	}
	allowed := filepath.Join(home, "notes.txt")
	if got := te.EvaluatePath(allowed); got != Allow {
		t.Errorf("allowed control %q = %v, want Allow", allowed, got)
	}
}
