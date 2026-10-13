//go:build !windows

package policy

import "testing"

func TestGlobMatcherUnixEscaping(t *testing.T) {
	cases := []struct {
		name  string
		allow []string
		deny  []string
		path  string
		want  Action
	}{
		{"escaped wildcard deny", []string{"/workspace/**"}, []string{`/workspace/\*.secret`}, "/workspace/*.secret", Deny},
		{"escaped wildcard stays literal", []string{"/workspace/**"}, []string{`/workspace/\*.secret`}, "/workspace/prod.secret", Allow},
		{"literal backslash deny", []string{"/workspace/**"}, []string{`/workspace/name\\file`}, `/workspace/name\file`, Deny},
		{"backslash is not separator", []string{`/workspace/name/file`}, nil, `/workspace/name\file`, Ask},
		{"case sensitive deny preserved", []string{"/workspace/**"}, []string{"/workspace/Secret"}, "/workspace/secret", Allow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewGlobMatcher(tc.allow, tc.deny).Check(tc.path); got != tc.want {
				t.Errorf("Check(%q) = %v, want %v (allow %q, deny %q)", tc.path, got, tc.want, tc.allow, tc.deny)
			}
		})
	}
}
