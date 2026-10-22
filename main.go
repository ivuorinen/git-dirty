// Command git-dirty recursively lists git repositories as a tree with their
// branch, dirty state and ahead/behind counts.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ivuorinen/git-dirty/internal/config"
	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/render"
	"github.com/ivuorinen/git-dirty/internal/scan"
	"github.com/ivuorinen/git-dirty/internal/tree"
	"github.com/ivuorinen/git-dirty/internal/tui"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitOK      = 0
	exitError   = 1
	exitUsage   = 2
	exitSignal  = 130
	programName = "git-dirty"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", programName, err)
		return exitError
	}
	opts, err := parseArgs(args, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\nRun '%s -h' for usage.\n", programName, err, programName)
		return exitUsage
	}
	switch {
	case opts.help:
		writeHelp(stdout, cfg)
		return exitOK
	case opts.version:
		fmt.Fprintln(stdout, version)
		return exitOK
	}
	cfg = opts.cfg
	if err := cfg.Normalize(); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", programName, err)
		return exitError
	}
	if fi, err := os.Stat(cfg.Dir); errors.Is(err, fs.ErrNotExist) {
		// The OS wording differs (Windows: "The system cannot find the file
		// specified"), so a missing directory gets one message everywhere.
		fmt.Fprintf(stderr, "%s: %s: no such file or directory\n", programName, cfg.Dir)
		return exitError
	} else if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", programName, err)
		return exitError
	} else if !fi.IsDir() {
		fmt.Fprintf(stderr, "%s: %s: not a directory\n", programName, cfg.Dir)
		return exitError
	}
	ex, err := gitstat.LoadExcludes()
	if err != nil {
		fmt.Fprintf(stderr, "%s: git ignore configuration: %v\n", programName, err)
		return exitError
	}
	term := os.Getenv("TERM")
	if useTUI(cfg.Interactive, term, isTerminal(stdin), isTerminal(stdout)) {
		err := tui.Run(ctx, tui.Config{
			Root:   cfg.Dir,
			Scan:   scanOptions(cfg, ex),
			Render: renderOptions(cfg),
			Filter: tree.Filter{Verbose: cfg.Verbose, Depth: cfg.Depth},
			Color:  cfg.Color,
		}, stdin, stdout)
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(stderr, "%s: %v\n", programName, err)
			return exitError
		}
		if ctx.Err() != nil {
			return exitSignal
		}
		return exitOK
	}
	return runPlain(ctx, cfg, scanOptions(cfg, ex), stdout, stderr)
}

// useTUI picks the interactive view: wanted (not --plain), both stdin and
// stdout are terminals, and the terminal can draw it.
func useTUI(interactive bool, term string, stdinTTY, stdoutTTY bool) bool {
	return interactive && stdinTTY && stdoutTTY && term != "dumb"
}

func scanOptions(cfg config.Config, ex *gitstat.Excludes) scan.Options {
	return scan.Options{
		Root:     cfg.Dir,
		MaxDepth: cfg.MaxDepth,
		Exclude:  cfg.Exclude,
		Inspect: gitstat.Options{
			CheckUntracked: cfg.CheckUntracked,
			CheckStash:     cfg.CheckStash,
			MainBranches:   cfg.MainBranches,
			Excludes:       ex,
		},
	}
}

func renderOptions(cfg config.Config) render.Options {
	return render.Options{ShowBranch: cfg.ShowBranch, Truncate: cfg.Truncate, MainBranches: cfg.MainBranches}
}

