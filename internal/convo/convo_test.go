package convo

import (
	"strings"
	"testing"
)

// Claude Code derives a project directory name by replacing every character
// outside [A-Za-z0-9] with "-". Runs are not collapsed and case is kept.
func TestEncodeProjectPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "underscores become hyphens",
			in:   "/Users/foo/engagements/epiq_csf_2026-04-0702/github",
			want: "-Users-foo-engagements-epiq-csf-2026-04-0702-github",
		},
		{
			name: "dots become hyphens",
			in:   "/Users/foo/.claude2",
			want: "-Users-foo--claude2",
		},
		{
			name: "spaces become hyphens",
			in:   "/Users/foo/my project",
			want: "-Users-foo-my-project",
		},
		{
			name: "existing hyphens and case are preserved",
			in:   "/Users/foo/engagements/fox-sports-2026/Rao-findings",
			want: "-Users-foo-engagements-fox-sports-2026-Rao-findings",
		},
		{
			name: "plain path is unchanged apart from separators",
			in:   "/Users/foo/engagements/cfa",
			want: "-Users-foo-engagements-cfa",
		},
		{
			// The reference regex carries no /u flag, so it substitutes per UTF-16
			// code unit: an astral-plane rune is two units and yields two hyphens.
			name: "astral-plane rune yields two hyphens",
			in:   "/Users/foo/proj-\U0001F600-x",
			want: "-Users-foo-proj----x",
		},
		{
			name: "single-unit non-ascii rune yields one hyphen",
			in:   "/Users/foo/café",
			want: "-Users-foo-caf-",
		},
		{
			name: "empty path",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EncodeProjectPath(tt.in); got != tt.want {
				t.Fatalf("EncodeProjectPath(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

// Past 200 code units the name is truncated and given a base-36 hash of the
// ORIGINAL path. Expected values come from the reference implementation.
func TestEncodeProjectPathTruncatesLongPaths(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "long run of letters",
			in:   "/Users/foo/" + strings.Repeat("a", 250),
			want: "-Users-foo-" + strings.Repeat("a", 189) + "-cqnqgx",
		},
		{
			name: "long path containing separators",
			in:   "/Users/foo/" + strings.Repeat("a_b", 80),
			want: "-Users-foo-" + strings.Repeat("a-b", 63) + "-kxognz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodeProjectPath(tt.in)
			if got != tt.want {
				t.Fatalf("EncodeProjectPath(len %d)\n got: %q (len %d)\nwant: %q (len %d)",
					len(tt.in), got, len(got), tt.want, len(tt.want))
			}
		})
	}
}
