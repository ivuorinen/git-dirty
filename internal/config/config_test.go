package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var envNames = []string{
	"GIT_DIRTY_DIR", "GIT_DIRTY_DEPTH", "GIT_DIRTY_MAXDEPTH",
	"GIT_DIRTY_CHECK_STASH", "GIT_DIRTY_CHECK_UNTRACKED", "GIT_DIRTY_EXCLUDE",
	"GIT_DIRTY_COLOR", "GIT_DIRTY_TRUNCATE", "GIT_DIRTY_SHOW_BRANCH",
	"GIT_DIRTY_MAIN_BRANCHES", "VERBOSE", "NO_COLOR",
}

// isolate points the config lookup at a temp dir and clears every variable
// Load reads, so the developer's own environment cannot leak in.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	for _, n := range envNames {
		t.Setenv(n, "")
		os.Unsetenv(n)
	}
	return dir
}

func writeConfig(t *testing.T, xdg, body string) {
	t.Helper()
	p := filepath.Join(xdg, "git-dirty", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_NoFileGivesDefaults(t *testing.T) {
	isolate(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, Defaults()) {
		t.Fatalf("got %+v, want defaults %+v", cfg, Defaults())
	}
}

func TestLoad_Precedence(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		env   map[string]string
		check func(t *testing.T, c Config)
	}{
		{
			name: "file overrides default",
			file: `{"depth": 2, "dir": "/from/file"}`,
			check: func(t *testing.T, c Config) {
				if c.Depth != 2 || c.Dir != "/from/file" {
					t.Errorf("depth=%d dir=%q", c.Depth, c.Dir)
				}
				if c.MaxDepth != 15 {
					t.Errorf("unset key lost its default: maxDepth=%d", c.MaxDepth)
				}
			},
		},
		{
			name: "env overrides file",
			file: `{"depth": 2, "dir": "/from/file", "checkStash": false}`,
			env:  map[string]string{"GIT_DIRTY_DEPTH": "7", "GIT_DIRTY_DIR": "/from/env", "GIT_DIRTY_CHECK_STASH": "1"},
			check: func(t *testing.T, c Config) {
				if c.Depth != 7 || c.Dir != "/from/env" || !c.CheckStash {
					t.Errorf("depth=%d dir=%q stash=%v", c.Depth, c.Dir, c.CheckStash)
				}
			},
		},
		{
			name: "env exclude replaces the list",
			env:  map[string]string{"GIT_DIRTY_EXCLUDE": "a  b"},
			check: func(t *testing.T, c Config) {
				if !reflect.DeepEqual(c.Exclude, []string{"a", "b"}) {
					t.Errorf("exclude=%q", c.Exclude)
				}
			},
		},
		{
			name: "env main branches",
			env:  map[string]string{"GIT_DIRTY_MAIN_BRANCHES": "develop"},
			check: func(t *testing.T, c Config) {
				if !reflect.DeepEqual(c.MainBranches, []string{"develop"}) {
					t.Errorf("mainBranches=%q", c.MainBranches)
				}
			},
		},
		{
			name: "NO_COLOR beats file and GIT_DIRTY_COLOR",
			file: `{"color": true}`,
			env:  map[string]string{"GIT_DIRTY_COLOR": "1", "NO_COLOR": "1"},
			check: func(t *testing.T, c Config) {
				if c.Color {
					t.Error("color still on")
				}
			},
		},
		{
			name: "empty NO_COLOR is ignored",
			env:  map[string]string{"NO_COLOR": ""},
			check: func(t *testing.T, c Config) {
				if !c.Color {
					t.Error("color turned off by empty NO_COLOR")
				}
			},
		},
		{
			name: "VERBOSE env",
			env:  map[string]string{"VERBOSE": "1"},
			check: func(t *testing.T, c Config) {
				if !c.Verbose {
					t.Error("verbose not set")
				}
			},
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
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, cfg)
		})
	}
}

func TestLoad_Errors(t *testing.T) {
	tests := []struct {
		name string
		file string
		env  map[string]string
		want string
	}{
		{name: "unknown key names the key", file: `{"colour": true}`, want: `"colour"`},
		{name: "wrong type", file: `{"depth": "two"}`, want: "depth"},
		{name: "trailing data", file: `{} {}`, want: "unexpected data"},
		{name: "bad int env", env: map[string]string{"GIT_DIRTY_MAXDEPTH": "x"}, want: "GIT_DIRTY_MAXDEPTH"},
		{name: "bad bool env", env: map[string]string{"GIT_DIRTY_COLOR": "maybe"}, want: "GIT_DIRTY_COLOR"},
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
			_, err := Load()
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