// runPlain scans, then prints the sorted report to stdout. A repository
// counter runs on stderr while scanning when stderr is a terminal.
func runPlain(ctx context.Context, cfg config.Config, so scan.Options, stdout, stderr io.Writer) int {
	start := time.Now()
	t := tree.New(cfg.Dir)
	progress := isTerminal(stderr)
	found := 0
	clearLine := func() {
		if progress {
			fmt.Fprint(stderr, "\r\x1b[K")
		}
	}
	for it := range scan.Run(ctx, so) {
		switch {
		case it.Kind == scan.Warning && cfg.Verbose:
			clearLine()
			fmt.Fprintf(stderr, "%s: %s: %s\n", programName, render.Sanitize(it.Path), it.Message)
		case it.Kind == scan.Repo:
			found++
			if cfg.Verbose && it.Result.Err != nil {
				clearLine()
				fmt.Fprintf(stderr, "%s: %s: %s\n", programName, render.Sanitize(it.Path), render.Sanitize(it.Result.Err.Error()))
			}
			if progress {
				fmt.Fprintf(stderr, "\rFound %d git repositories...", found)
			}
		}
		t.Add(it)
	}
	clearLine()
	if ctx.Err() != nil {
		fmt.Fprintf(stderr, "%s: interrupted\n", programName)
		return exitSignal
	}
	filter := tree.Filter{Verbose: cfg.Verbose, Depth: cfg.Depth}
	color := cfg.Color && isTerminal(stdout) && os.Getenv("TERM") != "dumb"
	if err := render.WritePlain(stdout, cfg.Dir, t.Lines(filter), t.Repos(), time.Since(start), renderOptions(cfg), color); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", programName, err)
		return exitError
	}
	return exitOK
}

// isTerminal reports whether w is a character device such as a terminal.
func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// options is the result of parsing the command line on top of the loaded
// config.
type options struct {
	cfg     config.Config
	help    bool
	version bool
}

// Short flags that take no value and short flags that take one, used to
// expand getopts-style clusters such as -vac and -d3.
const (
	shortBools  = "hvpactb"
	shortValues = "dem"
)

// longNames are the multi-letter flags; "-plain" must not be read as -p -l ...
var longNames = map[string]bool{"plain": true, "version": true, "help": true}

// expandShortFlags rewrites getopts-style clusters into one flag per argument:
// -vac becomes -v -a -c, and -d3 becomes -d 3 (a value-taking letter consumes
// the rest of its cluster). The argument after a bare value flag is left as
// is, so `-e -x` keeps "-x" as the value. Everything after "--" is untouched.
func expandShortFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return append(out, args[i:]...)
		}
		if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
			out = append(out, arg)
			continue
		}
		name, _, _ := strings.Cut(arg[1:], "=")
		if longNames[name] {
			out = append(out, arg)
			continue
		}
		expanded, takesNext, ok := expandCluster(arg[1:])
		if !ok {
			out = append(out, arg) // let flag report the unknown letter
			continue
		}
		out = append(out, expanded...)
		if takesNext && i+1 < len(args) {
			i++
			out = append(out, args[i])
		}
	}
	return out
}

// expandCluster splits the letters after "-". takesNext reports that the last
// letter is a value flag whose value is the next argument.
func expandCluster(letters string) (expanded []string, takesNext, ok bool) {
	for i, ch := range letters {
		switch {
		case strings.ContainsRune(shortBools, ch):
			expanded = append(expanded, "-"+string(ch))
		case strings.ContainsRune(shortValues, ch):
			expanded = append(expanded, "-"+string(ch))
			rest := letters[i+1:]
			if rest == "" {
				return expanded, true, true
			}
			return append(expanded, rest), false, true
		default:
			return nil, false, false
		}
	}
	return expanded, false, true
}

// excludeValue appends comma-separated names to the exclude list. Empty items
// are skipped, as the bash script's word splitting did.
type excludeValue struct{ list *[]string }

func (e excludeValue) String() string { return "" }

func (e excludeValue) Set(s string) error {
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*e.list = append(*e.list, item)
		}
	}
	return nil
}

