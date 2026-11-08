package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ivuorinen/git-dirty/internal/gitstat"
)

// kinds is every action.
var kinds = []Kind{Fetch, FetchPrune, Pull, PullRebase}

// fixtures builds one repository per case Check distinguishes, with git,
// and returns what gitstat.Inspect makes of each.
func fixtures(t *testing.T) map[string]*gitstat.Result {
	t.Helper()
	up := newRepo(t)
	dirs := map[string]string{}

	dirs["ordinary"] = clone(t, up)

	dirs["detached"] = clone(t, up)
	git(t, dirs["detached"], "checkout", "-q", "--detach")

	dirs["no upstream"] = newRepo(t)

	dirs["upstream gone"] = clone(t, up)
	git(t, dirs["upstream gone"], "config", "branch."+mainBranch+".merge", "refs/heads/gone")

	dirs["bare"] = filepath.Join(t.TempDir(), "bare.git")
	git(t, up, "clone", "-q", "--bare", up, dirs["bare"])

	dirs["unreadable"] = clone(t, up)
	write(t, dirs["unreadable"], ".git/index", "not an index")

	dirs["unborn"] = t.TempDir()
	git(t, dirs["unborn"], "init", "-q")

	out := map[string]*gitstat.Result{}
	for name, dir := range dirs {
		res := gitstat.Inspect(context.Background(), dir, gitstat.Options{})
		out[name] = &res
	}

	return out
}

// TestFixtures checks that each fixture is the case its name says, so
// TestCheck tests Check and not the fixtures.
func TestFixtures(t *testing.T) {
	upstream := func(res *gitstat.Result, gone bool) bool {
		return res.Err == nil && res.Upstream != nil && res.Upstream.Gone == gone
	}
	checks := map[string]func(*gitstat.Result) bool{
		"ordinary":      func(res *gitstat.Result) bool { return upstream(res, false) },
		"detached":      func(res *gitstat.Result) bool { return res.Err == nil && res.Head.Detached() },
		"no upstream":   func(res *gitstat.Result) bool { return res.Err == nil && res.Upstream == nil },
		"upstream gone": func(res *gitstat.Result) bool { return upstream(res, true) },
		"bare":          func(res *gitstat.Result) bool { return res.Err == nil && res.Bare },
		"unreadable":    func(res *gitstat.Result) bool { return res.Err != nil && !res.Bare },
		"unborn":        func(res *gitstat.Result) bool { return res.Err == nil && res.Head.Unborn },
	}
	for name, res := range fixtures(t) {
		if !checks[name](res) {
			t.Errorf("fixture %q is not that case: %+v", name, *res)
		}
	}
}

func TestCheck(t *testing.T) {
	r := fixtures(t)
	tests := []struct {
		name  string
		res   *gitstat.Result
		fetch error // Fetch and FetchPrune
		pull  error // Pull and PullRebase
	}{
		{"ordinary", r["ordinary"], nil, nil},
		{"detached", r["detached"], nil, ErrDetached},
		{"no upstream", r["no upstream"], nil, ErrNoUpstream},
		{"upstream gone", r["upstream gone"], nil, ErrUpstreamGone},
		{"bare", r["bare"], nil, ErrBare},
		{"unreadable", r["unreadable"], nil, ErrUnreadable},
		{"unborn", r["unborn"], nil, ErrUnborn},
		{"directory", nil, ErrNotRepository, ErrNotRepository},
	}
	for _, tt := range tests {
		for _, k := range kinds {
			want := tt.pull
			if k == Fetch || k == FetchPrune {
				want = tt.fetch
			}
			if got := k.Check(tt.res); !errors.Is(got, want) || (want == nil) != (got == nil) {
				t.Errorf("%s: %s.Check = %v, want %v", tt.name, k, got, want)
			}
		}
	}
}

func TestAsks(t *testing.T) {
	for _, k := range kinds {
		if want := k == FetchPrune || k == PullRebase; k.Asks() != want {
			t.Errorf("%s.Asks() = %v, want %v", k, k.Asks(), want)
		}
	}
}

// TestUnreadableFixtureFailsInGit: the unreadable fixture is unreadable to
// git as well, which is why Check refuses to pull it.
func TestUnreadableFixtureFailsInGit(t *testing.T) {
	dir := clone(t, newRepo(t))
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := gitCode(t, dir, "status", "--porcelain"); code == 0 {
		t.Error("git status succeeded on a corrupt index")
	}
}
