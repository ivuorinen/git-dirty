// Package config resolves git-dirty's settings from defaults, the JSON config
// file and GIT_DIRTY_* environment variables. Command-line flags are applied
// on top by main.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds every setting. JSON keys are the file format; the matching
// environment variable is noted on each field.
type Config struct {
	Dir            string   `json:"dir"`            // GIT_DIRTY_DIR
	Depth          int      `json:"depth"`          // GIT_DIRTY_DEPTH
	MaxDepth       int      `json:"maxDepth"`       // GIT_DIRTY_MAXDEPTH
	CheckStash     bool     `json:"checkStash"`     // GIT_DIRTY_CHECK_STASH
	CheckUntracked bool     `json:"checkUntracked"` // GIT_DIRTY_CHECK_UNTRACKED
	Exclude        []string `json:"exclude"`        // GIT_DIRTY_EXCLUDE (space separated)
	Color          bool     `json:"color"`          // GIT_DIRTY_COLOR, NO_COLOR
	Truncate       bool     `json:"truncate"`       // GIT_DIRTY_TRUNCATE
	ShowBranch     bool     `json:"showBranch"`     // GIT_DIRTY_SHOW_BRANCH
	MainBranches   []string `json:"mainBranches"`   // GIT_DIRTY_MAIN_BRANCHES (space separated)
	Interactive    bool     `json:"interactive"`    // --plain turns it off
	Verbose        bool     `json:"verbose"`        // VERBOSE
}

// Defaults returns the built-in settings, matching the bash script's.
func Defaults() Config {
	return Config{
		Dir:            "~/Code",
		Depth:          5,
		MaxDepth:       15,
		CheckStash:     false,
		CheckUntracked: true,
		Exclude:        []string{"node_modules", "vendor", ".cache", "build", "dist", ".tests", ".test"},
		Color:          true,
		Truncate:       true,
		ShowBranch:     true,
		MainBranches:   []string{"main", "master", "trunk"},
		Interactive:    true,
	}
}

// Path returns the config file location:
// $XDG_CONFIG_HOME/git-dirty/config.json, else ~/.config/git-dirty/config.json.
func Path() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "git-dirty", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".config", "git-dirty", "config.json"), nil
}

// Load returns the defaults overlaid with the config file at path (when it
// exists) and then the environment.
func Load(path string) (Config, error) {
	cfg, err := LoadFile(path)
	if err != nil {
		return cfg, err
	}

	return ApplyEnv(cfg)
}

// LoadFile returns the defaults overlaid with the config file at path, or
// the defaults when it does not exist: the file layer, without the
// environment.
func LoadFile(path string) (cfg Config, err error) {
	cfg = Defaults()
	f, err := os.Open(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("%s: unexpected data after the JSON object", path)
	}

	return cfg, nil
}

// ApplyEnv returns cfg overlaid with the GIT_DIRTY_* variables, VERBOSE and
// NO_COLOR.
func ApplyEnv(cfg Config) (Config, error) {
	if v, ok := os.LookupEnv("GIT_DIRTY_DIR"); ok {
		cfg.Dir = v
	}
	if err := envInt("GIT_DIRTY_DEPTH", &cfg.Depth); err != nil {
		return cfg, err
	}
	if err := envInt("GIT_DIRTY_MAXDEPTH", &cfg.MaxDepth); err != nil {
		return cfg, err
	}
	bools := []struct {
		name string
		dst  *bool
	}{
		{"GIT_DIRTY_CHECK_STASH", &cfg.CheckStash},
		{"GIT_DIRTY_CHECK_UNTRACKED", &cfg.CheckUntracked},
		{"GIT_DIRTY_COLOR", &cfg.Color},
		{"GIT_DIRTY_TRUNCATE", &cfg.Truncate},
		{"GIT_DIRTY_SHOW_BRANCH", &cfg.ShowBranch},
		{"VERBOSE", &cfg.Verbose},
	}
	for _, e := range bools {
		if err := envBool(e.name, e.dst); err != nil {
			return cfg, err
		}
	}
	if v, ok := os.LookupEnv("GIT_DIRTY_EXCLUDE"); ok {
		cfg.Exclude = strings.Fields(v)
	}
	if v, ok := os.LookupEnv("GIT_DIRTY_MAIN_BRANCHES"); ok {
		cfg.MainBranches = strings.Fields(v)
	}
	// https://no-color.org: present and non-empty disables color.
	if os.Getenv("NO_COLOR") != "" {
		cfg.Color = false
	}

	return cfg, nil
}