func parseArgs(args []string, cfg config.Config) (options, error) {
	opts := options{cfg: cfg}
	c := &opts.cfg
	// Copy the slice so appends never write into the caller's backing array.
	c.Exclude = append([]string(nil), c.Exclude...)

	fs := flag.NewFlagSet(programName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.BoolVar(&opts.help, "h", false, "")
	fs.BoolVar(&opts.help, "help", false, "")
	fs.BoolVar(&c.Verbose, "v", c.Verbose, "")
	fs.Bool("p", false, "") // accepted for compatibility; always parallel
	fs.BoolFunc("a", "", func(string) error {
		c.CheckStash, c.CheckUntracked = true, true
		return nil
	})
	fs.BoolFunc("c", "", func(string) error { c.Color = !c.Color; return nil })
	fs.BoolFunc("t", "", func(string) error { c.Truncate = !c.Truncate; return nil })
	fs.BoolFunc("b", "", func(string) error { c.ShowBranch = !c.ShowBranch; return nil })
	fs.IntVar(&c.Depth, "d", c.Depth, "")
	fs.IntVar(&c.MaxDepth, "m", c.MaxDepth, "")
	fs.Var(excludeValue{&c.Exclude}, "e", "")
	fs.BoolFunc("plain", "", func(string) error { c.Interactive = false; return nil })
	fs.BoolVar(&opts.version, "version", false, "")

	args = expandShortFlags(args)
	var rest []string
	for i, a := range args {
		if a == "--" {
			args, rest = args[:i], args[i+1:]
			break
		}
	}

	// flag stops at the first non-flag argument, so keep parsing after each
	// positional: `git-dirty ~/Code -v` must still see -v.
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return opts, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	positional = append(positional, rest...)

	switch len(positional) {
	case 0:
	case 1:
		c.Dir = positional[0]
	default:
		return opts, fmt.Errorf("expected at most one directory, got %d: %s",
			len(positional), strings.Join(positional, " "))
	}
	return opts, nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func writeHelp(w io.Writer, cfg config.Config) {
	path, err := config.Path()
	if err != nil {
		path = "(unavailable: " + err.Error() + ")"
	}
	// One quoted string per line keeps the text's two-space indents out of
	// the source indentation, which .editorconfig requires to be tabs.
	const help = "Usage: %[1]s [OPTIONS] [DIRECTORY]\n" +
		"\n" +
		"Recursively check git repository status\n" +
		"\n" +
		"Options:\n" +
		"  -h           Show this help message and exit\n" +
		"  -d NUM       With -v, list non-git directories down to this depth (default: %[2]d)\n" +
		"  -p           Accepted for compatibility; scanning is always parallel\n" +
		"  -v           Verbose: list every directory, log skipped ones to stderr\n" +
		"  -a           Show all status details (stash, untracked)\n" +
		"  -e PATTERNS  Additional directory names to exclude (comma separated)\n" +
		"  -m NUM       Maximum recursion depth (default: %[3]d)\n" +
		"  -c           Toggle colorized output (currently: %[4]s)\n" +
		"  -t           Toggle path truncation (currently: %[5]s)\n" +
		"  -b           Toggle branch name display (currently: %[6]s)\n" +
		"  --plain      Print plain output instead of the interactive view\n" +
		"  --version    Print the version and exit\n" +
		"\n" +
		"Status indicators:\n" +
		"  M = Modified files\n" +
		"  S = Staged changes\n" +
		"  ? = Untracked files (off with GIT_DIRTY_CHECK_UNTRACKED=0)\n" +
		"  $ = Stashed changes (with -a)\n" +
		"  ↑N ↓N = Commits ahead of / behind the upstream\n" +
		"\n" +
		"Configuration: %[7]s\n" +
		"and GIT_DIRTY_* environment variables; flags override both.\n" +
		"\n" +
		"Example:\n" +
		"  %[1]s ~/Projects\n" +
		"  %[1]s -d 3 -e 'build,dist' ~/Code\n"
	fmt.Fprintf(w, help, programName, cfg.Depth, cfg.MaxDepth, onOff(cfg.Color), onOff(cfg.Truncate), onOff(cfg.ShowBranch), path)
}
