// Package sqlsplit cuts a SQL dump into statements.
//
// Splitting on the delimiter character is what every dialect's stream parser
// used to do, and it fails on the first routine in the file: a trigger body
// contains semicolons of its own, so the statement is cut in half and nothing
// after it parses. mysqldump writes DELIMITER ;; around exactly that, and
// PostgreSQL wraps a function body in $$, both of which say "the delimiter does
// not apply in here" and neither of which a character scan can see.
//
// The splitter reads the input as a stream, never holding more than the current
// statement, because the stream parsers exist for dumps too large to fit in
// memory.
package sqlsplit

import (
	"bufio"
	"io"
	"regexp"
	"strings"
)

// Mode says how a dialect separates statements.
type Mode int

const (
	// Semicolon ends a statement, as in MySQL, PostgreSQL and SQLite. A MySQL
	// dump may change it part way through with a DELIMITER directive.
	Semicolon Mode = iota
	// Batch is SQL Server: GO ends a batch and must stand alone on its line, and
	// a semicolon ends an ordinary statement within one.
	Batch
	// PLSQL is Oracle: a semicolon ends an ordinary statement, but inside a
	// PL/SQL block only a slash on its own line does.
	PLSQL
)

// ModeFor maps the delimiter strings the stream parsers pass to a mode.
func ModeFor(delimiter string) Mode {
	switch strings.ToUpper(strings.TrimSpace(delimiter)) {
	case "GO":
		return Batch
	case "/":
		return PLSQL
	}
	return Semicolon
}

// routineStart matches the statements whose body carries semicolons of its own.
// Inside one of these only the line terminator ends the statement, which is how
// Oracle's slash and SQL Server's GO are meant to be used.
var routineStart = regexp.MustCompile(`(?is)^\s*(?:CREATE` +
	`(?:\s+OR\s+ALTER)?(?:\s+OR\s+REPLACE)?` +
	// mysqldump writes DEFINER, and a view or routine can carry ALGORITHM and
	// SQL SECURITY too. Without them a dumped trigger did not look like a
	// routine at all, and its body was cut at the first inner semicolon.
	`(?:\s+DEFINER\s*=\s*\S+)?(?:\s+ALGORITHM\s*=\s*\S+)?(?:\s+SQL\s+SECURITY\s+\w+)?` +
	// DBMS_METADATA writes EDITIONABLE between REPLACE and the object keyword.
	`(?:\s+(?:NON)?EDITIONABLE)?` +
	`\s+(?:FUNCTION|PROCEDURE|PROC|TRIGGER|PACKAGE|TYPE\s+BODY)|DECLARE|BEGIN)\b`)

// delimiterDirective matches MySQL's DELIMITER, which changes the terminator
// for the statements that follow it.
var delimiterDirective = regexp.MustCompile(`(?i)^\s*DELIMITER\s+(\S+)\s*$`)

// Splitter reads statements one at a time.
type Splitter struct {
	r    *bufio.Reader
	mode Mode

	// delimiter is the current terminator in Semicolon mode, which a DELIMITER
	// directive can change.
	delimiter string

	// dollarQuoting is off when the caller's own delimiter uses a dollar.
	dollarQuoting bool

	// versionComment counts the open /*! ... */ blocks. MySQL executes what is
	// inside one when the server is new enough, so the content is SQL and only
	// the wrapper is dropped.
	versionComment int

	// sawBegin records that the statement has opened a BEGIN block, and prevWord
	// is the last complete word before the current position. Together they say
	// whether a semicolon belongs to the body or ends the statement. Both are
	// fed from the ordinary scan path only, so a BEGIN inside a string, a
	// comment or a dollar-quoted body is not one.
	sawBegin bool
	prevWord string
	word     strings.Builder

	buf  strings.Builder
	line strings.Builder // the current line, for the line-oriented terminators
	done bool
}

