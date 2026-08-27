package expr

import (
	"fmt"
	"strings"
)

// Scanner turns SQL text into tokens.
//
// It is deliberately lenient about quoting: it accepts the double quotes of
// standard SQL, the backticks MySQL writes and the brackets SQL Server writes,
// whatever dialect it was told about. Dumps only ever use their own dialect's
// quoting, and accepting all three means the same scanner reads all of them.
// The one thing it does not do is treat a double-quoted run as a string, which
// MySQL does outside ANSI_QUOTES mode; mysqldump quotes strings with single
// quotes, so that case does not arise in the input this package sees.
type Scanner struct {
	src string
	pos int
}

// NewScanner returns a scanner over src.
func NewScanner(src string) *Scanner {
	return &Scanner{src: src}
}

// Tokens scans the whole input.
func Tokens(src string) ([]Token, error) {
	s := NewScanner(src)
	var out []Token
	for {
		tok, err := s.Next()
		if err != nil {
			return nil, err
		}
		out = append(out, tok)
		if tok.Kind == TokEOF {
			return out, nil
		}
	}
}

// Next returns the next token, or a token of kind TokEOF at the end of the input.
func (s *Scanner) Next() (Token, error) {
	s.skipSpaceAndComments()
	if s.pos >= len(s.src) {
		return Token{Kind: TokEOF, Pos: s.pos}, nil
	}

	start := s.pos
	c := s.src[s.pos]

	switch {
	case c == '\'':
		return s.scanString()
	case c == '"' || c == '`':
		return s.scanQuotedIdent(c)
	case c == '[':
		return s.scanQuotedIdent(']')
	case isDigit(c):
		return s.scanNumber()
	case c == '.' && s.pos+1 < len(s.src) && isDigit(s.src[s.pos+1]):
		return s.scanNumber()
	case isIdentStart(c):
		return s.scanWord()
	}

	if op, n := s.matchOperator(); n > 0 {
		s.pos += n
		return Token{Kind: TokOperator, Text: op, Raw: op, Pos: start}, nil
	}

	if strings.ContainsRune("(),.", rune(c)) {
		s.pos++
		return Token{Kind: TokPunct, Text: string(c), Raw: string(c), Pos: start}, nil
	}

	return Token{}, fmt.Errorf("expr: unexpected character %q at offset %d", c, start)
}

func (s *Scanner) skipSpaceAndComments() {
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			s.pos++
		case c == '-' && s.pos+1 < len(s.src) && s.src[s.pos+1] == '-':
			for s.pos < len(s.src) && s.src[s.pos] != '\n' {
				s.pos++
			}
		case c == '/' && s.pos+1 < len(s.src) && s.src[s.pos+1] == '*':
			s.pos += 2
			for s.pos < len(s.src) {
				if s.src[s.pos] == '*' && s.pos+1 < len(s.src) && s.src[s.pos+1] == '/' {
					s.pos += 2
					break
				}
				s.pos++
			}
		default:
			return
		}
	}
}

// scanString reads a single-quoted literal. A doubled quote is the standard
// escape; a backslash escape is accepted too because MySQL writes them.
func (s *Scanner) scanString() (Token, error) {
	start := s.pos
	s.pos++ // opening quote

	var b strings.Builder
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case c == '\'':
			if s.pos+1 < len(s.src) && s.src[s.pos+1] == '\'' {
				b.WriteByte('\'')
				s.pos += 2
				continue
			}
			s.pos++
			return Token{
				Kind: TokString,
				Text: b.String(),
				Raw:  s.src[start:s.pos],
				Pos:  start,
			}, nil
		case c == '\\' && s.pos+1 < len(s.src):
			b.WriteByte(unescape(s.src[s.pos+1]))
			s.pos += 2
		default:
			b.WriteByte(c)
			s.pos++
		}
	}

	return Token{}, fmt.Errorf("expr: unterminated string literal at offset %d", start)
}

func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case '0':
		return 0
	}
	return c
}

// scanQuotedIdent reads an identifier up to the given closing character. A
// doubled closing character is the escape, as in "a""b" and [a]]b].
func (s *Scanner) scanQuotedIdent(close byte) (Token, error) {
	start := s.pos
	s.pos++ // opening quote

	var b strings.Builder
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		if c == close {
			if s.pos+1 < len(s.src) && s.src[s.pos+1] == close {
				b.WriteByte(close)
				s.pos += 2
				continue
			}
			s.pos++
			return Token{
				Kind:   TokIdent,
				Text:   b.String(),
				Raw:    s.src[start:s.pos],
				Quoted: true,
				Pos:    start,
			}, nil
		}
		b.WriteByte(c)
		s.pos++
	}

	return Token{}, fmt.Errorf("expr: unterminated quoted identifier at offset %d", start)
}

func (s *Scanner) scanNumber() (Token, error) {
	start := s.pos
	seenDot := false
	seenExp := false

	for s.pos < len(s.src) {
		c := s.src[s.pos]
		switch {
		case isDigit(c):
			s.pos++
		case c == '.' && !seenDot && !seenExp:
			seenDot = true
			s.pos++
		case (c == 'e' || c == 'E') && !seenExp && s.pos+1 < len(s.src):
			next := s.src[s.pos+1]
			if isDigit(next) || ((next == '+' || next == '-') && s.pos+2 < len(s.src) && isDigit(s.src[s.pos+2])) {
				seenExp = true
				s.pos += 2
				continue
			}
			return s.numberToken(start), nil
		default:
			return s.numberToken(start), nil
		}
	}
	return s.numberToken(start), nil
}

func (s *Scanner) numberToken(start int) Token {
	raw := s.src[start:s.pos]
	return Token{Kind: TokNumber, Text: raw, Raw: raw, Pos: start}
}

// scanWord reads a bare identifier, which becomes a keyword or a word operator
// when it matches one.
func (s *Scanner) scanWord() (Token, error) {
	start := s.pos
	for s.pos < len(s.src) && isIdentPart(s.src[s.pos]) {
		s.pos++
	}

	raw := s.src[start:s.pos]
	upper := strings.ToUpper(raw)

	if keywords[upper] {
		return Token{Kind: TokKeyword, Text: upper, Raw: raw, Pos: start}, nil
	}
	return Token{Kind: TokIdent, Text: raw, Raw: raw, Pos: start}, nil
}

// operators are matched longest first so that <= is not read as < followed by =.
var operators = []string{
	"<=>", "!=", "<>", "<=", ">=", "||", "::",
	"=", "<", ">", "+", "-", "*", "/", "%",
}

func (s *Scanner) matchOperator() (string, int) {
	rest := s.src[s.pos:]
	for _, op := range operators {
		if strings.HasPrefix(rest, op) {
			return op, len(op)
		}
	}
	return "", 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentStart(c byte) bool {
	return c == '_' || c == '@' || c == '#' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || isDigit(c) }
