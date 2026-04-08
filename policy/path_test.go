package policy

import (
	"fmt"
	"os"
	"testing"
)

func TestGlobMatcherAllow(t *testing.T) {
	gm := NewGlobMatcher([]string{"/tmp/*", "/workspace/**"}, nil)
	if gm.Check("/tmp/foo.txt") != Allow {
		t.Error("/tmp/foo.txt should be allowed")
	}
}

func TestGlobMatcherDenyWins(t *testing.T) {
	gm := NewGlobMatcher([]string{"/home/**"}, []string{"/home/user/.ssh/*"})
	if gm.Check("/home/user/.ssh/id_rsa") != Deny {
		t.Error(".ssh should be denied even though /home is allowed")
	}
}

func TestGlobMatcherNoMatch(t *testing.T) {
	gm := NewGlobMatcher([]string{"/tmp/*"}, nil)
	if gm.Check("/etc/passwd") != Ask {
		t.Error("unmatched path should return Ask")
	}
}

func TestGlobMatcherDoublestar(t *testing.T) {
	gm := NewGlobMatcher([]string{"/workspace/**"}, nil)
	if gm.Check("/workspace/src/main.go") != Allow {
		t.Error("doublestar should match nested paths")
	}
	if gm.Check("/workspace/a/b/c/d.go") != Allow {
		t.Error("doublestar should match deeply nested paths")
	}
}

func TestGlobMatcherEmpty(t *testing.T) {
	gm := NewGlobMatcher(nil, nil)
	if gm.Check("/any/path") != Ask {
		t.Error("empty matcher should return Ask")
	}
}

func TestGlobMatcherDenyOnly(t *testing.T) {
	gm := NewGlobMatcher(nil, []string{"/etc/**"})
	if gm.Check("/etc/passwd") != Deny {
		t.Error("/etc should be denied")
	}
	if gm.Check("/tmp/foo") != Ask {
		t.Error("non-denied path should Ask")
	}
}

func TestGlobMatcherTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir available")
	}
	gm := NewGlobMatcher(nil, []string{"~/.ssh/*"})
	sshKey := home + "/.ssh/id_rsa"
	if gm.Check(sshKey) != Deny {
		t.Errorf("~/.ssh/* should deny %s", sshKey)
	}
}

func TestGlobMatcherSingleStarDoesNotCrossDir(t *testing.T) {
	gm := NewGlobMatcher([]string{"/tmp/*"}, nil)
	// Single * should not match across directory boundaries.
	if gm.Check("/tmp/sub/file.txt") != Ask {
		t.Error("single * should not match /tmp/sub/file.txt (crosses dir boundary)")
	}
}

func TestGlobMatcherExactMatch(t *testing.T) {
	gm := NewGlobMatcher([]string{"/workspace/main.go"}, nil)
	if gm.Check("/workspace/main.go") != Allow {
		t.Error("exact match should allow")
	}
	if gm.Check("/workspace/other.go") != Ask {
		t.Error("non-matching file should Ask")
	}
}

func TestGlobMatcherDenyBeforeAllow(t *testing.T) {
	// Even if allow matches, deny should win.
	gm := NewGlobMatcher([]string{"/**"}, []string{"/etc/**"})
	if gm.Check("/etc/passwd") != Deny {
		t.Error("/etc/passwd should be denied even when /** allows everything")
	}
	if gm.Check("/tmp/foo") != Allow {
		t.Error("/tmp/foo should be allowed by /**")
	}
}

func TestGlobMatcherMultipleDenyPatterns(t *testing.T) {
	gm := NewGlobMatcher(
		[]string{"/workspace/**"},
		[]string{"**/.git/**", "**/*.secret"},
	)
	if gm.Check("/workspace/src/main.go") != Allow {
		t.Error("normal file should be allowed")
	}
	if gm.Check("/workspace/.git/config") != Deny {
		t.Error(".git dir should be denied")
	}
	if gm.Check("/workspace/secrets/prod.secret") != Deny {
		t.Error(".secret files should be denied")
	}
}

func TestGlobMatcherCleanPath(t *testing.T) {
	gm := NewGlobMatcher([]string{"/tmp/**"}, nil)
	// Paths with redundant separators should be cleaned before matching.
	if gm.Check("/tmp//foo/./bar") != Allow {
		t.Error("cleaned path should match")
	}
}

func TestGlobMatcherWorkspaceRecursive(t *testing.T) {
	gm := NewGlobMatcher([]string{"/workspace/**"}, nil)
	paths := []string{
		"/workspace/main.go",
		"/workspace/pkg/foo/bar.go",
		"/workspace/a/b/c/d/e.txt",
	}
	for _, p := range paths {
		if gm.Check(p) != Allow {
			t.Errorf("%s should be allowed by /workspace/**", p)
		}
	}
}

func TestGlobMatcherEdgeCases(t *testing.T) {
	cases := []struct {
		name    string
		allow   []string
		deny    []string
		path    string
		want    Action
	}{
		{"root deny", nil, []string{"/**"}, "/any/path", Deny},
		{"single star no subdir", []string{"/tmp/*"}, nil, "/tmp/a/b", Ask},
		{"doublestar deep", []string{"/a/**"}, nil, "/a/b/c/d/e/f", Allow},
		{"deny star pattern", nil, []string{"/etc/*"}, "/etc/hosts", Deny},
		{"deny star no match", nil, []string{"/etc/*"}, "/etc/sub/file", Ask},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gm := NewGlobMatcher(tc.allow, tc.deny)
			got := gm.Check(tc.path)
			if got != tc.want {
				t.Errorf("Check(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func ExampleGlobMatcher() {
	gm := NewGlobMatcher(
		[]string{"/workspace/**", "/tmp/*"},
		[]string{"**/.git/**", "**/*.secret"},
	)
	fmt.Println(gm.Check("/workspace/main.go")) // Allow
	fmt.Println(gm.Check("/workspace/.git/HEAD")) // Deny
	fmt.Println(gm.Check("/etc/passwd"))           // Ask
	// Output:
	// allow
	// deny
	// ask
}