// New returns a splitter over r for the given delimiter. "GO" and "/" select
// the line-oriented rules of SQL Server and Oracle; anything else is used as the
// terminating characters directly, which is how a caller passes a custom one.
func New(r io.Reader, delimiter string) *Splitter {
	mode := ModeFor(delimiter)

	term := delimiter
	if mode != Semicolon || term == "" {
		term = ";"
	}

	return &Splitter{
		r:         bufio.NewReader(r),
		mode:      mode,
		delimiter: term,
		// A delimiter written with a dollar is the caller's own, so the
		// PostgreSQL dollar-quoting rule would fight with it.
		dollarQuoting: !strings.Contains(term, "$"),
	}
}

// Next returns the next statement, without its terminator. It returns io.EOF
// when the input is exhausted.
func (s *Splitter) Next() (string, error) {
	for {
		stmt, err := s.next()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		return stmt, nil
	}
}

func (s *Splitter) next() (string, error) {
	if s.done {
		return "", io.EOF
	}

	s.buf.Reset()
	s.line.Reset()
	s.sawBegin = false
	s.prevWord = ""
	s.word.Reset()
	s.versionComment = 0

	// A DELIMITER directive is an instruction to the client rather than a
	// statement. It has to be taken before anything else, because the semicolon
	// it names would otherwise terminate on itself.
	s.consumeDelimiterDirectives()

	for {
		c, err := s.r.ReadByte()
		if err != nil {
			s.done = true
			// The last line of a file often has no newline after it, so a
			// terminator standing there has not been examined yet.
			if stmt, ok := s.lineTerminates(); ok {
				return stmt, nil
			}
			if s.buf.Len() > 0 {
				return s.buf.String(), nil
			}
			return "", io.EOF
		}

		switch c {
		case '\'':
			s.closeWord()
			s.buf.WriteByte(c)
			s.line.WriteByte(c)
			if err := s.consumeQuoted('\'', true); err != nil {
				return s.finish()
			}
			continue

		case '"', '`':
			s.closeWord()
			s.buf.WriteByte(c)
			s.line.WriteByte(c)
			if err := s.consumeQuoted(c, false); err != nil {
				return s.finish()
			}
			continue

		case '[':
			s.closeWord()
			s.buf.WriteByte(c)
			s.line.WriteByte(c)
			if err := s.consumeQuoted(']', false); err != nil {
				return s.finish()
			}
			continue

		case '$':
			// PostgreSQL wraps a routine body in $$ or $tag$, inside which the
			// delimiter has no meaning. A dollar that opens nothing falls
			// through to the ordinary path, where it can still be part of the
			// caller's own delimiter.
			if s.dollarQuoting {
				if tag, ok := s.readDollarTag(); ok {
					s.closeWord()
					s.buf.WriteString(tag)
					s.line.WriteString(tag)
					if err := s.consumeDollarQuoted(tag); err != nil {
						return s.finish()
					}
					continue
				}
			}

		case '-':
			if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '-' {
				s.consumeLineComment()
				continue
			}

		case '*':
			// The end of a version comment: the wrapper goes, the SQL inside it
			// stays.
			if s.versionComment > 0 {
				if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '/' {
					_, _ = s.r.ReadByte()
					s.versionComment--
					s.closeWord()
					continue
				}
			}

		case '/':
			if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '*' {
				s.closeWord()
				if s.openVersionComment() {
					s.versionComment++
					continue
				}
				s.consumeBlockComment()
				continue
			}

		case '\n':
			// A line-oriented terminator is only one when it stands alone.
			if stmt, ok := s.lineTerminates(); ok {
				return stmt, nil
			}
			s.closeWord()
			s.buf.WriteByte(c)
			s.line.Reset()
			continue
		}

		s.buf.WriteByte(c)
		s.line.WriteByte(c)
		s.noteWordByte(c)

		if s.terminatorApplies() {
			if stmt, ok := s.delimiterTerminates(); ok {
				return stmt, nil
			}
		}
	}
}

