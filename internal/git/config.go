package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Config is a parsed git config file. Section and key names are
// case-insensitive; subsection names are case-sensitive, except in the
// legacy [section.subsection] form, which git lowercases. Include
// directives are not followed.
type Config struct {
	entries []configEntry
}

type configEntry struct {
	name    string // see variableName; git compares variables by this string too
	value   string
	noValue bool // "key" alone, which git reads as true
}

// variableName is the variable section[.subsection].key as git spells it, with
// the section and key lowercased.
func variableName(section, subsection, key string) string {
	section = strings.ToLower(section)
	if subsection != "" {
		section += "." + subsection
	}

	return section + "." + strings.ToLower(key)
}

// maxConfigFile caps a config file that ReadConfigFile reads; a repository's
// config comes from the repository, which may be hostile.
const maxConfigFile = 16 << 20

// ReadConfigFile parses the file at path; a missing file is an empty config,
// and so is the null device, as git reads GIT_CONFIG_GLOBAL=/dev/null.
// Otherwise only a regular file of at most maxConfigFile bytes is read: a
// config may be a symlink (global configs often are), but one leading to a
// device or a FIFO is refused by OpenRegular, so it cannot block or flood
// the scan.
func ReadConfigFile(path string) (*Config, error) {
	if path == os.DevNull || path == "/dev/null" { // Git for Windows accepts /dev/null too
		return &Config{}, nil
	}
	// Open passes the config under the repository's commondir, a path read
	// from the repository; git reads the shared config there too.
	f, err := OpenRegular(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFile+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(data) > maxConfigFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, maxConfigFile)
	}
	c, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return c, nil
}

// Get returns the last value of section[.subsection].key. ok is false when
// the key is absent; a key without "=" has the value "".
func (c *Config) Get(section, subsection, key string) (value string, ok bool) {
	name := variableName(section, subsection, key)
	for i := len(c.entries) - 1; i >= 0; i-- {
		if e := c.entries[i]; e.name == name {
			return e.value, true
		}
	}

	return "", false
}

// GetAll returns every value of a multi-valued key, in file order.
func (c *Config) GetAll(section, subsection, key string) []string {
	var out []string
	name := variableName(section, subsection, key)
	for _, e := range c.entries {
		if e.name == name {
			out = append(out, e.value)
		}
	}

	return out
}

// Bool returns a boolean key the way git_parse_maybe_bool does (true/yes/on,
// false/no/off, an integer is true unless 0, a bare key is true); def when
// absent or unparsable.
func (c *Config) Bool(section, subsection, key string, def bool) bool {
	name := variableName(section, subsection, key)
	for i := len(c.entries) - 1; i >= 0; i-- {
		e := c.entries[i]
		if e.name != name {
			continue
		}
		if e.noValue {
			return true
		}
		switch strings.ToLower(e.value) {
		case "true", "yes", "on":
			return true
		case "false", "no", "off", "":
			return false
		}
		if v, ok := intBool(e.value); ok {
			return v
		}

		return def
	}

	return def
}

// configInt is what git_parse_int reads: strtoimax's base 0, then a unit.
var configInt = regexp.MustCompile(`^[ \t\n\v\f\r]*([+-]?(?:0[xX][0-9a-fA-F]+|[0-9]+))([kKmMgG]?)$`)

// intBool reads s as an integer and reports whether it is not 0; ok is false
// unless, with its unit, it fits a C int.
func intBool(s string) (v, ok bool) {
	m := configInt.FindStringSubmatch(s)
	if m == nil {
		return false, false
	}
	n, err := strconv.ParseInt(m[1], 0, 32)
	n <<= map[string]int{"k": 10, "m": 20, "g": 30}[strings.ToLower(m[2])]

	return n != 0, err == nil && n >= math.MinInt32 && n <= math.MaxInt32
}

// Keys returns the lowercase key names present in section[.subsection].
func (c *Config) Keys(section, subsection string) []string {
	prefix := variableName(section, subsection, "")
	seen := map[string]bool{}
	var out []string
	for _, e := range c.entries {
		key, ok := strings.CutPrefix(e.name, prefix)
		if ok && !strings.Contains(key, ".") && !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}

	return out
}

