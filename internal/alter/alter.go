// Package alter reads the ALTER statements a schema file carries.
//
// A dump is not the only thing handed to this converter: a hand-written
// migration is DDL too, and it states most of its schema in ALTER rather than
// in CREATE. Every dialect used to recognise only the few forms its own dump
// tool emits, so an ALTER TABLE ADD COLUMN was read by one of the five and
// silently discarded by the other four. The column simply did not appear in the
// output, and nothing said so.
//
// The forms do not conflict between dialects, so they are recognised here once
// rather than five times: MySQL's CHANGE means nothing in PostgreSQL and
// Oracle's parenthesised ADD means nothing in SQL Server, but reading them
// costs nothing and five copies of the same regex drift apart. What each
// dialect does with the result is its own business, because applying a column
// definition needs that dialect's own reader.
package alter

import (
	"regexp"
	"strings"

	"github.com/mstgnz/sqlmapper/internal/sqlfmt"
)

// Action is what an ALTER statement asks for.
type Action int

const (
	// None means the statement is not one this package reads. ADD CONSTRAINT
	// and its relatives are deliberately in that set: each dialect already
	// reads its own, with its own constraint model.
	None Action = iota
	AddColumn
	DropColumn
	RenameColumn
	RenameTable
	ModifyColumn
	DropConstraint
)

// Property names the single attribute a statement changes. PostgreSQL states
// one at a time, so reading "ALTER COLUMN email SET NOT NULL" as a whole column
// definition would silently drop the column's type and default.
type Property int

const (
	// WholeColumn means the statement restates the column, which is what MySQL,
	// Oracle and SQL Server do.
	WholeColumn Property = iota
	SetType
	SetNotNull
	DropNotNull
	SetDefault
	DropDefault
)

// Statement is one ALTER, read.
type Statement struct {
	Action Action
	Table  string

	// Names holds the columns to drop, or the single constraint name for
	// DropConstraint, or the column being renamed or modified.
	Names []string

	// NewName is the rename target, for RenameColumn and RenameTable.
	NewName string

	// Definitions holds a column definition per column, for AddColumn and
	// ModifyColumn. Oracle states several in one statement. For the property
	// forms it holds the one value the property carries, if it has one.
	Definitions []string

	// Property is what a ModifyColumn changes. It is WholeColumn unless the
	// source stated a single attribute.
	Property Property
}

