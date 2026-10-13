package policy

import "testing"

func TestGlobMatcherWindowsDriveAndUNC(t *testing.T) {
	cases := []struct {
		name  string
		allow []string
		deny  []string
		path  string
		want  Action
	}{
		{"drive native path", []string{"C:/workspace/**"}, nil, `C:\workspace\src\main.go`, Allow},
		{"drive native pattern", []string{`C:\workspace\**`}, nil, "C:/workspace/src/main.go", Allow},
		{"drive mixed separators deny", []string{`C:\workspace\**`}, []string{"**/.git/**"}, `C:\workspace/src\.git/HEAD`, Deny},
		{"different drive", []string{`C:\workspace\**`}, nil, `D:\workspace\main.go`, Ask},
		{"case sensitive deny preserved", []string{"C:/workspace/**"}, []string{"C:/workspace/Secret"}, `C:\workspace\secret`, Allow},
		{"UNC native path", []string{"//server/share/workspace/**"}, nil, `\\server\share\workspace\main.go`, Allow},
		{"UNC native pattern", []string{`\\server\share\workspace\**`}, nil, "//server/share/workspace/src/main.go", Allow},
		{"UNC deny wins", []string{"//server/share/workspace/**"}, []string{`\\server\share\workspace\.git\**`}, "//server/share/workspace/.git/config", Deny},
		{"different UNC share", []string{`\\server\share\workspace\**`}, nil, `\\server\other\workspace\main.go`, Ask},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewGlobMatcher(tc.allow, tc.deny).Check(tc.path); got != tc.want {
				t.Errorf("Check(%q) = %v, want %v (allow %q, deny %q)", tc.path, got, tc.want, tc.allow, tc.deny)
			}
		})
	}
}