// terminatorApplies reports whether the character delimiter is meaningful at
// this point.
//
// A semicolon ends an ordinary statement everywhere. Inside a routine body it
// does not, because the body carries semicolons of its own. Each dialect says
// where such a body ends in its own way: Oracle writes a slash on the next line,
// SQL Server writes GO, MySQL changes the delimiter, PostgreSQL wraps the body
// in dollar quotes. SQLite has none of those, and marks the end with END, so
// that is what closes one here.
// routineStartBound caps how much of a statement the routine test reads.
//
// The pattern is anchored at the start, so nothing past its longest possible
// match can change the answer, and the longest is a CREATE carrying OR ALTER,
// OR REPLACE, DEFINER, ALGORITHM, SQL SECURITY and NONEDITIONABLE before the
// object keyword. Reading the whole buffer instead rescanned every buffered
// statement on every terminator, which is most of what it cost to split a large
// dump.
const routineStartBound = 512

func (s *Splitter) terminatorApplies() bool {
	head := s.buf.String()
	if len(head) > routineStartBound {
		head = head[:routineStartBound]
	}
	if !routineStart.MatchString(head) {
		return true
	}
	if s.mode != Semicolon {
		// The line terminator is the only one inside a body.
		return false
	}
	if !s.sawBegin {
		// A routine with no block body, such as a PostgreSQL trigger that only
		// names the function to execute, ends on the delimiter like anything
		// else.
		return true
	}
	// Inside a block the delimiter is meaningful only after END. That is the
	// rule SQLite needs, and it costs the others nothing: their own markers, a
	// changed DELIMITER or a dollar-quoted body, still decide where the
	// statement really ends.
	return strings.EqualFold(s.prevWord, "END")
}

// noteWordByte feeds the running word, which is how BEGIN and END are seen
// without a second pass over the buffer.
func (s *Splitter) noteWordByte(c byte) {
	if isTagByte(c) {
		s.word.WriteByte(c)
		return
	}
	s.closeWord()
}

// closeWord ends the word in progress. An empty word leaves the previous one
// standing, so the space in "END ;" does not hide the END.
func (s *Splitter) closeWord() {
	if s.word.Len() == 0 {
		return
	}
	w := s.word.String()
	s.word.Reset()
	s.prevWord = w
	if strings.EqualFold(w, "BEGIN") {
		s.sawBegin = true
	}
}

// delimiterTerminates reports whether the statement just ended on the current
// character delimiter, and returns it with the delimiter removed.
func (s *Splitter) delimiterTerminates() (string, bool) {
	stmt := s.buf.String()
	if !strings.HasSuffix(stmt, s.delimiter) {
		return "", false
	}
	return stmt[:len(stmt)-len(s.delimiter)], true
}

// lineTerminates reports whether the line just completed was a lone terminator:
// GO for SQL Server, or a slash for Oracle.
func (s *Splitter) lineTerminates() (string, bool) {
	word := strings.TrimSpace(s.line.String())
	s.line.Reset()

	switch s.mode {
	case Batch:
		// SQL Server allows a repeat count after GO.
		if fields := strings.Fields(word); len(fields) > 0 && strings.EqualFold(fields[0], "GO") {
			stmt := s.buf.String()
			return strings.TrimSuffix(stmt, word), true
		}
	case PLSQL:
		if word == "/" {
			stmt := s.buf.String()
			return strings.TrimSuffix(stmt, word), true
		}
	}
	return "", false
}

// consumeDelimiterDirectives reads any DELIMITER lines waiting at the current
// position, setting the terminator and leaving nothing behind.
func (s *Splitter) consumeDelimiterDirectives() {
	if s.mode != Semicolon {
		return
	}

	for {
		s.skipSpace()

		line, ok := s.peekLine()
		if !ok {
			return
		}
		m := delimiterDirective.FindStringSubmatch(line)
		if m == nil {
			return
		}
		if _, err := s.r.Discard(len(line)); err != nil {
			return
		}
		// Take the newline that ends the directive, if there is one.
		if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == '\n' {
			_, _ = s.r.ReadByte()
		}
		s.delimiter = m[1]
	}
}

// skipSpace consumes whitespace without recording it, so a directive is found
// whatever precedes it.
func (s *Splitter) skipSpace() {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			_ = s.r.UnreadByte()
			return
		}
	}
}

// peekLine returns the text up to the next newline without consuming it.
func (s *Splitter) peekLine() (string, bool) {
	peek, _ := s.r.Peek(512)
	if len(peek) == 0 {
		return "", false
	}
	if i := strings.IndexByte(string(peek), '\n'); i >= 0 {
		return string(peek[:i]), true
	}
	return string(peek), true
}

