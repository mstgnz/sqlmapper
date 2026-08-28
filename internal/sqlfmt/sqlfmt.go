// Package sqlfmt holds the small formatting rules the generators share.
package sqlfmt

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mstgnz/sqlmapper/internal/expr"
	"github.com/mstgnz/sqlmapper/internal/keyword"
)

// Terminate returns stmt followed by exactly one term.
//
// The generators disagreed about whose job the terminator was: some
// generateTableSQL implementations ended with a semicolon and some did not,
// while every stream call site appended one regardless. The result was a
// statement ending in ";;" in half the dialects.
func Terminate(stmt, term string) string {
	s := strings.TrimRight(stmt, " \t\n")
	if s == "" {
		return ""
	}
	return strings.TrimSuffix(s, term) + term
}

// Comment turns text into a SQL comment, one "-- " per line. A block that is
// commented out has to stay commented out on every line, including the blank
// ones, or the statement after it is read as SQL.
func Comment(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = "--"
			continue
		}
		lines[i] = "-- " + line
	}
	return strings.Join(lines, "\n")
}

// ForeignRoutine comments out a routine that came from another database.
//
// A routine body is procedural code, and this library translates DDL, not
// procedures. Writing the body out as it stands would produce a file that fails
// to load at that statement, and dropping it silently would lose work the
// reader does not know is missing. Commenting it keeps the file loadable and
// keeps the routine in front of whoever has to port it.
func ForeignRoutine(source, sql string) string {
	header := fmt.Sprintf(
		"Defined by the %s source. The body is procedural code, which is not\ntranslated, so it is left commented out. Port it by hand.",
		source)
	return Comment(header + "\n\n" + strings.TrimRight(sql, "\n"))
}

// SourceRoutineSQL reconstructs a routine the way the source dialect wrote it.
//
// It is used for the commented-out form, where rendering the routine in the
// target's shape would be misleading: PostgreSQL keeps the name of the function
// a trigger calls in the same field MySQL uses for a procedural body, so a
// MySQL body rendered as PostgreSQL reads as EXECUTE FUNCTION BEGIN.
func SourceRoutineSQL(header, body string) string {
	header = strings.TrimRight(header, " \n")
	body = strings.TrimSpace(body)
	if body == "" {
		return header
	}
	return header + "\n" + body
}

// TriggerHeader renders the part of a CREATE TRIGGER before its body, in the
// form every dialect spells more or less the same way.
func TriggerHeader(name, timing, event, table string, forEachRow bool) string {
	var sb strings.Builder
	sb.WriteString("CREATE TRIGGER ")
	sb.WriteString(name)
	for _, part := range []string{timing, event} {
		if part != "" {
			sb.WriteString(" ")
			sb.WriteString(part)
		}
	}
	if table != "" {
		sb.WriteString(" ON ")
		sb.WriteString(table)
	}
	if forEachRow {
		sb.WriteString("\nFOR EACH ROW")
	}
	return sb.String()
}

// UnsupportedRoutine comments out a routine the target cannot express at all,
// such as a stored function on SQLite.
func UnsupportedRoutine(reason, sql string) string {
	return Comment(reason + "\n\n" + strings.TrimRight(sql, "\n"))
}

// BlockBody wraps a routine body in BEGIN and END unless it already carries
// them.
//
// Parsers disagree about whether the keywords belong to the body: MySQL and
// SQLite capture what is between them, Oracle and SQL Server keep them. A
// trigger written out without them runs only its first statement, if the server
// accepts it at all.
func BlockBody(body string) string {
	b := strings.TrimSpace(body)
	if b == "" {
		return "BEGIN\nEND"
	}
	if strings.EqualFold(firstWord(b), "BEGIN") {
		return b
	}
	return "BEGIN\n" + b + "\nEND"
}

func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return s[:i]
	}
	return s
}

// UnquoteLiteral strips the quotes from a SQL string literal and undoes the
// doubling that escapes a quote inside one.
//
// Parsers have to agree about this: the schema holds the value, not the
// literal, and every generator quotes it again for its own dialect. Two of them
// kept the quotes, so a default of 'active' reached PostgreSQL as a pair of
// empty strings around a bare word, and Oracle as a value wrapped twice more.
func UnquoteLiteral(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 2 || v[0] != '\'' || v[len(v)-1] != '\'' {
		return v
	}
	return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
}

// CommentOn renders the COMMENT ON statements PostgreSQL and Oracle use.
//
// A comment is documentation the author wrote, and every parser here reads one
// while no generator wrote one, so it was carried into the schema and dropped
// on the way out.
func CommentOn(object, name, comment string) string {
	if comment == "" {
		return ""
	}
	return fmt.Sprintf("COMMENT ON %s %s IS '%s';", object, name,
		strings.ReplaceAll(comment, "'", "''"))
}

// PartialIndexNote states a filter the target cannot hold.
//
// MySQL and Oracle have no partial index, so one widens into a full index on
// the way in, and that is not the same object: a UNIQUE one becomes stricter
// than the source and starts rejecting rows that were legal before. Dropping
// the clause silently made that invisible, so the condition is written above
// the index as a comment.
func PartialIndexNote(name, condition string, unique bool) string {
	if condition == "" {
		return ""
	}
	kind := "index"
	if unique {
		kind = "unique index"
	}
	return fmt.Sprintf("-- %s %s was partial in the source and is not here: WHERE %s\n", kind, name, condition)
}

