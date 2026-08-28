// Package routine renders the functions, procedures and triggers a schema was
// parsed with, for a target that cannot execute them.
package routine

import (
	"strings"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/internal/sqlfmt"
)

// ForeignSQL returns the schema's routines commented out, each with a note
// saying where it came from and why it was not translated.
//
// Every dialect renders this the same way, because it renders the routine as
// the SOURCE wrote it rather than as the target would. The target's shape does
// not fit: PostgreSQL stores the name of the function a trigger calls in the
// field MySQL uses for a procedural body, so a MySQL trigger rendered as
// PostgreSQL comes out as EXECUTE FUNCTION BEGIN.
func ForeignSQL(schema *sqlmapper.Schema) string {
	source := string(schema.SourceDialect)
	if source == "" {
		source = "source"
	}

	var sb strings.Builder
	for _, stmt := range sourceStatements(schema) {
		sb.WriteString(sqlfmt.ForeignRoutine(source, stmt))
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// Count reports how many routines the schema holds, so a generator can skip the
// section without building it.
func Count(schema *sqlmapper.Schema) int {
	return len(schema.Functions) + len(schema.Procedures) + len(schema.Triggers)
}

func sourceStatements(schema *sqlmapper.Schema) []string {
	var out []string

	for _, fn := range schema.Functions {
		kind := "FUNCTION"
		if fn.IsProc {
			kind = "PROCEDURE"
		}
		header := "CREATE " + kind + " " + fn.Name + "(" + Params(fn.Parameters) + ")"
		if !fn.IsProc && fn.Returns != "" {
			header += " RETURNS " + fn.Returns
		}
		out = append(out, sqlfmt.SourceRoutineSQL(header, fn.Body))
	}

	for _, pr := range schema.Procedures {
		header := "CREATE PROCEDURE " + pr.Name + "(" + Params(pr.Parameters) + ")"
		out = append(out, sqlfmt.SourceRoutineSQL(header, pr.Body))
	}

	for _, tr := range schema.Triggers {
		header := sqlfmt.TriggerHeader(tr.Name, tr.Timing, tr.Event, tr.Table, tr.ForEachRow)
		out = append(out, sqlfmt.SourceRoutineSQL(header, triggerBody(schema.SourceDialect, tr.Body)))
	}

	return out
}

// Params renders a parameter list in the form every dialect accepts.
func Params(params []sqlmapper.Parameter) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		part := p.Name + " " + p.DataType
		if p.Direction != "" {
			part = p.Direction + " " + part
		}
		parts = append(parts, strings.TrimSpace(part))
	}
	return strings.Join(parts, ", ")
}

// UnsupportedSQL comments out the schema's functions and procedures with a
// reason of the target's choosing. It is for a target that cannot express them
// at all, which is a different thing from a body it cannot translate.
func UnsupportedSQL(schema *sqlmapper.Schema, reason string) string {
	var sb strings.Builder
	for _, stmt := range sourceStatements(&sqlmapper.Schema{
		Functions:  schema.Functions,
		Procedures: schema.Procedures,
	}) {
		sb.WriteString(sqlfmt.UnsupportedRoutine(reason, stmt))
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// triggerBody renders a trigger body the way its source dialect meant it.
//
// The field does not hold the same thing everywhere: PostgreSQL keeps the name
// of the function the trigger runs, while the others keep procedural code, with
// only some of them keeping the BEGIN and END around it.
func triggerBody(source sqlmapper.DatabaseType, body string) string {
	b := strings.TrimSpace(body)
	if b == "" {
		return ""
	}
	if source == sqlmapper.PostgreSQL {
		if i := strings.Index(b, "("); i > 0 {
			b = b[:i]
		}
		return "EXECUTE FUNCTION " + strings.TrimSpace(b) + "()"
	}
	return sqlfmt.BlockBody(b)
}
