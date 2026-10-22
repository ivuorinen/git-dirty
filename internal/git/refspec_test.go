package git

import "testing"

// Ref names and refspecs the tests share.
const (
	refMain      = "refs/heads/main"
	defaultFetch = "+refs/heads/*:refs/remotes/origin/*"
)

func specs(t *testing.T, in ...string) []RefSpec {
	t.Helper()
	out := make([]RefSpec, 0, len(in))
	for _, s := range in {
		rs, err := ParseRefSpec(s)
		if err != nil {
			t.Fatalf("ParseRefSpec(%q): %v", s, err)
		}
		out = append(out, rs)
	}

	return out
}

func TestMapRef(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		ref   string
		want  string
		ok    bool
	}{
		{
			"default",
			[]string{defaultFetch},
			"refs/heads/feat/x",
			"refs/remotes/origin/feat/x",
			true,
		},
		{
			"exact",
			[]string{"refs/heads/main:refs/remotes/origin/main"},
			refMain,
			"refs/remotes/origin/main",
			true,
		},
		{"exact no match", []string{"refs/heads/main:refs/remotes/origin/main"}, "refs/heads/dev", "", false},
		{
			"star in middle",
			[]string{"refs/heads/team/*/x:refs/remotes/o/*"},
			"refs/heads/team/a/b/x",
			"refs/remotes/o/a/b",
			true,
		},
		{
			"star in middle, wrong suffix",
			[]string{"refs/heads/team/*/x:refs/remotes/o/*"},
			"refs/heads/team/a/y",
			"",
			false,
		},
		{
			"negative excludes",
			[]string{defaultFetch, "^refs/heads/secret/*"},
			"refs/heads/secret/k",
			"",
			false,
		},
		{
			"negative does not exclude others",
			[]string{defaultFetch, "^refs/heads/secret/*"},
			refMain,
			"refs/remotes/origin/main",
			true,
		},
		{
			"first positive wins",
			[]string{"refs/heads/main:refs/remotes/a/main", "+refs/heads/*:refs/remotes/b/*"},
			refMain,
			"refs/remotes/a/main",
			true,
		},
		{"empty dst maps nowhere", []string{refMain + ":"}, refMain, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := MapRef(specs(t, tt.specs...), tt.ref)
			if got != tt.want || ok != tt.ok {
				t.Errorf("MapRef = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseRefSpec_Malformed(t *testing.T) {
	malformed := []string{
		"refs/heads/*/*:refs/remotes/o/*/*",
		"refs/heads/*:refs/remotes/o/x",
		"refs/heads/x",
		":refs/x",
		"^refs/a:refs/b",
	}
	for _, s := range malformed {
		if _, err := ParseRefSpec(s); err == nil {
			t.Errorf("ParseRefSpec(%q): want error", s)
		}
	}
}