// utf8BOM is skipped at the start of a config file, as git does.
const utf8BOM = "\xef\xbb\xbf"

// ParseConfig parses git config syntax: [section] and [section "sub"]
// headers, key = value lines, quoting, the \n \t \b \\ \" escapes,
// backslash-newline continuation and ; or # comments.
func ParseConfig(data []byte) (*Config, error) {
	p := configParser{data: data, line: 1}
	if bytes.HasPrefix(data, []byte(utf8BOM)) {
		p.pos = len(utf8BOM)
	}
	c := &Config{}
	// Keys before any header have no section, as in git.
	var prefix string
	for {
		ch, ok := p.next()
		switch {
		case !ok:
			return c, nil
		case p.skipBlank(ch):
			continue
		case ch == '[':
			base, err := p.header()
			if err != nil {
				return nil, err
			}
			prefix = base + "."

			continue
		case !isAlpha(ch):
			return nil, p.errorf("unexpected character %q", ch)
		}
		key, noValue, value, err := p.variable(ch)
		if err != nil {
			return nil, err
		}
		c.entries = append(c.entries, configEntry{name: prefix + key, value: value, noValue: noValue})
	}
}

type configParser struct {
	data []byte
	pos  int
	line int
}

// next returns the next byte, folding CRLF into LF and counting lines.
func (p *configParser) next() (byte, bool) {
	if p.pos >= len(p.data) {
		return 0, false
	}
	c := p.data[p.pos]
	p.pos++
	if c == '\r' && p.pos < len(p.data) && p.data[p.pos] == '\n' {
		c = '\n'
		p.pos++
	}
	if c == '\n' {
		p.line++
	}

	return c, true
}

func (p *configParser) peek() (byte, bool) {
	if p.pos >= len(p.data) {
		return 0, false
	}

	return p.data[p.pos], true
}

func (p *configParser) skipLine() {
	for {
		c, ok := p.next()
		if !ok || c == '\n' {
			return
		}
	}
}

// skipBlank reports whether c, read between entries, starts nothing: a line
// end, a blank, or a comment, whose rest it consumes.
func (p *configParser) skipBlank(c byte) bool {
	switch c {
	case '\n', ' ', '\t', '\r':
		return true
	case '#', ';':
		p.skipLine()

		return true
	}

	return false
}

// skipSpaces consumes blanks without leaving the line.
func (p *configParser) skipSpaces() {
	for {
		c, ok := p.peek()
		if !ok || (c != ' ' && c != '\t') {
			return
		}
		p.pos++
	}
}

func (p *configParser) errorf(format string, a ...any) error {
	return fmt.Errorf("bad config line %d: %s", p.line, fmt.Sprintf(format, a...))
}

// header parses after "[" and returns "section[.subsection]": a section name,
// then "]" or a quoted subsection.
func (p *configParser) header() (string, error) {
	var name strings.Builder
	for {
		c, ok := p.next()
		switch {
		case !ok || c == '\n':
			return "", p.errorf("unterminated section header")
		case c == ']':
			if name.Len() == 0 {
				return "", p.errorf("empty section name")
			}

			return strings.ToLower(name.String()), nil // a dot is the legacy [section.subsection]
		case isSpace(c):
			sub, err := p.subsection()

			return strings.ToLower(name.String()) + "." + sub, err
		case isSectionChar(c):
			name.WriteByte(c)
		default:
			return "", p.errorf("invalid character %q in section name", c)
		}
	}
}

// subsection parses ` "name"]` after the section name.
func (p *configParser) subsection() (string, error) {
	if err := p.subsectionQuote(); err != nil {
		return "", err
	}
	sub, err := p.subsectionName()
	if err != nil {
		return "", err
	}
	if c, ok := p.next(); !ok || c != ']' {
		return "", p.errorf("expected ] after the subsection name")
	}

	return sub, nil
}

// subsectionQuote skips blanks up to and including the subsection's opening
// quote.
func (p *configParser) subsectionQuote() error {
	for {
		c, ok := p.next()
		if !ok || c == '\n' {
			return p.errorf("unterminated section header")
		}
		if isSpace(c) {
			continue
		}
		if c != '"' {
			return p.errorf("expected a quoted subsection name")
		}

		return nil
	}
}