// ForeignType states a user-defined type the target cannot build.
//
// A type's definition is dialect-specific text in the same way a routine body
// is: an Oracle OBJECT is not a PostgreSQL composite, and writing one into the
// other produced "CREATE TYPE addr_t AS (OBJECT (...))", which does not load.
// It is stated with its provenance rather than emitted broken.
func ForeignType(source, name, kind, definition string) string {
	// An alias names an existing type rather than describing a shape, and SQL
	// Server writes it with FROM. Showing it with AS states a statement the
	// source never wrote.
	keyword := "AS"
	if strings.EqualFold(kind, "ALIAS") {
		keyword = "FROM"
	}
	sql := fmt.Sprintf("CREATE TYPE %s %s %s;", name, keyword, definition)
	if source == "" {
		source = "source"
	}
	header := fmt.Sprintf(
		"Defined by the %s source. A type definition is dialect-specific and is\nnot translated, so it is left commented out. Port it by hand.",
		source)
	return Comment(header + "\n\n" + strings.TrimRight(sql, "\n"))
}

// generatedHeadRe finds where a computed column's clause begins.
//
// Every dialect has one and each spells it a little differently: PostgreSQL and
// Oracle write GENERATED ALWAYS AS, MySQL and SQLite accept that or a bare AS,
// and SQL Server writes only AS and states no data type at all. None of them
// was read. A column declared GENERATED ALWAYS AS (a * 2) arrived as a plain
// column that computes nothing, and SQL Server's form was read as though
// "AS (a * 2)" were the type, which then went into the output and would not
// load.
//
// Only the head is matched. Where the expression ends is decided by counting
// parentheses, because a call inside it has parentheses of its own and a regex
// would stop at the first closing one.
var generatedHeadRe = regexp.MustCompile(`(?is)(?:GENERATED\s+ALWAYS\s+)?\bAS\s*\(`)

// TakeGenerated strips a computed column's clause off a definition.
//
// It returns the definition without the clause, so the caller reads the rest
// with its own column reader, plus the expression and whether the value is
// stored. A dialect that offers the choice says STORED, VIRTUAL or, in SQL
// Server, PERSISTED; one that does not is read as computed, and each generator
// writes whichever of the two its own target supports.
func TakeGenerated(def string) (rest, expression string, stored, ok bool) {
	// The clause is located in a copy whose string contents are blanked, so a
	// default whose value happens to read like one, DEFAULT 'as (x)', is not
	// mistaken for it. The offsets still hold: blanking preserves length.
	loc := generatedHeadRe.FindStringIndex(keyword.BlankStringLiterals(def))
	if loc == nil {
		return def, "", false, false
	}

	// The expression ends at the parenthesis that closes the one the clause
	// opened, not at the first one: a call inside it has parentheses of its own.
	depth, end, inString := 0, -1, false
	for i := loc[1] - 1; i < len(def); i++ {
		switch c := def[i]; {
		case c == '\'':
			inString = !inString
		case inString:
		case c == '(':
			depth++
		case c == ')':
			if depth--; depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return def, "", false, false
	}

	// The expression is normalised, so what the schema carries is the same
	// whichever dialect stated it. mysqldump writes its own parentheses around
	// the whole thing and SSMS brackets every name, and both then went into the
	// output on top of the parentheses the generators add.
	expression = expr.Normalize(strings.TrimSpace(def[loc[1]:end]))
	tail := strings.TrimSpace(def[end+1:])
	// The keyword right after the expression says where the value lives, when
	// the dialect offers the choice.
	word := firstWord(tail)
	switch strings.ToUpper(word) {
	case "STORED", "PERSISTED":
		stored = true
		tail = strings.TrimSpace(tail[len(word):])
	case "VIRTUAL":
		tail = strings.TrimSpace(tail[len(word):])
	}

	rest = strings.TrimSpace(strings.TrimSpace(def[:loc[0]]) + " " + tail)
	return rest, expression, stored, expression != ""
}

// UntypedGeneratedNote states a computed column the target cannot declare.
//
// SQL Server states no data type on a computed column: it infers one from the
// expression. PostgreSQL, MySQL and Oracle all require one, and there is no
// honest way to work it out here, since "a * 2" is an integer or a decimal
// depending on what a is. Guessing produces a schema that differs from the
// source without saying so, so the column is stated instead and left out of the
// table.
func UntypedGeneratedNote(source, table, column, expression string) string {
	if source == "" {
		source = "source"
	}
	return Comment(fmt.Sprintf(
		"Column %s.%s is computed and the %s source states no type for it, which\n"+
			"this target requires. Add the type and uncomment:\n\n"+
			"  ALTER TABLE %s ADD COLUMN %s <type> GENERATED ALWAYS AS (%s) STORED;",
		table, column, source, table, column, expression)) + "\n"
}

// DeferrableClause renders a constraint's deferral for a target that has one.
//
// It was read by nothing and written by nothing. Deferral says the constraint is
// checked at the end of the transaction rather than at each statement, which is
// what lets a pair of rows referencing each other be inserted at all: losing it
// turns a schema that works into one that rejects its own data.
func DeferrableClause(deferrable bool, initially string) string {
	if !deferrable {
		return ""
	}
	if strings.EqualFold(initially, "DEFERRED") {
		return " DEFERRABLE INITIALLY DEFERRED"
	}
	return " DEFERRABLE INITIALLY IMMEDIATE"
}

// DeferrableNote states a deferral the target cannot hold.
func DeferrableNote(name string, initially string) string {
	which := name
	if which == "" {
		which = "a constraint"
	}
	// The leading newline is part of the note: it is written after whatever the
	// generator last emitted, which does not always end its own line.
	return fmt.Sprintf("\n-- not carried, this target checks every constraint per statement: %s was DEFERRABLE INITIALLY %s\n",
		which, strings.ToUpper(initially))
}
