package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
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

// gitConfigOutput runs git config on file and returns its untrimmed stdout.
func gitConfigOutput(file string, args ...string) ([]byte, error) {
	args = append([]string{"config", "--file", file}, args...)

	return exec.Command("git", args...).Output() //nolint:gosec // G204: git is the test oracle
}

// gitConfigList returns what git reads from file, one "key=value" per entry
// and "key" alone for a key without "=", or ok false when git refuses it.
func gitConfigList(t *testing.T, file string) (entries []string, ok bool) {
	t.Helper()
	out, err := gitConfigOutput(file, "--null", "--list")

	if err != nil {
		return nil, false
	}
	for _, e := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if e != "" {
			entries = append(entries, strings.Replace(e, "\n", "=", 1))
		}
	}

	return entries, true
}

// listed renders cfg's entries as gitConfigList does.
func listed(cfg *Config) []string {
	out := make([]string, 0, len(cfg.entries))
	for _, e := range cfg.entries {
		name := e.name
		if !e.noValue {
			name += "=" + e.value
		}
		out = append(out, name)
	}

	return out
}

// TestParseConfig_GrammarMatchesGit runs each corner of the grammar through
// git: the same entries when git accepts the file, an error when it refuses.
// Whitespace inside a value is left out: git 2.45 changed how it reads that,
// and TestParseConfig_MatchesGit covers it.
func TestParseConfig_GrammarMatchesGit(t *testing.T) {
	for _, in := range []string{
		"[a]\n  spaced = 1\n",
		"; first\n[a]\nk = 1\n",
		"# no newline",
		"[a]\nk",
		"[a]\nk ",
		"[a]\nk # c\n",
		"[a]\nk ; c\n",
		"[a]\nk = v\\",
		"[ \"b\"]\nk = 1\n",
		"\r\n",
		"\r",
		"[a]\n\rk = 1\n",
		"[a]\nk = v\x0c\n",
		"[a]\nk = \x0bv\n",
		"[a.]\nk = 1\n",
		"[a \"\"]\nk = 1\n",
		"[a\r\"b\"]\nk = 1\n",
		"[a \r\"b\"]\nk = 1\n",
		"[a]\nk\t= v\n",
		"[a]\nk x\n",
		"[a]\nk = v",
		"[a]\nk = a\\bc\n",
		"[a]\nk = a\\xc\n",
		"[a]\nk = \"open\n",
		"[a]\nmy-key1 = 1\n",
		"[a]\nk_x = 1\n",
		"[a]\nk{ = 1\n",
		"[a]\n1k = 1\n",
		"[a-b.c]\nk = 1\n",
		"[a",
		"[a\n",
		"[a_b]\nk = 1\n",
		"[]\nk = 1\n",
		"[a\t\"b\"]\nk = 1\n",
		"[a  \"b\"]\nk = 1\n",
		"[a \t\"b\"]\nk = 1\n",
		"[a b]\nk = 1\n",
		"[a \"b\"",
		"[a \"b\"x]\n",
		"[a \"b\n",
		"[a \"b",
		"[a \"",
		"[a ",
		"[a \n",
		"[a \"b\\",
		"[a \"b\\\n",
		"[a \"b\\\"c\"]\nk = 1\n",
		"[a]\nk = v\r",
		"[a]\nk = v\r \n",
		"[a]\nk = \x01v\n",
		"k = 1\n",
		"!\n",
	} {
		file := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(file, []byte(in), 0o600); err != nil {
			t.Fatal(err)
		}
		want, gitOK := gitConfigList(t, file)
		cfg, err := ParseConfig([]byte(in))
		switch {
		case gitOK != (err == nil):
			t.Errorf("%q: err = %v, git accepts it: %v", in, err, gitOK)
		case gitOK && !slices.Equal(listed(cfg), want):
			t.Errorf("%q: entries %q, git says %q", in, listed(cfg), want)
		}
	}
}

// TestConfigBool_MatchesGit compares every boolean spelling with git's
// --type=bool, which refuses what Bool answers with the default for.
func TestConfigBool_MatchesGit(t *testing.T) {
	for _, v := range []string{
		"yes", "on", "1", "tRuE", "TRUE", "no", "off", "0", "false", "Off", "", "maybe",
		"2", "-1", "+3", "0x10", "010", "00", "08", "1k", "2K", "1m", "1g", "0k", "2g", "k",
		"99999999999", "1_000", " yes ", " 2", "2 ", "1x",
	} {
		file := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(file, []byte("[a]\n\tk = \""+v+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := gitConfigOutput(file, "--type=bool", "--get", "a.k")

		cfg, perr := ReadConfigFile(file)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, def := range []bool{false, true} {
			want := def
			if err == nil {
				want, _ = strconv.ParseBool(strings.TrimSpace(string(out)))
			}
			if got := cfg.Bool("a", "", "k", def); got != want {
				t.Errorf("%q with default %v: Bool = %v, git says %q (%v)", v, def, got, out, err)
			}
		}
	}
}

// An explicitly empty subsection is its own section: git does not read
// core.bare from [core ""].
func TestConfig_EmptySubsection(t *testing.T) {
	for _, body := range []string{"[core \"\"]\n\tbare = true\n", "[core.]\n\tbare = true\n"} {
		file := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := gitConfigGet(t, file, "core.bare", false); ok {
			t.Fatalf("fixture: git reads core.bare from %q", body)
		}
		cfg, err := ReadConfigFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Bool(secCore, "", "bare", false) || len(cfg.Keys(secCore, "")) != 0 {
			t.Errorf("%q: read as [core]", body)
		}
	}
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

// TestReadConfigFile_NullDevice: git reads the null device as an empty
// config (GIT_CONFIG_GLOBAL=/dev/null is its documented way to skip the
// global config), so the refusal of devices must not reach it.
func TestReadConfigFile_NullDevice(t *testing.T) {
	cfg, err := ReadConfigFile(os.DevNull)
	if err != nil || cfg == nil {
		t.Fatalf("%s: %v, %v", os.DevNull, cfg, err)
	}
	if _, ok := cfg.Get(secCore, "", "bare"); ok {
		t.Error("the null device's config has a key")
	}
}
