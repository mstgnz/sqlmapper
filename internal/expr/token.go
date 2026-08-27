// Package expr parses SQL expressions into a small syntax tree and writes them
// back out for a different dialect.
//
// It exists because the dialects disagree about things a string copy cannot
// reconcile: PostgreSQL writes a cast as ((amount >= (0)::numeric)), SQL Server
// writes the same constraint as (([amount]>=(0))), and a PostgreSQL view says
// WHERE is_active where Oracle needs WHERE is_active <> 0 because it has no
// boolean type. Every one of those was previously handled by a per-dialect
// regular expression that stripped whatever it recognised and left the rest.
//
// The package is internal on purpose. The Schema model stays a plain
// string-based data model, so nothing here is part of the module's public API
// and it can change freely.
package expr

import "fmt"

// TokenKind classifies a token produced by the scanner.
type TokenKind int

const (
	// TokEOF marks the end of the input.
	TokEOF TokenKind = iota
	// TokIdent is a bare or quoted identifier.
	TokIdent
	// TokNumber is a numeric literal.
	TokNumber
	// TokString is a single-quoted string literal.
	TokString
	// TokKeyword is a reserved word the parser gives meaning to.
	TokKeyword
	// TokOperator is a symbolic or word operator.
	TokOperator
	// TokPunct is one of ( ) , .
	TokPunct
)

func (k TokenKind) String() string {
	switch k {
	case TokEOF:
		return "end of input"
	case TokIdent:
		return "identifier"
	case TokNumber:
		return "number"
	case TokString:
		return "string"
	case TokKeyword:
		return "keyword"
	case TokOperator:
		return "operator"
	case TokPunct:
		return "punctuation"
	}
	return fmt.Sprintf("TokenKind(%d)", int(k))
}

// Token is one lexical unit.
type Token struct {
	Kind TokenKind

	// Text is the value the parser works with: an identifier with its quotes
	// removed, a string literal with its quotes removed and its escapes undone,
	// a keyword or word operator folded to upper case.
	Text string

	// Raw is the token exactly as it appeared, which the renderer needs for
	// numeric literals so that 0.00 does not come back as 0.
	Raw string

	// Quoted reports whether an identifier was written inside quotes. A quoted
	// identifier keeps its case; an unquoted one was folded by the server that
	// produced the dump and can be folded back.
	Quoted bool

	// Pos is the byte offset the token starts at, used in error messages.
	Pos int
}

// keywords are the words the parser treats as syntax rather than as names.
// Everything else scans as an identifier, including function names.
var keywords = map[string]bool{
	"AND": true, "OR": true, "NOT": true, "IS": true, "NULL": true,
	"TRUE": true, "FALSE": true, "IN": true, "LIKE": true, "BETWEEN": true,
	"CAST": true, "AS": true, "DISTINCT": true, "ESCAPE": true,
}
