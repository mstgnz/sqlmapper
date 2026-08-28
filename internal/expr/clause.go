package expr

import "strings"

// TranslateViewBody rewrites the parts of a view definition that this package
// understands and leaves the rest exactly as it was.
//
// A view body is a statement, not an expression, and parsing SELECT is a larger
// job than this package takes on. What it does instead is locate the WHERE
// clause with the scanner, which knows about strings, comments and nesting, and
// translate that one expression. That is enough for the case the dialects
// actually disagree about: a PostgreSQL view saying WHERE is_active does not
// load on Oracle 21 or SQL Server, both of which need WHERE is_active <> 0.
//
// The select list, the joins and anything else are passed through untouched.
// Text that cannot be read comes back unchanged, so a body this package does
// not understand is no worse off than before.
func TranslateViewBody(body string, to Dialect) string {
	return TranslateViewBodyWithBooleans(body, to, nil)
}

// TranslateViewBodyWithBooleans is TranslateViewBody told which columns the
// target really declares as boolean.
func TranslateViewBodyWithBooleans(body string, to Dialect, booleans map[string]bool) string {
	// A qualifier naming another dialect's default schema has to go from the
	// whole body, not only the part that gets translated: public.customers in a
	// FROM clause does not resolve anywhere but PostgreSQL.
	body = stripDefaultSchemaQualifiers(body)

	start, end, ok := findWhereClause(body)
	if !ok {
		return body
	}

	clause := strings.TrimSpace(body[start:end])
	if clause == "" {
		return body
	}

	translated := ConditionWithBooleans(clause, to, booleans)
	if translated == clause {
		return body
	}

	// The clause was trimmed, so the separator on each side has to be put back
	// or the next keyword ends up glued to the expression.
	rest := body[end:]
	if rest != "" && !isSpace(rest[0]) {
		rest = " " + rest
	}
	return body[:start] + " " + translated + rest
}

// whereEnd are the keywords that close a WHERE clause. Anything after one of
// them belongs to the rest of the statement.
var whereEnd = map[string]bool{
	"GROUP": true, "HAVING": true, "ORDER": true, "LIMIT": true,
	"OFFSET": true, "FETCH": true, "WINDOW": true, "UNION": true,
	"INTERSECT": true, "EXCEPT": true, "FOR": true, "WITH": true,
}

// findWhereClause returns the byte range of the expression following the
// top-level WHERE, if there is one.
//
// The scanner is what makes this safe: a WHERE inside a string literal, inside a
// comment or inside a subquery's parentheses is not the one being looked for,
// and a regular expression cannot tell the difference.
func findWhereClause(body string) (start, end int, ok bool) {
	s := NewScanner(body)

	depth := 0
	for {
		tok, err := s.Next()
		if err != nil || tok.Kind == TokEOF {
			return 0, 0, false
		}

		switch {
		case tok.Kind == TokPunct && tok.Text == "(":
			depth++
		case tok.Kind == TokPunct && tok.Text == ")":
			depth--
		case depth == 0 && tok.Kind == TokIdent && !tok.Quoted && strings.EqualFold(tok.Text, "WHERE"):
			start = tok.Pos + len(tok.Raw)
			return start, findClauseEnd(body, start), true
		}
	}
}

// findClauseEnd returns the offset at which the clause starting at start ends,
// which is the next top-level keyword that opens a different clause, or the end
// of the input.
func findClauseEnd(body string, start int) int {
	s := NewScanner(body[start:])

	depth := 0
	for {
		tok, err := s.Next()
		if err != nil || tok.Kind == TokEOF {
			return len(body)
		}

		switch {
		case tok.Kind == TokPunct && tok.Text == "(":
			depth++
		case tok.Kind == TokPunct && tok.Text == ")":
			// A closing parenthesis with nothing open belongs to whatever
			// wrapped the whole body, so the clause ends before it.
			if depth == 0 {
				return start + tok.Pos
			}
			depth--
		case depth == 0 && tok.Kind == TokIdent && !tok.Quoted && whereEnd[strings.ToUpper(tok.Text)]:
			return start + tok.Pos
		}
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// stripDefaultSchemaQualifiers removes a public. or dbo. qualifier wherever it
// appears in a statement.
//
// This is a token-level rewrite rather than a textual one: the scanner knows
// that the word inside 'public.customers' is a string and that a column named
// public_id is not a qualifier, which is what a pattern over the raw text kept
// getting wrong.
func stripDefaultSchemaQualifiers(body string) string {
	toks, err := Tokens(body)
	if err != nil {
		return body
	}

	// Collect the byte ranges to remove, then splice from the end so the
	// earlier offsets stay valid.
	type span struct{ start, end int }
	var cuts []span

	for i := 0; i+2 < len(toks); i++ {
		name, dot, next := toks[i], toks[i+1], toks[i+2]
		if name.Kind != TokIdent || !defaultSchemas[strings.ToLower(name.Text)] {
			continue
		}
		if dot.Kind != TokPunct || dot.Text != "." || next.Kind != TokIdent {
			continue
		}
		// A qualifier is never itself qualified: in a.public.b the middle word
		// is an object name, not a schema.
		if i > 0 && toks[i-1].Kind == TokPunct && toks[i-1].Text == "." {
			continue
		}
		cuts = append(cuts, span{start: name.Pos, end: next.Pos})
	}

	out := body
	for i := len(cuts) - 1; i >= 0; i-- {
		out = out[:cuts[i].start] + out[cuts[i].end:]
	}
	return out
}