var (
	// The leading table name. PostgreSQL writes ONLY, and every dialect quotes
	// the name its own way.
	alterHead = `(?is)^\s*ALTER\s+TABLE\s+(?:ONLY\s+)?(?:IF\s+EXISTS\s+)?([\[\]"` + "`" + `\w.]+)\s+`

	// ADD CONSTRAINT and the anonymous constraint forms belong to the dialects,
	// which already read them into their own constraint model.
	notAColumn = regexp.MustCompile(`(?is)^\s*(CONSTRAINT|PRIMARY\s+KEY|FOREIGN\s+KEY|UNIQUE|CHECK|INDEX|KEY|FULLTEXT|SPATIAL)\b`)

	addRe          = regexp.MustCompile(alterHead + `ADD\s+(?:COLUMN\s+)?(.+?)\s*;?\s*$`)
	dropColumnRe   = regexp.MustCompile(alterHead + `DROP\s+(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?([\[\]"` + "`" + `\w]+)\s*(?:CASCADE|RESTRICT)?\s*;?\s*$`)
	dropParenRe    = regexp.MustCompile(alterHead + `DROP\s+\(([^)]*)\)\s*;?\s*$`)
	dropConstraint = regexp.MustCompile(alterHead + `DROP\s+(?:CONSTRAINT|FOREIGN\s+KEY|INDEX|KEY)\s+(?:IF\s+EXISTS\s+)?([\[\]"` + "`" + `\w]+)\s*(?:CASCADE|RESTRICT)?\s*;?\s*$`)
	renameColumnRe = regexp.MustCompile(alterHead + `RENAME\s+COLUMN\s+([\[\]"` + "`" + `\w]+)\s+TO\s+([\[\]"` + "`" + `\w]+)\s*;?\s*$`)
	renameTableRe  = regexp.MustCompile(alterHead + `RENAME\s+(?:TO|AS)\s+([\[\]"` + "`" + `\w.]+)\s*;?\s*$`)

	// MySQL CHANGE renames and retypes in one statement, so it is both a rename
	// and a modify. The rename is what would otherwise be lost.
	changeRe = regexp.MustCompile(alterHead + `CHANGE\s+(?:COLUMN\s+)?([\[\]"` + "`" + `\w]+)\s+([\[\]"` + "`" + `\w]+)\s+(.+?)\s*;?\s*$`)

	// MODIFY is MySQL and Oracle; ALTER COLUMN is PostgreSQL and SQL Server.
	modifyRe = regexp.MustCompile(alterHead + `(?:MODIFY|ALTER)\s+(?:COLUMN\s+)?(.+?)\s*;?\s*$`)

	// The PostgreSQL sub-forms, which state one property rather than a whole
	// column definition.
	pgSetTypeRe    = regexp.MustCompile(`(?is)^([\[\]"` + "`" + `\w]+)\s+(?:SET\s+DATA\s+)?TYPE\s+(.+?)\s*(?:USING\s+.+)?$`)
	pgSetNotNullRe = regexp.MustCompile(`(?is)^([\[\]"` + "`" + `\w]+)\s+SET\s+NOT\s+NULL\s*$`)
	pgDropNotNull  = regexp.MustCompile(`(?is)^([\[\]"` + "`" + `\w]+)\s+DROP\s+NOT\s+NULL\s*$`)
	pgSetDefaultRe = regexp.MustCompile(`(?is)^([\[\]"` + "`" + `\w]+)\s+SET\s+DEFAULT\s+(.+?)\s*$`)
	pgDropDefault  = regexp.MustCompile(`(?is)^([\[\]"` + "`" + `\w]+)\s+DROP\s+DEFAULT\s*$`)

	// SQL Server has no RENAME of its own and scripts one as a stored procedure
	// call, which is why it never looked like an ALTER at all.
	spRenameRe = regexp.MustCompile(`(?is)^\s*EXEC(?:UTE)?\s+(?:sys\.)?sp_rename\s+\(?\s*'([^']+)'\s*,\s*'([^']+)'\s*(?:,\s*'([^']+)'\s*)?\)?\s*;?\s*$`)
)

