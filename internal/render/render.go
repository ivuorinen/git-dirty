// Package render turns tree lines into styled text segments, and writes them
// as plain terminal output. The TUI styles the same segments with lipgloss,
// so both modes print identical content.
package render

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ivuorinen/git-dirty/internal/gitstat"
	"github.com/ivuorinen/git-dirty/internal/tree"
)

// Style is the semantic role of a segment.
type Style int

const (
	Plain  Style = iota
	Clean        // ✅ icon
	Dirty        // ❌ icon
	NotGit       // ⚠️ icon for directories and errors
	Status       // [MS?$]
	Branch       // branch names and ahead/behind counts
	Error        // error text
)

// Icons, as in the bash script.
const (
	IconClean  = "✅"
	IconDirty  = "❌"
	IconNotGit = "⚠️"
)

// Segment is a run of text with one style.
type Segment struct {
	Text  string
	Style Style
}

// Options controls line content.
type Options struct {
	ShowBranch   bool     // print branch names (counts are always printed)
	Truncate     bool     // print basenames; off prints each line's full path
	MainBranches []string // branch names whose name is not printed
}

// Line returns the segments of one tree line, prefix included.
func Line(l tree.Line, o Options) []Segment {
	n := l.Node
	segs := []Segment{{Text: l.Prefix}}
	name := n.Name
	if !o.Truncate {
		name = n.Path
	}
	name = Sanitize(name)

	if n.Repo == nil {
		return append(segs, Segment{IconNotGit, NotGit}, Segment{Text: " " + name})
	}
	r := n.Repo
	switch {
	case r.Err != nil:
		segs = append(segs, Segment{IconNotGit, NotGit})
	case r.Dirty():
		segs = append(segs, Segment{IconDirty, Dirty})
	default:
		segs = append(segs, Segment{IconClean, Clean})
	}
	segs = append(segs, Segment{Text: " " + name})
	if letters := statusLetters(r.Status); letters != "" {
		segs = append(segs, Segment{Text: " "}, Segment{"[" + letters + "]", Status})
	}
	if b := branchSegment(r, o); b != "" {
		segs = append(segs, Segment{Text: " "}, Segment{b, Branch})
	}
	if d := r.Default; d != nil {
		if l := d.Local; l != nil && !l.Gone && (l.Ahead > 0 || l.Behind > 0) {
			segs = append(segs, Segment{Text: " · "}, Segment{Sanitize(d.Name) + counts(l.Ahead, l.Behind, false), Branch})
		}
		if v := d.Versus; v != nil {
			segs = append(segs, Segment{Text: " · vs "}, Segment{Sanitize(d.Name) + counts(v.Ahead, v.Behind, true), Branch})
		}
	}
	if r.Err != nil {
		msg, _, _ := strings.Cut(r.Err.Error(), "\n")
		segs = append(segs, Segment{Text: " "}, Segment{"! " + Sanitize(msg), Error})
	}
	return segs
}

func statusLetters(s gitstat.Status) string {
	var b strings.Builder
	for _, l := range []struct {
		on bool
		c  byte
	}{{s.Modified, 'M'}, {s.Staged, 'S'}, {s.Untracked, '?'}, {s.Stashed, '$'}} {
		if l.on {
			b.WriteByte(l.c)
		}
	}
	return b.String()
}

// counts formats " ↑a ↓b"; zero counts are omitted unless always is set.
func counts(ahead, behind int, always bool) string {
	var s string
	if always || ahead > 0 {
		s += fmt.Sprintf(" ↑%d", ahead)
	}
	if always || behind > 0 {
		s += fmt.Sprintf(" ↓%d", behind)
	}
	return s
}

// branchSegment is "(name ↑a ↓b)" for a branch whose name is shown, the bare
// counts for a main branch (or with names off), and "" when nothing applies.
func branchSegment(r *gitstat.Result, o Options) string {
	h := r.Head
	if h.Label == "" {
		return ""
	}
	var tail string
	switch u := r.Upstream; {
	case u != nil && u.Gone:
		tail = ", upstream gone"
	case u != nil:
		tail = counts(u.Ahead, u.Behind, false)
	case !h.Detached() && !h.Unborn:
		tail = ", no upstream"
	}
	hideName := !o.ShowBranch || (!h.Detached() && slices.Contains(o.MainBranches, h.Branch))
	if hideName {
		if strings.HasPrefix(tail, ",") {
			return ""
		}
		return strings.TrimPrefix(tail, " ")
	}
	return "(" + Sanitize(h.Label) + tail + ")"
}

// Sanitize makes repository-controlled text safe to print to a terminal:
// control characters (C0, DEL, C1) and invalid UTF-8 bytes become \xNN
// escapes, so a directory or branch name cannot inject escape sequences.
func Sanitize(s string) string {
	clean := true
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unicode.IsControl(r):
			for _, c := range []byte(s[i : i+size]) {
				fmt.Fprintf(&b, `\x%02x`, c)
			}
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// ANSI colors per style, matching the bash script's choices.
var ansi = map[Style]string{
	Clean:  "32",
	Dirty:  "31",
	NotGit: "33",
	Status: "33",
	Branch: "35",
	Error:  "31",
}

// WriteSegments writes segments followed by a newline, colored when color is
// set.
func WriteSegments(w io.Writer, segs []Segment, color bool) error {
	var b strings.Builder
	for _, s := range segs {
		if code, ok := ansi[s.Style]; ok && color && s.Text != "" {
			b.WriteString("\x1b[" + code + "m" + s.Text + "\x1b[0m")
		} else {
			b.WriteString(s.Text)
		}
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

// Header returns the lines printed before the tree.
func Header(root string, o Options) []string {
	lines := []string{
		"Checking repositories in: " + Sanitize(root),
		"Legend: " + IconClean + " Clean repo | " + IconDirty + " Dirty repo | " + IconNotGit + " Directory / error",
	}
	if o.ShowBranch {
		lines = append(lines, "Showing non-standard branches (hiding "+strings.Join(o.MainBranches, " ")+")")
	}
	return append(lines, strings.Repeat("-", 57))
}

// Summary returns the line printed after the tree.
func Summary(repos int, elapsed time.Duration) string {
	noun := "repositories"
	if repos == 1 {
		noun = "repository"
	}
	return fmt.Sprintf("Found %d git %s in %s", repos, noun, FormatDuration(elapsed))
}

// FormatDuration prints seconds with two decimals under a minute, else
// minutes and seconds.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// WritePlain writes the whole report: header, tree, blank line, summary.
func WritePlain(w io.Writer, root string, lines []tree.Line, repos int, elapsed time.Duration, o Options, color bool) error {
	for _, h := range Header(root, o) {
		if _, err := fmt.Fprintln(w, h); err != nil {
			return err
		}
	}
	for _, l := range lines {
		if err := WriteSegments(w, Line(l, o), color); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "\n%s\n", Summary(repos, elapsed))
	return err
}
