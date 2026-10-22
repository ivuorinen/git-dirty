package git

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	section, subsection, key string
	value                    string
	noValue                  bool // "key" alone, which git reads as true
}

// ReadConfigFile parses the file at path; a missing file is an empty config.
func ReadConfigFile(path string) (*Config, error) {
	// Open passes the config under the repository's commondir, a path read
	// from the repository; git reads the shared config there too.
	//nolint:gosec // G703: following commondir to the shared config is git's own behavior
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
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
	for i := len(c.entries) - 1; i >= 0; i-- {
		if e := c.entries[i]; e.matches(section, subsection, key) {
			return e.value, true
		}
	}

	return "", false
}

// GetAll returns every value of a multi-valued key, in file order.
func (c *Config) GetAll(section, subsection, key string) []string {
	var out []string
	for _, e := range c.entries {
		if e.matches(section, subsection, key) {
			out = append(out, e.value)
		}
	}

	return out
}

// Bool returns a boolean key the way git parses it (true/yes/on/1,
// false/no/off/0, a bare key is true); def when absent or unparsable.
func (c *Config) Bool(section, subsection, key string, def bool) bool {
	for i := len(c.entries) - 1; i >= 0; i-- {
		e := c.entries[i]
		if !e.matches(section, subsection, key) {
			continue
		}
		if e.noValue {
			return true
		}
		switch strings.ToLower(strings.TrimSpace(e.value)) {
		case "true", "yes", "on", "1":
			return true
		case "false", "no", "off", "0", "":
			return false
		}

		return def
	}

	return def
}

// Keys returns the lowercase key names present in section[.subsection].
func (c *Config) Keys(section, subsection string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range c.entries {
		if e.section == strings.ToLower(section) && e.subsection == subsection && !seen[e.key] {
			seen[e.key] = true
			out = append(out, e.key)
		}
	}

	return out
}

func (e configEntry) matches(section, subsection, key string) bool {
	return e.section == strings.ToLower(section) && e.subsection == subsection && e.key == strings.ToLower(key)
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
	// Keys before any header belong to the empty section, as in git.
	var section, subsection string
	for {
		ch, ok := p.next()
		switch {
		case !ok:
			return c, nil
		case p.skipBlank(ch):
			continue
		case ch == '[':
			var err error
			if section, subsection, err = p.header(); err != nil {
				return nil, err
			}

			continue
		case !isAlpha(ch):
			return nil, p.errorf("unexpected character %q", ch)
		}
		key, noValue, value, err := p.variable(ch)
		if err != nil {
			return nil, err
		}
		c.entries = append(c.entries, configEntry{
			section: section, subsection: subsection, key: key, value: value, noValue: noValue,
		})
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
	case '\n', ' ', '\t':
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

// header parses after "[": a section name, then "]" or a quoted subsection.
func (p *configParser) header() (section, subsection string, err error) {
	var name strings.Builder
	for {
		c, ok := p.next()
		switch {
		case !ok || c == '\n':
			return "", "", p.errorf("unterminated section header")
		case c == ']':
			return p.closeHeader(name.String())
		case c == ' ' || c == '\t':
			if name.Len() == 0 {
				return "", "", p.errorf("empty section name")
			}
			sub, err := p.subsection()

			return strings.ToLower(name.String()), sub, err
		case isSectionChar(c):
			name.WriteByte(c)
		default:
			return "", "", p.errorf("invalid character %q in section name", c)
		}
	}
}

// closeHeader returns the section of a header without a quoted subsection,
// splitting the legacy [section.subsection] form.
func (p *configParser) closeHeader(name string) (section, subsection string, err error) {
	if name == "" {
		return "", "", p.errorf("empty section name")
	}
	n := strings.ToLower(name)
	if s, sub, dotted := strings.Cut(n, "."); dotted {
		return s, sub, nil // legacy [section.subsection]
	}

	return n, "", nil
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
		if c == ' ' || c == '\t' {
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
	case c == '#' || c == ';':
		p.skipLine()

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
	name.WriteByte(toLower(first))
	for {
		c, ok := p.peek()
		if !ok || (!isAlnum(c) && c != '-') {
			return name.String()
		}
		p.pos++
		name.WriteByte(toLower(c))
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
	case !ok:
		return p.errorf("unterminated escape")
	case e == '\n':
		// line continuation
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

// isSpace is C's isspace, which parse_value uses; '\n' never reaches it.
func isSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }
func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' }

// isSectionChar reports a byte allowed in a section name.
func isSectionChar(c byte) bool { return isAlnum(c) || c == '-' || c == '.' }

func toLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}

	return c
}