// Unquote strips whichever quoting a dialect put around a name. All three are
// stripped rather than one, because this recogniser reads every dialect's
// statements and a name is never legitimately wrapped in another's quotes.
func Unquote(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`\"[]")
	return strings.TrimSpace(name)
}

// unqualify reduces a possibly schema-qualified name to its last part, the way
// every table name in this converter is written.
func unqualify(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return Unquote(name)
}

// Parse reads one statement. The second return is false when the statement is
// not an ALTER this package handles, which is the common case: a schema file is
// mostly CREATE.
func Parse(stmt string) (Statement, bool) {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return Statement{}, false
	}

	if m := spRenameRe.FindStringSubmatch(stmt); m != nil {
		return fromSpRename(m)
	}

	upper := strings.ToUpper(stmt)
	if !strings.HasPrefix(strings.TrimSpace(upper), "ALTER TABLE") {
		return Statement{}, false
	}

	// The order matters: DROP CONSTRAINT has to be tried before DROP COLUMN,
	// which would otherwise read the constraint keyword as a column name.
	if m := dropConstraint.FindStringSubmatch(stmt); m != nil {
		return Statement{Action: DropConstraint, Table: unqualify(m[1]), Names: []string{Unquote(m[2])}}, true
	}
	if m := renameColumnRe.FindStringSubmatch(stmt); m != nil {
		return Statement{Action: RenameColumn, Table: unqualify(m[1]),
			Names: []string{Unquote(m[2])}, NewName: Unquote(m[3])}, true
	}
	if m := renameTableRe.FindStringSubmatch(stmt); m != nil {
		return Statement{Action: RenameTable, Table: unqualify(m[1]), NewName: unqualify(m[2])}, true
	}
	if m := changeRe.FindStringSubmatch(stmt); m != nil {
		return Statement{Action: RenameColumn, Table: unqualify(m[1]),
			Names: []string{Unquote(m[2])}, NewName: Unquote(m[3]),
			Definitions: []string{Unquote(m[3]) + " " + strings.TrimSpace(m[4])}}, true
	}
	if m := dropParenRe.FindStringSubmatch(stmt); m != nil {
		return Statement{Action: DropColumn, Table: unqualify(m[1]), Names: names(m[2])}, true
	}
	if m := dropColumnRe.FindStringSubmatch(stmt); m != nil {
		return Statement{Action: DropColumn, Table: unqualify(m[1]), Names: []string{Unquote(m[2])}}, true
	}
	if m := addRe.FindStringSubmatch(stmt); m != nil {
		if body := strings.TrimSpace(m[2]); !notAColumn.MatchString(body) {
			return Statement{Action: AddColumn, Table: unqualify(m[1]), Definitions: definitions(body)}, true
		}
		return Statement{}, false
	}
	if m := modifyRe.FindStringSubmatch(stmt); m != nil {
		return fromModify(unqualify(m[1]), strings.TrimSpace(m[2]))
	}

	return Statement{}, false
}

// fromModify reads the body of a MODIFY or ALTER COLUMN. PostgreSQL states one
// property at a time and the other dialects restate the whole column, so the
// property forms are tried first and the rest is taken as a definition.
func fromModify(table, body string) (Statement, bool) {
	body = strings.TrimSuffix(strings.TrimSpace(body), ";")

	if m := pgSetTypeRe.FindStringSubmatch(body); m != nil {
		return Statement{Action: ModifyColumn, Property: SetType, Table: table,
			Names: []string{Unquote(m[1])}, Definitions: []string{strings.TrimSpace(m[2])}}, true
	}
	if m := pgSetNotNullRe.FindStringSubmatch(body); m != nil {
		return Statement{Action: ModifyColumn, Property: SetNotNull, Table: table,
			Names: []string{Unquote(m[1])}}, true
	}
	if m := pgDropNotNull.FindStringSubmatch(body); m != nil {
		return Statement{Action: ModifyColumn, Property: DropNotNull, Table: table,
			Names: []string{Unquote(m[1])}}, true
	}
	if m := pgSetDefaultRe.FindStringSubmatch(body); m != nil {
		return Statement{Action: ModifyColumn, Property: SetDefault, Table: table,
			Names: []string{Unquote(m[1])}, Definitions: []string{strings.TrimSpace(m[2])}}, true
	}
	if m := pgDropDefault.FindStringSubmatch(body); m != nil {
		return Statement{Action: ModifyColumn, Property: DropDefault, Table: table,
			Names: []string{Unquote(m[1])}}, true
	}

	defs := definitions(body)
	if len(defs) == 0 {
		return Statement{}, false
	}
	out := Statement{Action: ModifyColumn, Table: table, Definitions: defs}
	for _, d := range defs {
		fields := strings.Fields(d)
		if len(fields) == 0 {
			return Statement{}, false
		}
		out.Names = append(out.Names, Unquote(fields[0]))
	}
	return out, true
}

// fromSpRename reads sp_rename, which SQL Server uses for both a column and a
// table. The third argument says which, and its absence means a table.
func fromSpRename(m []string) (Statement, bool) {
	target, newName, kind := m[1], Unquote(m[2]), strings.ToUpper(strings.TrimSpace(m[3]))

	if kind == "COLUMN" {
		// The old name arrives qualified by its table: 't.email'.
		i := strings.LastIndex(target, ".")
		if i < 0 {
			return Statement{}, false
		}
		return Statement{Action: RenameColumn, Table: unqualify(target[:i]),
			Names: []string{Unquote(target[i+1:])}, NewName: newName}, true
	}
	if kind != "" && kind != "OBJECT" {
		// INDEX, DATABASE and the rest are not a table shape.
		return Statement{}, false
	}
	return Statement{Action: RenameTable, Table: unqualify(target), NewName: newName}, true
}

// definitions splits a column list that may hold several definitions, and drops
// the parentheses Oracle wraps them in.
func definitions(body string) []string {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "(") && strings.HasSuffix(body, ")") {
		body = strings.TrimSpace(body[1 : len(body)-1])
	}
	var out []string
	for _, part := range sqlfmt.SplitTopLevelCommas(body) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// names reads a bare column list, which is what Oracle's parenthesised DROP
// carries.
func names(body string) []string {
	var out []string
	for _, part := range strings.Split(body, ",") {
		if part = Unquote(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