// subsectionName reads a quoted subsection name up to and including its
// closing quote; a backslash takes the next byte literally.
func (p *configParser) subsectionName() (string, error) {
	var sub strings.Builder
	for {
		c, ok := p.next()
		if !ok || c == '\n' {
			return "", p.errorf("unterminated subsection name")
		}
		if c == '"' {
			return sub.String(), nil
		}
		if c == '\\' {
			if c, ok = p.next(); !ok || c == '\n' {
				return "", p.errorf("unterminated subsection name")
			}
		}
		sub.WriteByte(c)
	}
}

// variable parses a key starting with first, and its value if any.
func (p *configParser) variable(first byte) (key string, noValue bool, value string, err error) {
	name := p.keyName(first)
	p.skipSpaces()
	c, ok := p.next()
	switch {
	case !ok || c == '\n':
		return name, true, "", nil
	case c == '=':
		v, err := p.value()

		return name, false, v, err
	}

	return "", false, "", p.errorf("unexpected character %q after key %q", c, name)
}

// keyName reads the rest of a key name starting with first, lowercased.
func (p *configParser) keyName(first byte) string {
	var name strings.Builder
	name.WriteByte(first)
	for {
		c, ok := p.peek()
		if !ok || (!isAlnum(c) && c != '-') {
			return strings.ToLower(name.String())
		}
		p.pos++
		name.WriteByte(c)
	}
}

// value parses after "=" up to the end of the line, like git's parse_value
// since git 2.45: whitespace outside quotes is kept as written, except at the
// start of the value and before its end or a comment. (Older git turned each
// whitespace character into a space.)
func (p *configParser) value() (string, error) {
	v := configValue{trim: -1}
	for {
		c, ok := p.next()
		if !ok || c == '\n' {
			return v.end(p)
		}
		if err := v.add(p, c); err != nil {
			return "", err
		}
	}
}

// escape reads the byte after a backslash in a value and writes what it
// stands for; a backslash-newline continues the value on the next line.
func (p *configParser) escape(b *strings.Builder) error {
	e, ok := p.next()
	switch {
	case !ok || e == '\n':
		// line continuation; git reads the end of the file as a newline
	case e == 't':
		b.WriteByte('\t')
	case e == 'b':
		b.WriteByte('\b')
	case e == 'n':
		b.WriteByte('\n')
	case e == '\\' || e == '"':
		b.WriteByte(e)
	default:
		return p.errorf("invalid escape \\%c", e)
	}

	return nil
}

// configValue accumulates a value as configParser.value reads it.
type configValue struct {
	b       strings.Builder
	quote   bool // inside double quotes
	comment bool // past an unquoted ; or #
	trim    int  // length to cut back to if only whitespace follows
}

// add takes one byte of the value's line.
func (v *configValue) add(p *configParser, c byte) error {
	switch {
	case v.comment:
		return nil
	case !v.quote && isSpace(c):
		v.space(c)

		return nil
	case !v.quote && (c == ';' || c == '#'):
		v.comment = true

		return nil
	}
	v.trim = -1
	switch c {
	case '\\':
		return p.escape(&v.b)
	case '"':
		v.quote = !v.quote
	default:
		v.b.WriteByte(c)
	}

	return nil
}

// space takes unquoted whitespace: dropped at the start of the value, kept
// otherwise, and marked for trimming in case only whitespace follows.
func (v *configValue) space(c byte) {
	if v.trim < 0 {
		v.trim = v.b.Len()
	}
	if v.b.Len() > 0 {
		v.b.WriteByte(c)
	}
}

// end finishes the value at the end of its line.
func (v *configValue) end(p *configParser) (string, error) {
	if v.quote {
		return "", p.errorf("unterminated quoted value")
	}
	s := v.b.String()
	if v.trim >= 0 {
		s = s[:v.trim]
	}

	return s, nil
}

// isSpace is git's isspace, which parse_value uses: unlike C's, it leaves out
// \v and \f. '\n' never reaches it.
func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' }
func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' }

// isSectionChar reports a byte allowed in a section name.
func isSectionChar(c byte) bool { return isAlnum(c) || c == '-' || c == '.' }