// consumeQuoted reads to the closing quote. A doubled quote is the escape
// everywhere; a backslash escape is accepted inside string literals because
// MySQL writes them.
func (s *Splitter) consumeQuoted(close byte, backslash bool) error {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return err
		}
		s.buf.WriteByte(c)
		s.line.WriteByte(c)

		switch {
		case backslash && c == '\\':
			n, err := s.r.ReadByte()
			if err != nil {
				return err
			}
			s.buf.WriteByte(n)
			s.line.WriteByte(n)
		case c == close:
			if next, _ := s.r.Peek(1); len(next) == 1 && next[0] == close {
				n, _ := s.r.ReadByte()
				s.buf.WriteByte(n)
				s.line.WriteByte(n)
				continue
			}
			return nil
		case c == '\n':
			s.line.Reset()
		}
	}
}

// readDollarTag reads a PostgreSQL dollar-quote opener, having already consumed
// the leading dollar. It returns the whole tag, or reports false and consumes
// nothing when what follows is not one.
func (s *Splitter) readDollarTag() (string, bool) {
	peek, _ := s.r.Peek(64)

	end := -1
	for i, c := range peek {
		if c == '$' {
			end = i
			break
		}
		if !isTagByte(c) {
			return "", false
		}
	}
	if end == -1 {
		return "", false
	}

	tag := "$" + string(peek[:end]) + "$"
	if _, err := s.r.Discard(end + 1); err != nil {
		return "", false
	}
	return tag, true
}

func isTagByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// consumeDollarQuoted reads to the matching closing tag.
func (s *Splitter) consumeDollarQuoted(tag string) error {
	var tail strings.Builder
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return err
		}
		s.buf.WriteByte(c)
		s.line.WriteByte(c)
		if c == '\n' {
			s.line.Reset()
		}

		tail.WriteByte(c)
		if tail.Len() > len(tag) {
			trimmed := tail.String()[tail.Len()-len(tag):]
			tail.Reset()
			tail.WriteString(trimmed)
		}
		if tail.String() == tag {
			return nil
		}
	}
}

// consumeLineComment discards to the end of the line. The newline is kept so
// that a line-oriented terminator on the next line is still recognised.
func (s *Splitter) consumeLineComment() {
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return
		}
		if c == '\n' {
			s.buf.WriteByte(c)
			s.line.Reset()
			return
		}
	}
}

// openVersionComment consumes the opener of a MySQL version comment, having
// already seen the slash and peeked the star. It reports false and consumes
// nothing when what follows is an ordinary block comment.
//
// mysqldump writes a trigger as /*!50003 CREATE*/ /*!50017 DEFINER=...*/
// /*!50003 TRIGGER ... */, so a splitter that treats the whole thing as a
// comment throws the trigger away with it.
func (s *Splitter) openVersionComment() bool {
	peek, _ := s.r.Peek(8)
	if len(peek) < 2 || peek[0] != '*' || peek[1] != '!' {
		return false
	}

	n := 2
	for n < len(peek) && peek[n] >= '0' && peek[n] <= '9' {
		n++
	}
	if _, err := s.r.Discard(n); err != nil {
		return false
	}
	return true
}

// consumeBlockComment discards to the closing marker, keeping any newlines so
// the line-oriented terminators still line up.
func (s *Splitter) consumeBlockComment() {
	prev := byte(0)
	for {
		c, err := s.r.ReadByte()
		if err != nil {
			return
		}
		if c == '\n' {
			s.buf.WriteByte(c)
			s.line.Reset()
		}
		if prev == '*' && c == '/' {
			return
		}
		prev = c
	}
}

// finish returns whatever was accumulated when the input ended mid-construct,
// so an unterminated string does not swallow the statement silently. The text
// goes on to the dialect parser, which reports a real error about it.
func (s *Splitter) finish() (string, error) {
	s.done = true
	if s.buf.Len() > 0 {
		return s.buf.String(), nil
	}
	return "", io.EOF
}
