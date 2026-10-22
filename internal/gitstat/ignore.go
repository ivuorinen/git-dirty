package gitstat

import (
	"os"
	"path/filepath"
	"strings"

	gitrepo "github.com/ivuorinen/git-dirty/internal/git"
)

// Excludes is the core.excludesFile resolved from the system and global git
// config. It is built once per run and only read afterwards, so every worker
// can share it.
type Excludes struct {
	path     string            // excludes file; may be relative (resolved per repo)
	patterns []gitrepo.Pattern // parsed from path when it is absolute
}

// LoadExcludes resolves core.excludesFile the way git does: the system config
// (unless GIT_CONFIG_NOSYSTEM is set), then the global config
// (GIT_CONFIG_GLOBAL, else $XDG_CONFIG_HOME/git/config and ~/.gitconfig, the
// later file winning), defaulting to $XDG_CONFIG_HOME/git/ignore.
func LoadExcludes() (*Excludes, error) {
	home := gitHome()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}

	var files []string
	if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		files = append(files, "/etc/gitconfig")
	}
	if g := os.Getenv("GIT_CONFIG_GLOBAL"); g != "" {
		files = append(files, g)
	} else {
		if xdg != "" {
			files = append(files, filepath.Join(xdg, "git", "config"))
		}
		if home != "" {
			files = append(files, filepath.Join(home, ".gitconfig"))
		}
	}

	var path string
	for _, f := range files {
		cfg, err := gitrepo.ReadConfigFile(f)
		if err != nil {
			return nil, err
		}
		if v, _ := cfg.Get("core", "", "excludesfile"); v != "" {
			path = v
		}
	}
	if path == "" && xdg != "" {
		path = filepath.Join(xdg, "git", "ignore")
	}
	path = expandHome(path, home)

	ex := &Excludes{path: path}
	if path != "" && filepath.IsAbs(path) {
		ps, err := gitrepo.ReadPatterns(path, nil)
		if err != nil {
			return nil, err
		}
		ex.patterns = ps
	}
	return ex, nil
}

// gitHome is the directory git expands ~ to: $HOME on every platform. When
// HOME is unset, Git for Windows derives it from the user profile, the
// directory os.UserHomeDir returns there (os.UserHomeDir alone would ignore a
// HOME that Git Bash or the user set).
func gitHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func expandHome(p, home string) string {
	if home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// rootIgnores returns the patterns that apply at the worktree root, lowest
// precedence first: core.excludesFile (the repository's own setting replaces
// the global one), then info/exclude. Matching folds case when
// core.ignorecase is set, as git does.
func (r *Repo) rootIgnores(ex *Excludes) (ignoreStack, error) {
	var ps []gitrepo.Pattern
	path := ""
	if ex != nil {
		path = ex.path
		ps = ex.patterns
	}
	cfg := r.repo.Config
	override := false
	if v, _ := cfg.Get("core", "", "excludesfile"); v != "" {
		path, override = expandHome(v, gitHome()), true
	}
	// The shared parse covers only an absolute global path; a repository
	// override or a relative path (git reads it from the worktree root) is
	// parsed here.
	if path != "" && (override || !filepath.IsAbs(path)) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.worktree, path)
		}
		var err error
		if ps, err = gitrepo.ReadPatterns(path, nil); err != nil {
			return ignoreStack{}, err
		}
	}
	info, err := gitrepo.ReadPatterns(filepath.Join(r.repo.CommonDir, "info", "exclude"), nil)
	if err != nil {
		return ignoreStack{}, err
	}
	all := make([]gitrepo.Pattern, 0, len(ps)+len(info))
	all = append(append(all, ps...), info...)
	return ignoreStack{patterns: all, ignoreCase: cfg.Bool("core", "", "ignorecase", false)}, nil
}

// ignoreStack holds the patterns in effect for one directory while walking.
// push never modifies the receiver's slice, so siblings cannot see each
// other's .gitignore rules.
type ignoreStack struct {
	patterns   []gitrepo.Pattern
	ignoreCase bool
}

// push adds the .gitignore in dirAbs, whose path relative to the worktree is
// domain.
func (s ignoreStack) push(dirAbs string, domain []string) (ignoreStack, error) {
	ps, err := gitrepo.ReadPatterns(filepath.Join(dirAbs, ".gitignore"), domain)
	if err != nil || len(ps) == 0 {
		return s, err
	}
	all := make([]gitrepo.Pattern, 0, len(s.patterns)+len(ps))
	all = append(append(all, s.patterns...), ps...)
	return ignoreStack{patterns: all, ignoreCase: s.ignoreCase}, nil
}

func (s ignoreStack) match(path []string, isDir bool) bool {
	return gitrepo.Matcher{Patterns: s.patterns, IgnoreCase: s.ignoreCase}.Ignored(path, isDir)
}
