// Package config resolves git-dirty's settings from defaults, the JSON config
// file and GIT_DIRTY_* environment variables. Command-line flags are applied
// on top by main.
package config

import (
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

// Load returns the defaults overlaid with the config file (when it exists)
// and then the environment.
func Load() (Config, error) {
	cfg := Defaults()
	path, err := Path()
	if err != nil {
		return cfg, err
	}
	if err := loadFile(path, &cfg); err != nil {
		return cfg, err
	}
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}

	return cfg, nil
}

func loadFile(path string, cfg *Config) (err error) {
	f, err := os.Open(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: unexpected data after the JSON object", path)
	}

	return nil
}

func applyEnv(cfg *Config) error {
	if v, ok := os.LookupEnv("GIT_DIRTY_DIR"); ok {
		cfg.Dir = v
	}
	ints := []struct {
		name string
		dst  *int
	}{
		{"GIT_DIRTY_DEPTH", &cfg.Depth},
		{"GIT_DIRTY_MAXDEPTH", &cfg.MaxDepth},
	}
	for _, e := range ints {
		if err := envInt(e.name, e.dst); err != nil {
			return err
		}
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
			return err
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

	return nil
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