// Save writes cfg to the config file at path, keeping only the settings that
// differ from Defaults(), so a later change of a default still reaches the
// user. A symlinked file is followed and its target replaced, as dotfile
// managers link it. The file is written beside its target and renamed over
// it, so a failed save leaves the old file whole; an existing file keeps its
// mode, a new one is 0600, and a missing directory is created.
func Save(path string, cfg Config) error {
	data, err := nonDefault(cfg)
	if err != nil {
		return err
	}
	target, err := resolve(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(target); err == nil {
		mode = fi.Mode().Perm()
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return replace(target, data, mode)
}

// nonDefault returns cfg as a JSON object holding only the keys whose values
// differ from Defaults().
func nonDefault(cfg Config) ([]byte, error) {
	keys, err := jsonFields(cfg)
	if err != nil {
		return nil, err
	}
	defaults, err := jsonFields(Defaults())
	if err != nil {
		return nil, err
	}
	for k, v := range keys {
		if bytes.Equal(v, defaults[k]) {
			delete(keys, k)
		}
	}
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}

// jsonFields returns each JSON key of cfg with its compact encoding.
func jsonFields(cfg Config) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage

	return fields, json.Unmarshal(data, &fields)
}

// resolve returns the file a save replaces: path, or the target of path
// when it is a symlink. The target need not exist: a dotfile manager can
// link the config before the file is first written, which startup reads as
// the defaults, and the save then creates it. A symlink cycle is an error
// from EvalSymlinks, so the recursion ends.
func resolve(path string) (string, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return path, nil // created at path
	case err != nil:
		return "", err
	case fi.Mode()&os.ModeSymlink == 0:
		return path, nil
	}
	target, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, os.ErrNotExist) {
		return target, err
	}
	link, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(link) {
		link = filepath.Join(filepath.Dir(path), link)
	}

	return resolve(link)
}

// replace writes data to a temporary file in target's directory and renames
// it over target. The temporary file is removed on every error.
func replace(target string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			err = errors.Join(err, os.Remove(tmp.Name()))
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), target)
}

// envInt sets *dst from the variable name when it is set.
func envInt(name string, dst *int) error {
	v, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s: not a number: %q", name, v)
	}
	*dst = n

	return nil
}

// envBool sets *dst from the variable name when it is set.
func envBool(name string, dst *bool) error {
	v, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("%s: not a boolean (0/1/true/false): %q", name, v)
	}
	*dst = b

	return nil
}

// SplitList splits a comma-separated list, as -e and the settings page take
// it: blanks around an item and empty items are dropped, as the bash script's
// word splitting did. No items is an empty list, not nil, so a saved config
// holds [] rather than null.
func SplitList(s string) []string {
	list := []string{}
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}

	return list
}

// Normalize expands a leading ~ in Dir and validates the settings. main calls
// it after applying flags.
func (c *Config) Normalize() error {
	if c.Dir == "" {
		return errors.New("dir: must not be empty")
	}
	if c.Dir == "~" || strings.HasPrefix(c.Dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("dir: %w", err)
		}
		c.Dir = filepath.Join(home, strings.TrimPrefix(c.Dir, "~"))
	}
	if c.Depth < 0 {
		return fmt.Errorf("depth: must be 0 or more, got %d", c.Depth)
	}
	if c.MaxDepth < 1 {
		return fmt.Errorf("maxDepth: must be 1 or more, got %d", c.MaxDepth)
	}
	for _, e := range c.Exclude {
		if strings.TrimSpace(e) == "" {
			return errors.New("exclude: empty pattern")
		}
	}

	return nil
}
