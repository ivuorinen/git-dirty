package gitstat

import "testing"

// gitLabel returns a func deriving the expected head label from git itself:
// the output of git with args.
func gitLabel(args ...string) func(t *testing.T, dir string) string {
	return func(t *testing.T, dir string) string {
		t.Helper()

		return git(t, dir, args...)
	}
}

func TestHead(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) string
		// want derives the expected label from git itself.
		want         func(t *testing.T, dir string) string
		wantBranch   bool
		wantUnborn   bool
		wantDetached bool
	}{
		{
			name: "branch",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := newRepo(t)
				git(t, dir, "switch", "-q", "-c", "feature/x")

				return dir
			},
			want:       gitLabel("symbolic-ref", "--short", "HEAD"),
			wantBranch: true,
		},
		{
			name: "detached on lightweight tag",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := newRepo(t)
				git(t, dir, "tag", "v1")
				commitFile(t, dir, "b.txt", "b\n", "next")
				git(t, dir, "switch", "-q", "--detach", "v1")

				return dir
			},
			want:         gitLabel("describe", "--tags", "--exact-match"),
			wantDetached: true,
		},
		{
			name: "detached on annotated tag",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := newRepo(t)
				git(t, dir, "tag", "-a", "-m", "release", "v2")
				commitFile(t, dir, "b.txt", "b\n", "next")
				git(t, dir, "switch", "-q", "--detach", "v2")

				return dir
			},
			want:         gitLabel("describe", "--tags", "--exact-match"),
			wantDetached: true,
		},
		{
			name: "detached without tag",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := newRepo(t)
				commitFile(t, dir, "b.txt", "b\n", "next")
				git(t, dir, "switch", "-q", "--detach", "HEAD~1")

				return dir
			},
			want:         gitLabel("rev-parse", "--short", "HEAD"),
			wantDetached: true,
		},
		{
			name: "unborn",
			setup: func(t *testing.T) string {
				t.Helper()
				dir := t.TempDir()
				git(t, dir, "init", "-q")

				return dir
			},
			want:       gitLabel("symbolic-ref", "--short", "HEAD"),
			wantBranch: true,
			wantUnborn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setup(t)
			h, err := open(t, dir).head()
			if err != nil {
				t.Fatal(err)
			}
			if want := tt.want(t, dir); h.Label != want {
				t.Errorf("Label = %q, want %q (from git)", h.Label, want)
			}
			if tt.wantBranch && h.Branch != h.Label {
				t.Errorf("Branch = %q, want %q", h.Branch, h.Label)
			}
			if h.Detached() != tt.wantDetached {
				t.Errorf("Detached = %v, want %v", h.Detached(), tt.wantDetached)
			}
			if h.Unborn != tt.wantUnborn {
				t.Errorf("Unborn = %v, want %v", h.Unborn, tt.wantUnborn)
			}
			if !h.Unborn {
				if got, want := h.Hash.String(), git(t, dir, "rev-parse", "HEAD"); got != want {
					t.Errorf("Hash = %s, want %s", got, want)
				}
			}
		})
	}
}
