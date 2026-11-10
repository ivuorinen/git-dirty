package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Variables named by more than one test.
const (
	envMaxDepth = "GIT_DIRTY_MAXDEPTH"
	envColor    = "GIT_DIRTY_COLOR"
	envNoColor  = "NO_COLOR"
	goosWindows = "windows"
)

var envNames = []string{
	"GIT_DIRTY_DIR", "GIT_DIRTY_DEPTH", envMaxDepth,
	"GIT_DIRTY_CHECK_STASH", "GIT_DIRTY_CHECK_UNTRACKED", "GIT_DIRTY_EXCLUDE",
	envColor, "GIT_DIRTY_TRUNCATE", "GIT_DIRTY_SHOW_BRANCH",
	"GIT_DIRTY_MAIN_BRANCHES", "VERBOSE", envNoColor,
}

// isolate points the config lookup at a temp dir and clears every variable
// Load reads, so the developer's own environment cannot leak in.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // os.UserHomeDir on Windows
	for _, n := range envNames {
		t.Setenv(n, "") // restores the original value after the test
		if err := os.Unsetenv(n); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func writeConfig(t *testing.T, xdg, body string) {
	t.Helper()
	p := filepath.Join(xdg, "git-dirty", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// xdgFile is the default config file, as isolate's environment places it.
func xdgFile(t *testing.T) string {
	t.Helper()
	p, err := Path()
	if err != nil {
		t.Fatal(err)
	}

	return p
}

// TestSplitList: blanks and empty items go, and no items is an empty list,
// which a saved config holds as [] and not null.
func TestSplitList(t *testing.T) {
	for in, want := range map[string][]string{
		"":          {},
		" , ,":      {},
		"a":         {"a"},
		" a , b,,c": {"a", "b", "c"},
	} {
		if got := SplitList(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitList(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func TestLoad_NoFileGivesDefaults(t *testing.T) {
	isolate(t)
	cfg, err := Load(xdgFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("got %+v, want defaults %+v", cfg, Defaults())
	}
}

// TestLoad_Path: Load reads the file it is given and not the XDG one, and a
// missing file gives the defaults, as a missing XDG file does.
func TestLoad_Path(t *testing.T) {
	xdg := isolate(t)
	writeConfig(t, xdg, `{"depth": 2}`)
	custom := filepath.Join(t.TempDir(), "custom.json")
	if err := os.WriteFile(custom, []byte(`{"maxDepth": 9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(custom)
	if err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.MaxDepth = 9
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load(custom) = %+v\nwant %+v", cfg, want)
	}
	cfg, err = Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil || !reflect.DeepEqual(cfg, Defaults()) {
		t.Errorf("Load(missing) = %+v, %v; want the defaults", cfg, err)
	}
}

// TestLoadFile_IgnoresTheEnvironment: LoadFile is the file layer only;
// ApplyEnv adds the environment on a copy.
func TestLoadFile_IgnoresTheEnvironment(t *testing.T) {
	xdg := isolate(t)
	writeConfig(t, xdg, `{"depth": 2}`)
	t.Setenv("GIT_DIRTY_DEPTH", "7")
	file, err := LoadFile(xdgFile(t))
	if err != nil {
		t.Fatal(err)
	}
	if file.Depth != 2 {
		t.Errorf("LoadFile depth = %d, want the file's 2", file.Depth)
	}
	eff, err := ApplyEnv(file)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Depth != 7 || file.Depth != 2 {
		t.Errorf("ApplyEnv depth = %d (file layer now %d), want 7 and the file layer unchanged", eff.Depth, file.Depth)
	}
}

// TestLoad_Precedence compares the whole Config, so a test also fails when a
// setting it does not name loses its default.
func TestLoad_Precedence(t *testing.T) {
	tests := []struct {
		name string
		file string
		env  map[string]string
		want func(c *Config) // applied to Defaults()
	}{
		{
			name: "file overrides default",
			file: `{"depth": 2, "dir": "/from/file"}`,
			want: func(c *Config) { c.Depth, c.Dir = 2, "/from/file" },
		},
		{
			name: "env overrides file",
			file: `{"depth": 2, "dir": "/from/file", "checkStash": false}`,
			env:  map[string]string{"GIT_DIRTY_DEPTH": "7", "GIT_DIRTY_DIR": "/from/env", "GIT_DIRTY_CHECK_STASH": "1"},
			want: func(c *Config) { c.Depth, c.Dir, c.CheckStash = 7, "/from/env", true },
		},
		{
			name: "env exclude replaces the list",
			env:  map[string]string{"GIT_DIRTY_EXCLUDE": "a  b"},
			want: func(c *Config) { c.Exclude = []string{"a", "b"} },
		},
		{
			name: "env main branches",
			env:  map[string]string{"GIT_DIRTY_MAIN_BRANCHES": "develop"},
			want: func(c *Config) { c.MainBranches = []string{"develop"} },
		},
		{
			name: "NO_COLOR beats file and GIT_DIRTY_COLOR",
			file: `{"color": true}`,
			env:  map[string]string{envColor: "1", envNoColor: "1"},
			want: func(c *Config) { c.Color = false },
		},
		{
			name: "empty NO_COLOR is ignored",
			env:  map[string]string{envNoColor: ""},
			want: func(*Config) {},
		},
		{
			name: "VERBOSE env",
			env:  map[string]string{"VERBOSE": "1"},
			want: func(c *Config) { c.Verbose = true },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xdg := isolate(t)
			if tt.file != "" {
				writeConfig(t, xdg, tt.file)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := Load(xdgFile(t))
			if err != nil {
				t.Fatal(err)
			}
			want := Defaults()
			tt.want(&want)
			if !reflect.DeepEqual(cfg, want) {
				t.Errorf("got  %+v\nwant %+v", cfg, want)
			}
		})
	}
}

// nonDefaultConfig changes one setting of each type from its default.
func nonDefaultConfig() Config {
	c := Defaults()
	c.Dir, c.MaxDepth, c.CheckStash, c.Exclude = "/src", 9, true, []string{"tmp"}

	return c
}

func TestSave_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json") // the directory is created
	want := nonDefaultConfig()
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadFile(Save(c)) = %+v\nwant %+v", got, want)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"dir"`, `"maxDepth"`, `"checkStash"`, `"exclude"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("saved file lacks %s:\n%s", key, data)
		}
	}
	if strings.Contains(string(data), `"depth"`) {
		t.Errorf("saved file holds a default value:\n%s", data)
	}
}

func TestSave_DefaultsWriteAnEmptyObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, Defaults()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "{}" {
		t.Errorf("Save(Defaults()) wrote %q, want {}", got)
	}
}

func TestSave_FollowsASymlink(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "git-dirty.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := Save(link, nonDefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.json is no longer a symlink: %v", err)
	}
	got, err := LoadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, nonDefaultConfig()) {
		t.Errorf("the symlink's target holds %+v", got)
	}
}

// TestSave_CreatesADanglingSymlinkTarget: a link whose target does not exist
// yet, relative here, stays a link and the save creates the target.
func TestSave_CreatesADanglingSymlinkTarget(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "config.json")
	if err := os.Symlink(filepath.Join("dotfiles", "git-dirty.json"), link); err != nil {
		t.Fatal(err)
	}
	if err := Save(link, nonDefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.json is no longer a symlink: %v", err)
	}
	got, err := LoadFile(filepath.Join(dir, "dotfiles", "git-dirty.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, nonDefaultConfig()) {
		t.Errorf("the created target holds %+v", got)
	}
}

// TestSave_SymlinkCycleFails: a cycle is an error, not an endless walk.
func TestSave_SymlinkCycleFails(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if err := Save(a, nonDefaultConfig()); err == nil {
		t.Error("Save through a symlink cycle succeeded")
	}
}

func TestSave_KeepsTheMode(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("Windows has no Unix file modes")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 0400, not the 0600 a temporary file starts with, so keeping the mode
	// is what makes the test pass. The save renames over the file, so it
	// need not be writable.
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, nonDefaultConfig()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o400 {
		t.Errorf("mode after save = %o, want 400", got)
	}
	fresh := filepath.Join(t.TempDir(), "config.json")
	if err := Save(fresh, nonDefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(fresh); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v, %v; want 600", fi.Mode().Perm(), err)
	}
}

// TestSave_FailureLeavesNoTemporaryFile: a rename over a directory fails
// after the temporary file is written.
func TestSave_FailureLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, nonDefaultConfig()); err == nil {
		t.Fatal("Save over a directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %q, want only config.json", names)
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name string
		file string
		env  map[string]string
		want string
	}{
		{name: "unknown key names the key", file: `{"colors": true}`, want: `"colors"`},
		{name: "wrong type", file: `{"depth": "two"}`, want: "depth"},
		{name: "trailing data", file: `{} {}`, want: "unexpected data"},
		{name: "bad int env", env: map[string]string{envMaxDepth: "x"}, want: envMaxDepth},
		{name: "bad bool env", env: map[string]string{envColor: "maybe"}, want: envColor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xdg := isolate(t)
			if tt.file != "" {
				writeConfig(t, xdg, tt.file)
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load(xdgFile(t))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if tt.file != "" && !strings.Contains(err.Error(), "config.json") {
				t.Errorf("file error does not name the file: %v", err)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	home := isolate(t)
	tests := []struct {
		name    string
		mut     func(*Config)
		wantErr string
		wantDir string
	}{
		{name: "tilde expands", mut: func(c *Config) { c.Dir = "~/Code" }, wantDir: filepath.Join(home, "Code")},
		{name: "bare tilde", mut: func(c *Config) { c.Dir = "~" }, wantDir: home},
		{name: "tilde-user is left alone", mut: func(c *Config) { c.Dir = "~bob/x" }, wantDir: "~bob/x"},
		{name: "negative depth", mut: func(c *Config) { c.Depth = -1 }, wantErr: "depth"},
		{name: "zero maxDepth", mut: func(c *Config) { c.MaxDepth = 0 }, wantErr: "maxDepth"},
		{name: "empty exclude", mut: func(c *Config) { c.Exclude = []string{"a", " "} }, wantErr: "exclude"},
		{name: "empty dir", mut: func(c *Config) { c.Dir = "" }, wantErr: "dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Defaults()
			tt.mut(&c)
			err := c.Normalize()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Dir != tt.wantDir {
				t.Errorf("dir = %q, want %q", c.Dir, tt.wantDir)
			}
		})
	}
}

func TestPath_FallsBackToHome(t *testing.T) {
	home := isolate(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	got, err := Path()
	if want := filepath.Join(home, ".config", "git-dirty", "config.json"); err != nil || got != want {
		t.Errorf("Path() = %q, %v, want %q", got, err, want)
	}
}

// noHome leaves no way to find the home directory, or skips where there is
// one regardless.
func noHome(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if h, err := os.UserHomeDir(); err == nil {
		t.Skipf("this platform has a home without HOME: %s", h)
	}
}

func TestPath_NoHome(t *testing.T) {
	noHome(t)
	if got, err := Path(); err == nil {
		t.Errorf("Path() = %q, want an error", got)
	}
}

func TestNormalize_NoHome(t *testing.T) {
	noHome(t)
	c := Defaults()
	c.Dir = "~/code"
	if err := c.Normalize(); err == nil || !strings.HasPrefix(err.Error(), "dir:") {
		t.Errorf("err = %v, want a dir: error", err)
	}
}

// A path below a regular file cannot be opened, and does not read as missing.
func TestLoadFile_NotReadable(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("Windows reports a path below a file as missing")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(file, "config.json")); err == nil {
		t.Error("a path below a file: want error, not the defaults")
	}
}

func TestLoad_BadDepthEnv(t *testing.T) {
	isolate(t)
	t.Setenv("GIT_DIRTY_DEPTH", "x")
	if _, err := Load(xdgFile(t)); err == nil || !strings.Contains(err.Error(), "GIT_DIRTY_DEPTH") {
		t.Errorf("err = %v, want it to name the variable", err)
	}
}

func TestSave_BelowAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(filepath.Join(file, "config.json"), Defaults()); err == nil {
		t.Error("want error")
	}
}

// A dangling symlink with an absolute target is created at that target.
func TestSave_AbsoluteDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere", "config.json")
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := Save(link, Defaults()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("target not created: %v", err)
	}
}

// A directory that cannot be written to stops the save at either step: making
// the config's folder, or writing the temporary file beside it.
func TestSave_ReadOnlyDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // G302: the point is a directory it cannot write to
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // G302: a directory has to be searchable
			t.Error(err)
		}
	})
	if err := os.Mkdir(filepath.Join(dir, "probe"), 0o700); err == nil {
		t.Skip("the mode does not stop this user")
	}
	if err := Save(filepath.Join(dir, "new", "config.json"), Defaults()); err == nil {
		t.Error("the folder cannot be made: want error")
	}
	if err := Save(filepath.Join(dir, "config.json"), Defaults()); err == nil {
		t.Error("the temporary file cannot be written: want error")
	}
}
