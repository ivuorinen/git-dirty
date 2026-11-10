package action

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// outputTail is how much of git's output a Result keeps: the end, where
// git writes its verdict.
const outputTail = 4 << 10

// waitDelay bounds the wait for git after a cancel, and for output pipes
// held open by a process git started, such as an ssh ControlPersist
// master.
const waitDelay = 5 * time.Second

// localEnv is every variable `git rev-parse --local-env-vars` prints, in
// git 2.39 through 2.56: the ones that tie git to one repository. An
// exported GIT_DIR would point every run at that repository instead of
// the one in the working directory, so Run removes them all.
var localEnv = []string{
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_GRAFT_FILE",
	"GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_REPLACE_REF_BASE",
	"GIT_PREFIX",
	"GIT_INTERNAL_SUPER_PREFIX",
	"GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
}

// Result is what one run of git left.
type Result struct {
	Output string // the last outputTail bytes of git's stdout and stderr
	Err    error  // nil when git exited 0
}

// LastLine returns the last non-empty line of the output, which is where
// git writes why it failed. It comes from git and the remote server
// (remote: lines), so a caller that displays it sanitizes it.
func (r Result) LastLine() string {
	lines := strings.FieldsFunc(r.Output, func(c rune) bool { return c == '\n' || c == '\r' })
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}

	return ""
}

// Run runs the action's git command in dir and waits for it to exit.
//
// dir is the working directory, for a bare repository too, so nothing
// taken from a repository reaches git's arguments, and git applies the
// user's safe.directory and safe.bareRepository settings to it. git
// starts without a terminal and with stdin on the null device, so neither
// git nor ssh can prompt on a terminal: a credential that needs typing
// fails with git's own message, unless an askpass program the user
// configured answers it. Canceling ctx interrupts git the way ctrl+c in a
// terminal does, so it removes its lock files.
func Run(ctx context.Context, dir string, k Kind) Result {
	//nolint:forbidigo,gosec // the TUI's fetch and pull actions run git by design (CLAUDE.md)
	cmd := exec.CommandContext(ctx, "git", k.args()...)
	cmd.Dir = dir
	cmd.Env = environ(os.Environ())
	out := &tail{}
	cmd.Stdout, cmd.Stderr = out, out
	cmd.WaitDelay = waitDelay
	detach(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		reap(cmd)
	}

	return Result{Output: string(out.buf), Err: err}
}

// environ returns env without git's repository-local variables, with
// GIT_TERMINAL_PROMPT=0 so git does not ask on a terminal for a username
// or password.
func environ(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !local(name) && !strings.EqualFold(name, "GIT_TERMINAL_PROMPT") {
			out = append(out, kv)
		}
	}

	return append(out, "GIT_TERMINAL_PROMPT=0")
}

// local reports whether name is one of localEnv. Windows ignores case in
// variable names, so the comparison does too.
func local(name string) bool {
	return slices.ContainsFunc(localEnv, func(l string) bool { return strings.EqualFold(name, l) })
}

// tail is a writer that keeps the last outputTail bytes written to it.
// exec.Cmd calls Write from one goroutine when Stdout and Stderr are the
// same tail, so it needs no lock.
type tail struct {
	buf []byte
}

// Write implements io.Writer. It reports all of p as written even when it
// keeps only the end, because os/exec's copy stops at a short write.
func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - outputTail; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}

	return len(p), nil
}
