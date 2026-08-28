// Package sqlfmt holds the small formatting rules the generators share.
package sqlfmt

import (
	"fmt"
	"strings"
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
