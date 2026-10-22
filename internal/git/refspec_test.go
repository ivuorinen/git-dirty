package git

import "testing"

func specs(t *testing.T, in ...string) []RefSpec {
	t.Helper()
	var out []RefSpec
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
		{"default", []string{"+refs/heads/*:refs/remotes/origin/*"}, "refs/heads/feat/x", "refs/remotes/origin/feat/x", true},
		{"exact", []string{"refs/heads/main:refs/remotes/origin/main"}, "refs/heads/main", "refs/remotes/origin/main", true},
		{"exact no match", []string{"refs/heads/main:refs/remotes/origin/main"}, "refs/heads/dev", "", false},
		{"star in middle", []string{"refs/heads/team/*/x:refs/remotes/o/*"}, "refs/heads/team/a/b/x", "refs/remotes/o/a/b", true},
		{"star in middle, wrong suffix", []string{"refs/heads/team/*/x:refs/remotes/o/*"}, "refs/heads/team/a/y", "", false},
		{"negative excludes", []string{"+refs/heads/*:refs/remotes/origin/*", "^refs/heads/secret/*"}, "refs/heads/secret/k", "", false},
		{"negative does not exclude others", []string{"+refs/heads/*:refs/remotes/origin/*", "^refs/heads/secret/*"}, "refs/heads/main", "refs/remotes/origin/main", true},
		{"first positive wins", []string{"refs/heads/main:refs/remotes/a/main", "+refs/heads/*:refs/remotes/b/*"}, "refs/heads/main", "refs/remotes/a/main", true},
		{"empty dst maps nowhere", []string{"refs/heads/main:"}, "refs/heads/main", "", false},
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
	for _, s := range []string{"refs/heads/*/*:refs/remotes/o/*/*", "refs/heads/*:refs/remotes/o/x", "refs/heads/x", ":refs/x", "^refs/a:refs/b"} {
		if _, err := ParseRefSpec(s); err == nil {
			t.Errorf("ParseRefSpec(%q): want error", s)
		}
	}
}
