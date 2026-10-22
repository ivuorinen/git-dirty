package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// configFixture exercises every construct the parser handles.
const configFixture = "\xef\xbb\xbf# leading comment\n" +
	"[core]\n" +
	"\tfileMode = false\n" +
	"\tbare\n" +
	"\tIgnoreCase = TRUE ; trailing comment\n" +
	"\texcludesFile = \"~/my ignore\" # quoted with a space\n" +
	"[Remote \"Origin\"]\n" +
	"\turl = https://example.invalid/r.git\n" +
	"\tfetch = +refs/heads/*:refs/remotes/Origin/*\n" +
	"\tfetch = +refs/tags/*:refs/tags/*\n" +
	"[branch \"feat/x \\\"q\\\"\"]\n" +
	"\tremote = Origin\n" +
	"\tmerge = refs/heads/feat/x\n" +
	"[Section.Legacy]\n" +
	"\tkey = legacy value\n" +
	"[esc]\n" +
	"\ttabs = a\\tb\\nc\\\\d\\\"e\n" +
	"\tcont = first \\\n  second\n" +
	"\tinner = a  \t b\n" +
	"\tmixed = x \" y ; not a comment \" z # comment \"\n" +
	"\tcrlf = value\r\n" +
	"\tempty =\n" +
	"\tsemi = a;b\n" +
	"[core]\n" +
	"\tfileMode = true\n"

// Sections of configFixture.
const (
	secCore = "core"
	secEsc  = "esc"
)

// configKey names one key of configFixture for git and for Config.
type configKey struct {
	gitKey                   string
	section, subsection, key string
}

// gitConfigGet returns git's values for key (NUL-separated output, so
// whitespace and newlines inside values survive).
func gitConfigGet(t *testing.T, file, key string, all bool) ([]string, bool) {
	t.Helper()
	args := []string{"config", "--file", file, "--null", "--get", key}
	if all {
		args[4] = "--get-all"
	}
	// runCode trims the output, which would hide whitespace bugs.
	out, err := exec.Command("git", args...).Output() //nolint:gosec // G204: git is the test oracle
	if err != nil {
		return nil, false
	}
	vals := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")

	return vals, true
}

// gitAtLeast reports whether the git on PATH is major.minor or newer.
func gitAtLeast(t *testing.T, major, minor int) bool {
	t.Helper()
	v := strings.TrimPrefix(run(t, "", "version"), "git version ")
	var ma, mi int
	if _, err := fmt.Sscanf(v, "%d.%d", &ma, &mi); err != nil {
		t.Fatalf("parse git version %q: %v", v, err)
	}

	return ma > major || ma == major && mi >= minor
}

// checkConfigGet compares Get for one key with git config --get.
func checkConfigGet(t *testing.T, cfg *Config, file string, k configKey) {
	t.Helper()
	if k.gitKey == "esc.inner" && !gitAtLeast(t, 2, 45) {
		t.Logf("skipping %s: git before 2.45 turns each whitespace character into a space", k.gitKey)

		return
	}
	want, ok := gitConfigGet(t, file, k.gitKey, false)
	if !ok {
		t.Fatalf("git has no %s", k.gitKey)
	}
	got, ok := cfg.Get(k.section, k.subsection, k.key)
	if !ok || got != want[0] {
		t.Errorf("%s = %q (found %v), git says %q", k.gitKey, got, ok, want[0])
	}
}

// checkConfigBool checks Bool on configFixture's boolean keys.
func checkConfigBool(t *testing.T, cfg *Config) {
	t.Helper()
	if !cfg.Bool(secCore, "", "bare", false) || !cfg.Bool(secCore, "", "filemode", false) ||
		!cfg.Bool(secCore, "", "ignorecase", false) {
		t.Error("Bool: bare key, last-wins, or TRUE parsed wrong")
	}
	if cfg.Bool(secEsc, "", "empty", true) {
		t.Error(`Bool: "key =" (empty) should be false, as in git`)
	}
	if !cfg.Bool(secCore, "", "missing", true) {
		t.Error("Bool: absent key should give the default")
	}
}

func TestParseConfig_MatchesGit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte(configFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadConfigFile(file)
	if err != nil {
		t.Fatal(err)
	}
	keys := []configKey{
		{"core.filemode", secCore, "", "filemode"},
		{"CORE.FileMode", "CORE", "", "FileMode"},
		{"core.bare", secCore, "", "bare"},
		{"core.ignorecase", secCore, "", "ignorecase"},
		{"core.excludesfile", secCore, "", "excludesFile"},
		{"remote.Origin.url", "remote", "Origin", "url"},
		{"branch.feat/x \"q\".remote", "branch", "feat/x \"q\"", "remote"},
		{"branch.feat/x \"q\".merge", "branch", "feat/x \"q\"", "merge"},
		{"section.legacy.key", "section", "legacy", "key"},
		{"esc.tabs", secEsc, "", "tabs"},
		{"esc.cont", secEsc, "", "cont"},
		{"esc.inner", secEsc, "", "inner"},
		{"esc.mixed", secEsc, "", "mixed"},
		{"esc.crlf", secEsc, "", "crlf"},
		{"esc.empty", secEsc, "", "empty"},
		{"esc.semi", secEsc, "", "semi"},
	}
	for _, k := range keys {
		checkConfigGet(t, cfg, file, k)
	}
	want, _ := gitConfigGet(t, file, "remote.Origin.fetch", true)
	if got := cfg.GetAll("remote", "Origin", "fetch"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("GetAll fetch = %q, git says %q", got, want)
	}
	if _, ok := cfg.Get("remote", "origin", "url"); ok {
		t.Error("subsection lookup is case-insensitive; git's is not")
	}
	checkConfigBool(t, cfg)
}

func TestParseConfig_Errors(t *testing.T) {
	bad := map[string]string{
		"unclosed section":   "[core\n\tbare = true\n",
		"unterminated quote": "[core]\n\tx = \"open\n",
		"bad escape":         "[core]\n\tx = a\\qb\n",
		"bad key char":       "[core]\n\t1x = 1\n",
		"unterminated sub":   "[remote \"origin]\n",
	}
	dir := t.TempDir()
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(body))
			if err == nil || !strings.Contains(err.Error(), "line") {
				t.Errorf("err = %v, want a bad config line error", err)
			}
			file := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, code := runCode(t, "", nil, "config", "--file", file, "--list"); code == 0 {
				t.Errorf("git accepted the fixture, so the test is wrong: %s", out)
			}
		})
	}
}

func TestReadConfigFile_Missing(t *testing.T) {
	cfg, err := ReadConfigFile(filepath.Join(t.TempDir(), "nope"))
	if err != nil || cfg == nil {
		t.Fatalf("missing file: %v, %v", cfg, err)
	}
	if _, ok := cfg.Get(secCore, "", "bare"); ok {
		t.Error("empty config has a key")
	}
}
