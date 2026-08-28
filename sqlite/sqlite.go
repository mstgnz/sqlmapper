// Package sqlite provides functionality for parsing and generating SQLite database schemas.
// It implements the Parser interface for handling SQLite specific SQL syntax and schema structures.
package sqlite

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/mstgnz/sqlmapper/internal/alter"
	"github.com/mstgnz/sqlmapper/internal/expr"
	"github.com/mstgnz/sqlmapper/internal/keyword"
	"github.com/mstgnz/sqlmapper/internal/routine"
	"github.com/mstgnz/sqlmapper/internal/sqlfmt"
	"github.com/mstgnz/sqlmapper/internal/sqlsplit"

	"github.com/mstgnz/sqlmapper"
)

// SQLite represents a SQLite parser implementation that handles parsing and generating
// SQLite database schemas. It maintains an internal schema representation and provides
// methods for converting between SQLite SQL and the common schema format.
type SQLite struct {
	schema *sqlmapper.Schema
	buf    *bytes.Buffer
}

// NewSQLite creates and initializes a new SQLite parser instance.
// It returns a parser that can handle SQLite specific SQL syntax and schema structures.
func NewSQLite() sqlmapper.Database {
	return &SQLite{
		schema: &sqlmapper.Schema{SourceDialect: sqlmapper.SQLite},
		buf:    &bytes.Buffer{},
	}
}

// Parse takes a SQLite SQL dump content and parses it into a common schema structure.
// It processes various SQLite objects including:
// - Tables with columns and constraints
// - Indexes (including UNIQUE indexes)
// - Views
// - Triggers
//
// Parameters:
//   - content: The SQLite SQL dump content to parse
//
// Returns:
//   - *sqlmapper.Schema: The parsed schema structure
//   - error: An error if parsing fails or if the content is empty
func (s *SQLite) Parse(content string) (*sqlmapper.Schema, error) {
	if content == "" {
		return nil, fmt.Errorf("empty content")
	}

	s.buf = bytes.NewBuffer([]byte(content))
	s.schema = &sqlmapper.Schema{SourceDialect: sqlmapper.SQLite}

	// Splitting on every semicolon cut a trigger body at its first inner
	// statement: the trigger was still registered, but its body arrived empty.
	// The splitter knows a delimiter inside a body, a string or a comment is not
	// a terminator, and it drops comments so a statement is not hidden behind
	// one.
	splitter := sqlsplit.New(strings.NewReader(content), ";")

	// ALTER is held back and replayed once every table is read: a rename or a
	// drop has to find what it names. Nothing read one before, so a schema file
	// that added a column in an ALTER lost it without a word.
	var deferredAlters []string

	for {
		raw, err := splitter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading statement: %v", err)
		}

		stmt := bytes.TrimSpace([]byte(raw))
		if len(stmt) == 0 {
			continue
		}

		upperStmt := bytes.ToUpper(stmt)

		if keyword.HasPrefixBytes(upperStmt, "ALTER TABLE") {
			deferredAlters = append(deferredAlters, string(stmt))
			continue
		}

		switch {
		case keyword.HasPrefixBytes(upperStmt, "CREATE TABLE"):
			table, err := s.parseCreateTable(stmt)
			if err != nil {
				return nil, fmt.Errorf("error parsing CREATE TABLE: %v", err)
			}
			// sqlite_sequence is SQLite's own bookkeeping for AUTOINCREMENT.
			// It appears in .schema output, it is not part of anybody's schema,
			// and the target creates its own equivalent if it needs one.
			if isSQLiteInternalTable(table.Name) {
				continue
			}
			s.schema.Tables = append(s.schema.Tables, table)

		case bytes.HasPrefix(upperStmt, []byte("CREATE INDEX")) || bytes.HasPrefix(upperStmt, []byte("CREATE UNIQUE INDEX")):
			if err := s.parseCreateIndex(stmt); err != nil {
				return nil, fmt.Errorf("error parsing CREATE INDEX: %v", err)
			}

		case bytes.HasPrefix(upperStmt, []byte("CREATE VIEW")):
			view, err := s.parseCreateView(stmt)
			if err != nil {
				return nil, fmt.Errorf("error parsing CREATE VIEW: %v", err)
			}
			s.schema.Views = append(s.schema.Views, view)

		case bytes.HasPrefix(upperStmt, []byte("CREATE TRIGGER")):
			trigger, err := s.parseCreateTrigger(stmt)
			if err != nil {
				return nil, fmt.Errorf("error parsing CREATE TRIGGER: %v", err)
			}
			s.schema.Triggers = append(s.schema.Triggers, trigger)
		}
	}

	// SQLite has ADD COLUMN, DROP COLUMN and both renames, and none of the
	// forms that restate a column: there is no MODIFY and no DROP CONSTRAINT.
	alter.ApplyAll(s.schema, deferredAlters, alter.Reader{
		Column: func(def string) (sqlmapper.Column, error) {
			col, _, ok := parseSQLiteColumn([]byte(def))
			if !ok {
				return sqlmapper.Column{}, fmt.Errorf("not a column definition: %q", def)
			}
			return col, nil
		},
	})

	return s.schema, nil
}

// parseCreateTable parses a CREATE TABLE statement and returns a Table structure.
func (s *SQLite) parseCreateTable(stmt []byte) (sqlmapper.Table, error) {
	table := sqlmapper.Table{}

	// Extract table name. Reading the third field breaks on the form SQLite's
	// own .schema writes for its bookkeeping table, CREATE TABLE
	// sqlite_sequence(name,seq), where the name runs straight into the column
	// list.
	m := sqliteTableHeaderRe.FindSubmatch(stmt)
	if m == nil {
		return table, fmt.Errorf("invalid CREATE TABLE statement")
	}
	tableName := string(bytes.Trim(m[1], "`\"[]"))
	if idx := strings.LastIndex(tableName, "."); idx != -1 {
		tableName = tableName[idx+1:]
	}
	table.Name = tableName

	// Extract columns and table options
	// The two indexes have to be in order as well as present. Checking only
	// that each was found let a malformed statement, ")(", slice backwards and
	// take the process down with it.
	startIdx := bytes.Index(stmt, []byte("("))
	endIdx := bytes.LastIndex(stmt, []byte(")"))
	if startIdx == -1 || endIdx <= startIdx {
		return table, fmt.Errorf("no columns found in CREATE TABLE statement")
	}

	// Parse columns. Splitting on every comma would cut a definition in half at
	// the comma inside CHECK (status IN ('a','b')) or DECIMAL(10,2), so the split
	// tracks parentheses and string literals.
	columnDefs := splitTopLevelCommas(bytes.TrimSpace(stmt[startIdx+1 : endIdx]))
	for _, colDef := range columnDefs {
		colDef = bytes.TrimSpace(colDef)
		if len(colDef) == 0 {
			continue
		}

		// A table constraint rather than a column. These used to be skipped and
		// forgotten, so every primary key, foreign key, unique and check
		// declared at table level was lost on the way out.
		if c, ok := parseSQLiteTableConstraint(colDef); ok {
			table.Constraints = append(table.Constraints, c)
			continue
		}

		if column, inlineFK, ok := parseSQLiteColumn(colDef); ok {
			table.Columns = append(table.Columns, column)
			if inlineFK != nil {
				table.Constraints = append(table.Constraints, *inlineFK)
			}
		}
	}

	return table, nil
}

// sqliteTableHeaderRe reads the table name, which may run straight into the
// column list with no space between them.
var sqliteTableHeaderRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:TEMP(?:ORARY)?\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([` + "`" + `"\[\].\w]+)`)

// sqliteConstraintRe splits an optional CONSTRAINT name off the front of a
// table constraint.
var sqliteConstraintRe = regexp.MustCompile(`(?is)^CONSTRAINT\s+([` + "`" + `"\[\]\w]+)\s+(.*)$`)

var (
	sqlitePKRe     = regexp.MustCompile(`(?is)^PRIMARY\s+KEY\s*\((.*)\)\s*$`)
	sqliteUniqueRe = regexp.MustCompile(`(?is)^UNIQUE\s*\((.*)\)\s*$`)
	sqliteCheckRe  = regexp.MustCompile(`(?is)^CHECK\s*\((.*)\)\s*$`)
	sqliteFKRe     = regexp.MustCompile(`(?is)^FOREIGN\s+KEY\s*\((.*?)\)\s+REFERENCES\s+([` + "`" + `"\[\].\w]+)\s*\((.*?)\)(.*)$`)
	sqliteOnDelRe  = regexp.MustCompile(`(?is)ON\s+DELETE\s+(CASCADE|SET\s+NULL|SET\s+DEFAULT|RESTRICT|NO\s+ACTION)`)
	sqliteOnUpdRe  = regexp.MustCompile(`(?is)ON\s+UPDATE\s+(CASCADE|SET\s+NULL|SET\s+DEFAULT|RESTRICT|NO\s+ACTION)`)
)

// parseSQLiteTableConstraint reads one table-level constraint, reporting false
// when the definition is a column instead.
func parseSQLiteTableConstraint(def []byte) (sqlmapper.Constraint, bool) {
	text := strings.TrimSpace(string(def))

	var c sqlmapper.Constraint
	if m := sqliteConstraintRe.FindStringSubmatch(text); m != nil {
		c.Name = strings.Trim(m[1], "`\"[]")
		text = strings.TrimSpace(m[2])
	}

	switch {
	case sqlitePKRe.MatchString(text):
		c.Type = "PRIMARY KEY"
		c.Columns = splitAndTrimNames(sqlitePKRe.FindStringSubmatch(text)[1])
	case sqliteUniqueRe.MatchString(text):
		c.Type = "UNIQUE"
		c.Columns = splitAndTrimNames(sqliteUniqueRe.FindStringSubmatch(text)[1])
	case sqliteCheckRe.MatchString(text):
		c.Type = "CHECK"
		c.CheckExpression = strings.TrimSpace(sqliteCheckRe.FindStringSubmatch(text)[1])
	case sqliteFKRe.MatchString(text):
		m := sqliteFKRe.FindStringSubmatch(text)
		c.Type = "FOREIGN KEY"
		c.Columns = splitAndTrimNames(m[1])
		c.RefTable = strings.Trim(m[2], "`\"[]")
		c.RefColumns = splitAndTrimNames(m[3])
		if r := sqliteOnDelRe.FindStringSubmatch(m[4]); r != nil {
			c.DeleteRule = strings.ToUpper(strings.Join(strings.Fields(r[1]), " "))
		}
		if r := sqliteOnUpdRe.FindStringSubmatch(m[4]); r != nil {
			c.UpdateRule = strings.ToUpper(strings.Join(strings.Fields(r[1]), " "))
		}
	default:
		return sqlmapper.Constraint{}, false
	}

	return c, true
}

// parseSQLiteColumn reads one column definition.
func parseSQLiteColumn(def []byte) (column sqlmapper.Column, inlineFK *sqlmapper.Constraint, ok bool) {
	// A computed column's clause comes off first: it carries parentheses and
	// keywords of its own that the rest of this reader would take for a type, a
	// default or a constraint.
	stripped, generatedExpr, generatedStored, isGenerated := sqlfmt.TakeGenerated(string(def))
	def = []byte(stripped)

	fields := bytes.Fields(def)
	if len(fields) < 2 {
		return sqlmapper.Column{}, nil, false
	}

	upper := bytes.ToUpper(def)
	column = sqlmapper.Column{
		Name:          string(bytes.Trim(fields[0], "`\"[]")),
		DataType:      string(bytes.ToUpper(fields[1])),
		IsNullable:    !bytes.Contains(upper, []byte("NOT NULL")),
		AutoIncrement: bytes.Contains(upper, []byte("AUTOINCREMENT")),
		IsPrimaryKey:  bytes.Contains(upper, []byte("PRIMARY KEY")),
		IsUnique:      bytes.Contains(upper, []byte("UNIQUE")),
	}

	// A type may carry its precision, as NUMERIC(10,2) does.
	if open := bytes.IndexByte(fields[1], '('); open != -1 {
		column.DataType = string(bytes.ToUpper(fields[1][:open]))
		inner := fields[1][open+1:]
		if close := bytes.IndexByte(inner, ')'); close != -1 {
			nums := strings.Split(string(inner[:close]), ",")
			column.Length = atoi(strings.TrimSpace(nums[0]))
			if len(nums) > 1 {
				column.Scale = atoi(strings.TrimSpace(nums[1]))
			}
		}
	}

	column.DefaultValue = sqliteDefaultValue(def)

	// A column may carry its own foreign key, "referred_by INTEGER REFERENCES
	// customers(id) ON DELETE SET NULL". Only the table-level form was read, so
	// an inline reference was lost.
	if m := sqliteInlineFKRe.FindSubmatch(def); m != nil {
		fk := sqlmapper.Constraint{
			Type:       "FOREIGN KEY",
			Columns:    []string{column.Name},
			RefTable:   strings.Trim(string(m[1]), "`\"[]"),
			RefColumns: splitAndTrimNames(string(m[2])),
		}
		if r := sqliteOnDelRe.FindSubmatch(m[3]); r != nil {
			fk.DeleteRule = strings.ToUpper(strings.Join(strings.Fields(string(r[1])), " "))
		}
		if r := sqliteOnUpdRe.FindSubmatch(m[3]); r != nil {
			fk.UpdateRule = strings.ToUpper(strings.Join(strings.Fields(string(r[1])), " "))
		}
		inlineFK = &fk
	}

	// A column may carry its own CHECK, "meta TEXT CHECK (json_valid(meta))".
	// Matching only a definition that is nothing but a CHECK meant a column
	// constraint was read as no constraint at all.
	if m := sqliteColumnCheckRe.FindSubmatch(def); m != nil {
		column.CheckExpression = strings.TrimSpace(string(m[1]))
	}

	// A computed column has no default of its own: whatever it computes is the
	// value.
	if isGenerated {
		column.GeneratedExpression = generatedExpr
		column.GeneratedStored = generatedStored
		column.DefaultValue = ""
	}

	return column, inlineFK, true
}

// sqliteDefaultValue reads what follows DEFAULT, which may be a quoted string, a
// call with its own parentheses, or a bare token.
//
// Reading up to the first space returned nothing at all when DEFAULT was the
// last thing on the line, because the space it found was the one before the
// value.
func sqliteDefaultValue(def []byte) string {
	idx := bytes.Index(keyword.UpperASCIIBytes(def), []byte("DEFAULT"))
	if idx == -1 {
		return ""
	}

	rest := strings.TrimSpace(string(def[idx+len("DEFAULT"):]))
	if rest == "" {
		return ""
	}

	if rest[0] == '\'' {
		// The schema holds the value, not the literal: every generator
		// quotes it again for its own dialect, and keeping the quotes here
		// corrupted the default in every cross-dialect conversion.
		if end := strings.IndexByte(rest[1:], '\''); end != -1 {
			return sqlfmt.UnquoteLiteral(rest[:end+2])
		}
		return sqlfmt.UnquoteLiteral(rest)
	}
	if rest[0] == '(' {
		depth := 0
		for i, c := range rest {
			switch c {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return rest[:i+1]
				}
			}
		}
		return rest
	}

	value := strings.Fields(rest)[0]
	// A call keeps its parentheses: now() is one token only if nothing splits it.
	if open := strings.IndexByte(value, '('); open != -1 && !strings.Contains(value, ")") {
		if end := strings.IndexByte(rest, ')'); end != -1 {
			return rest[:end+1]
		}
	}
	return value
}

// splitAndTrimNames splits a parenthesised identifier list.
func splitAndTrimNames(list string) []string {
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if name := strings.Trim(strings.TrimSpace(p), "`\"[]"); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// sqliteIndexHeaderRe reads the index name and target table out of a CREATE
// INDEX header, tolerating the optional UNIQUE and IF NOT EXISTS keywords.
// sqliteIndexWhereRe captures the condition of a partial index.
var sqliteIndexWhereRe = regexp.MustCompile(`(?is)\)\s*WHERE\s+(.+?)\s*;?\s*$`)

var sqliteIndexHeaderRe = regexp.MustCompile(`(?i)CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([` + "`" + `"\[\].\w]+)\s+ON\s+([` + "`" + `"\[\].\w]+)`)

// parseCreateIndex parses a CREATE INDEX statement and adds the index to the appropriate table.
func (s *SQLite) parseCreateIndex(stmt []byte) error {
	isUnique := bytes.HasPrefix(bytes.ToUpper(stmt), []byte("CREATE UNIQUE"))

	// Match the header rather than counting tokens: the optional UNIQUE and
	// IF NOT EXISTS keywords shift every fixed position, and reading the table
	// name off a fixed index turned "IF NOT EXISTS" into a table called EXISTS.
	m := sqliteIndexHeaderRe.FindSubmatch(stmt)
	if m == nil {
		return fmt.Errorf("invalid CREATE INDEX statement")
	}

	indexName := strings.Trim(string(m[1]), "`\"[]")
	tableName := strings.Trim(strings.TrimSpace(string(m[2])), "`\"[]")

	// Remove schema prefix if exists
	if idx := strings.LastIndexByte(tableName, '.'); idx != -1 {
		tableName = tableName[idx+1:]
	}

	// Extract columns
	startIdx := bytes.LastIndex(stmt, []byte("("))
	endIdx := bytes.LastIndex(stmt, []byte(")"))
	if startIdx == -1 || endIdx <= startIdx {
		return fmt.Errorf("no columns found in CREATE INDEX statement")
	}

	columns := s.splitAndTrim(string(bytes.TrimSpace(stmt[startIdx+1 : endIdx])))

	// SQLite indexes can be partial. The WHERE clause was read by nothing, so a
	// partial index came across as a full one, which indexes rows the source
	// deliberately left out.
	var condition string
	if m := sqliteIndexWhereRe.FindSubmatch(stmt); m != nil {
		condition = strings.TrimSpace(string(m[1]))
	}

	// Find the table and add the index
	for i, table := range s.schema.Tables {
		if table.Name == tableName {
			s.schema.Tables[i].Indexes = append(s.schema.Tables[i].Indexes, sqlmapper.Index{
				Name:      indexName,
				Columns:   columns,
				IsUnique:  isUnique,
				Condition: condition,
			})
			return nil
		}
	}

	return fmt.Errorf("table not found for index: %s", tableName)
}

// parseCreateView parses a CREATE VIEW statement and returns a View structure.
func (s *SQLite) parseCreateView(stmt []byte) (sqlmapper.View, error) {
	view := sqlmapper.View{}

	// Extract view name
	parts := bytes.Fields(stmt)
	if len(parts) < 3 {
		return view, fmt.Errorf("invalid CREATE VIEW statement")
	}

	viewName := string(bytes.Trim(parts[2], "`"))
	// Remove schema prefix if exists
	if idx := bytes.LastIndex(parts[2], []byte(".")); idx != -1 {
		viewName = string(bytes.Trim(parts[2][idx+1:], "`"))
	}
	view.Name = viewName

	// Extract view definition
	if idx := bytes.Index(keyword.UpperASCIIBytes(stmt), []byte(" AS ")); idx != -1 {
		view.Definition = string(bytes.TrimSpace(stmt[idx+4:]))
	}

	return view, nil
}

// parseCreateTrigger parses a CREATE TRIGGER statement and returns a Trigger structure.
func (s *SQLite) parseCreateTrigger(stmt []byte) (sqlmapper.Trigger, error) {
	trigger := sqlmapper.Trigger{}

	// Extract trigger name
	parts := bytes.Fields(stmt)
	if len(parts) < 3 {
		return trigger, fmt.Errorf("invalid CREATE TRIGGER statement")
	}

	triggerName := string(bytes.Trim(parts[2], "`"))
	trigger.Name = triggerName

	// Everything before BEGIN describes the trigger; everything after it is the
	// body. Splitting there first matters because the body has statements of its
	// own: a trigger that updates a row says UPDATE inside its body, and reading
	// the event off the whole statement picked that up instead of the header.
	header, body := stmt, []byte(nil)
	if loc := triggerBodyRe.FindSubmatchIndex(stmt); loc != nil {
		header = stmt[:loc[0]]
		body = bytes.TrimSpace(stmt[loc[2]:loc[3]])
	}
	trigger.Body = string(body)

	upperHeader := bytes.ToUpper(header)

	// Extract timing (BEFORE/AFTER)
	switch {
	case bytes.Contains(upperHeader, []byte("BEFORE")):
		trigger.Timing = "BEFORE"
	case bytes.Contains(upperHeader, []byte("AFTER")):
		trigger.Timing = "AFTER"
	case bytes.Contains(upperHeader, []byte("INSTEAD OF")):
		trigger.Timing = "INSTEAD OF"
	}

	// Extract event (INSERT/UPDATE/DELETE)
	switch {
	case bytes.Contains(upperHeader, []byte("INSERT")):
		trigger.Event = "INSERT"
	case bytes.Contains(upperHeader, []byte("UPDATE")):
		trigger.Event = "UPDATE"
	case bytes.Contains(upperHeader, []byte("DELETE")):
		trigger.Event = "DELETE"
	}

	trigger.ForEachRow = bytes.Contains(upperHeader, []byte("FOR EACH ROW"))

	// Extract table name
	if m := triggerTableRe.FindSubmatch(header); m != nil {
		tableName := m[1]
		// Remove schema prefix if exists
		if dotIdx := bytes.LastIndex(tableName, []byte(".")); dotIdx != -1 {
			tableName = tableName[dotIdx+1:]
		}
		trigger.Table = string(bytes.Trim(tableName, "`\""))
	}

	return trigger, nil
}

// triggerBodyRe captures a trigger body. The keywords are matched with word
// boundaries rather than surrounding spaces: real DDL puts a newline after
// BEGIN, and looking for " BEGIN " left every such trigger with an empty body.
var triggerBodyRe = regexp.MustCompile(`(?is)\bBEGIN\b(.*)\bEND\s*;?\s*$`)

// triggerTableRe captures the table a trigger is attached to, which is the word
// after ON in the header.
var triggerTableRe = regexp.MustCompile("(?is)\\bON\\s+([`\"\\w.]+)")

// splitAndTrim splits a string by commas and trims whitespace and backticks from each part.
func (s *SQLite) splitAndTrim(str string) []string {
	parts := strings.Split(str, ",")
	result := make([]string, len(parts))
	for i, part := range parts {
		result[i] = strings.Trim(strings.TrimSpace(part), "`")
	}
	return result
}

// Generate creates a SQLite SQL dump from a schema structure.
func (s *SQLite) Generate(schema *sqlmapper.Schema) (string, error) {
	if schema == nil {
		return "", fmt.Errorf("empty schema")
	}

	s.buf.Reset()

	// Dump tools do not order tables by dependency, so a child table can precede
	// its parent. SQLite has no ALTER TABLE ADD CONSTRAINT, so a foreign key it
	// cannot place inline cannot be added later either. It does allow a
	// reference to a table that does not exist yet, so the ones the other
	// dialects defer are written inline here and ordering is for the reader.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)

	for i, table := range tables {
		// SQLite has nowhere to put a comment, so what the source wrote is kept
		// as a comment on the file rather than dropped without trace.
		for _, c := range sqlmapper.CommentStatements(table) {
			fmt.Fprintf(s.buf, "-- %s %s: %s\n", strings.ToLower(c.Object), c.Name, c.Comment)
		}

		s.buf.WriteString(s.generateTableSQL(table, deferredFKs[table.Name]))
		s.buf.WriteString(";\n")

		for _, idx := range table.Indexes {
			s.buf.WriteString(s.generateIndexSQL(table.Name, idx))
			s.buf.WriteString(";\n")
		}

		if i < len(tables)-1 {
			s.buf.WriteByte('\n')
		}
	}

	// Views are emitted after the tables they select from.
	for _, view := range schema.Views {
		s.buf.WriteString("\n")
		s.buf.WriteString(s.generateViewSQL(view))
		s.buf.WriteString(";\n")
	}

	// Routines come after everything they can refer to.
	if routines := s.generateRoutinesSQL(schema); routines != "" {
		s.buf.WriteString("\n")
		s.buf.WriteString(routines)
	}

	if perms := s.generatePermissionsSQL(schema); perms != "" {
		s.buf.WriteString("\n")
		s.buf.WriteString(perms)
	}

	return s.buf.String(), nil
}

// generateTableSQL generates SQL for a table
func (s *SQLite) generateTableSQL(table sqlmapper.Table, extraFKs []sqlmapper.Constraint) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "CREATE TABLE %s (\n", table.Name)

	// A single auto-increment primary key is written on the column, because
	// AUTOINCREMENT is only legal directly after INTEGER PRIMARY KEY.
	rowidAlias := rowidAliasColumn(table)
	inlinePK := singleColumnPK(table)
	if inlinePK == rowidAlias {
		inlinePK = ""
	}

	parts := make([]string, 0, len(table.Columns)+len(table.Constraints))

	for _, col := range table.Columns {
		var def strings.Builder
		def.WriteString("    ")
		def.WriteString(col.Name)
		def.WriteString(" ")

		if col.Name == rowidAlias {
			def.WriteString("INTEGER PRIMARY KEY AUTOINCREMENT")
			parts = append(parts, def.String())
			continue
		}

		// A computed column states what it computes and nothing else: no
		// default, and SQLite refuses PRIMARY KEY on one. SQLite is also the
		// only target that takes a column with no type at all, which is what a
		// SQL Server source gives it, so the type is written only when there is
		// one.
		if col.GeneratedExpression != "" {
			if clause := expr.GeneratedColumn(col.GeneratedExpression, col.GeneratedStored, expr.SQLite); clause != "" {
				if t := sqliteColumnType(col); strings.TrimSpace(t) != "" {
					def.WriteString(t)
					def.WriteString(" ")
				}
				def.WriteString(clause)
				if !col.IsNullable {
					def.WriteString(" NOT NULL")
				}
				parts = append(parts, def.String())
				continue
			}
		}

		def.WriteString(sqliteColumnType(col))
		if col.Name == inlinePK {
			// A single-column key is declared here rather than at the end,
			// which is how SQLite gets its rowid alias.
			def.WriteString(" PRIMARY KEY")
		} else if !col.IsNullable {
			def.WriteString(" NOT NULL")
		}
		if col.IsUnique {
			def.WriteString(" UNIQUE")
		}
		if lit := sqliteDefaultLiteral(col.DefaultValue, sqliteColumnType(col)); lit != "" {
			def.WriteString(" DEFAULT ")
			def.WriteString(lit)
		}
		if col.CheckExpression != "" {
			def.WriteString(" CHECK (")
			def.WriteString(expr.Condition(col.CheckExpression, expr.SQLite))
			def.WriteString(")")
		}
		parts = append(parts, def.String())
	}

	for _, c := range table.Constraints {
		// The key is already on the column, so declaring it again would be a
		// second primary key.
		if c.Type == "PRIMARY KEY" && len(c.Columns) == 1 &&
			(c.Columns[0] == rowidAlias || c.Columns[0] == inlinePK) {
			continue
		}
		if sql := s.generateConstraintSQL(c); sql != "" {
			parts = append(parts, "    "+sql)
		}
	}

	// A foreign key that closes a cycle has nowhere else to go: SQLite cannot
	// add one afterwards, and it accepts a reference to a table declared later.
	for _, c := range extraFKs {
		if sql := s.generateConstraintSQL(c); sql != "" {
			parts = append(parts, "    "+sql)
		}
	}

	sb.WriteString(strings.Join(parts, ",\n"))
	sb.WriteString("\n)")

	return sb.String()
}

// generateIndexSQL generates SQL for an index
func (s *SQLite) generateIndexSQL(tableName string, index sqlmapper.Index) string {
	var sql string
	if index.IsUnique {
		sql = "CREATE UNIQUE INDEX "
	} else {
		sql = "CREATE INDEX "
	}

	sql += index.Name + " ON " + tableName + "(" + strings.Join(index.Columns, ", ") + ")"
	if index.Condition != "" {
		sql += " WHERE " + expr.Condition(index.Condition, expr.SQLite)
	}

	return sql
}

// stripSQLComments removes -- line comments and /* */ block comments while
// leaving string literals untouched. Statement splitting runs on the result:
// without this a comment sitting directly above a statement ends up glued to it
// and the whole statement is skipped as if it were a comment.
func stripSQLComments(content string) string {
	var out strings.Builder
	out.Grow(len(content))
	inString, inLine, inBlock := false, false, false

	for i := 0; i < len(content); i++ {
		c := content[i]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
				out.WriteByte(c)
			}
		case inBlock:
			if c == '*' && i+1 < len(content) && content[i+1] == '/' {
				inBlock = false
				i++
			}
		case inString:
			out.WriteByte(c)
			if c == '\'' {
				if i+1 < len(content) && content[i+1] == '\'' {
					out.WriteByte(content[i+1])
					i++
				} else {
					inString = false
				}
			}
		case c == '\'':
			inString = true
			out.WriteByte(c)
		case c == '-' && i+1 < len(content) && content[i+1] == '-':
			inLine = true
			i++
		case c == '/' && i+1 < len(content) && content[i+1] == '*':
			inBlock = true
			i++
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// splitTopLevelCommas splits a CREATE TABLE body on the commas that separate
// definitions, ignoring the ones nested inside parentheses or string literals.
func splitTopLevelCommas(body []byte) [][]byte {
	var parts [][]byte
	depth := 0
	inString := false
	start := 0

	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\'':
			if inString && i+1 < len(body) && body[i+1] == '\'' {
				i++
				continue
			}
			inString = !inString
		case '(':
			if !inString {
				depth++
			}
		case ')':
			if !inString {
				depth--
			}
		case ',':
			if !inString && depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}

	if start < len(body) {
		parts = append(parts, body[start:])
	}
	return parts
}

// atoi reads a base-10 integer out of a string that a pattern has already
// matched as digits. Nothing malformed can reach here, and zero is the right
// answer if anything ever does.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// generateRoutinesSQL renders the routine section of a dump.
//
// SQLite has triggers and nothing else: no stored functions, no procedures.
// Whatever the source defined is kept as a comment rather than dropped, so the
// reader knows what did not come across.
func (s *SQLite) generateRoutinesSQL(schema *sqlmapper.Schema) string {
	if routine.Count(schema) == 0 {
		return ""
	}

	var sb strings.Builder

	if schema.RoutinesAreNativeTo(sqlmapper.SQLite) {
		for _, tr := range schema.Triggers {
			stmt := sqlfmt.TriggerHeader(tr.Name, tr.Timing, tr.Event, tr.Table, tr.ForEachRow)
			if tr.Condition != "" {
				stmt += "\nWHEN " + tr.Condition
			}
			stmt += "\n" + sqlfmt.BlockBody(tr.Body)
			sb.WriteString(sqlfmt.Terminate(stmt, ";"))
			sb.WriteString("\n\n")
		}
	} else if len(schema.Triggers) > 0 {
		sb.WriteString(routine.ForeignSQL(&sqlmapper.Schema{
			SourceDialect: schema.SourceDialect,
			Triggers:      schema.Triggers,
		}))
	}

	if len(schema.Functions) > 0 || len(schema.Procedures) > 0 {
		sb.WriteString(routine.UnsupportedSQL(schema,
			"SQLite has no stored functions or procedures. This came from the source and is left commented out."))
	}

	return sb.String()
}

// toSQLiteType maps a type from any source dialect to the SQLite equivalent.
//
// SQLite has five storage classes and decides affinity from the name, so an
// unmapped type is not an error there. It is still wrong: a column written as
// "timestamp with time zone" or "jsonb" tells the reader the database supports
// something it does not, and the affinity SQLite infers from such a name is
// rarely the one intended.
var toSQLiteType = map[string]string{
	// Whole numbers. SQLite has one integer class, so width is not carried.
	"tinyint": "INTEGER", "smallint": "INTEGER", "mediumint": "INTEGER",
	"int": "INTEGER", "integer": "INTEGER", "bigint": "INTEGER",
	"int2": "INTEGER", "int4": "INTEGER", "int8": "INTEGER",
	"serial": "INTEGER", "smallserial": "INTEGER", "bigserial": "INTEGER",
	"number": "INTEGER", "year": "INTEGER",

	// SQLite has no boolean; it stores 0 and 1.
	"bool": "INTEGER", "boolean": "INTEGER", "bit": "INTEGER",

	// Approximate numbers.
	"real": "REAL", "float": "REAL", "float4": "REAL", "float8": "REAL",
	"double": "REAL", "double precision": "REAL",
	"binary_float": "REAL", "binary_double": "REAL",

	// Exact numbers keep their precision, which SQLite accepts and which says
	// what the column was for.
	"decimal": "NUMERIC", "numeric": "NUMERIC", "dec": "NUMERIC",
	"money": "NUMERIC", "smallmoney": "NUMERIC",

	// Text. Length is dropped: SQLite does not enforce it.
	"char": "TEXT", "nchar": "TEXT", "character": "TEXT",
	"varchar": "TEXT", "nvarchar": "TEXT", "varchar2": "TEXT", "nvarchar2": "TEXT",
	"character varying": "TEXT", "string": "TEXT",
	"text": "TEXT", "ntext": "TEXT", "tinytext": "TEXT",
	"mediumtext": "TEXT", "longtext": "TEXT",
	"clob": "TEXT", "nclob": "TEXT", "xml": "TEXT",
	"json": "TEXT", "jsonb": "TEXT",
	"uuid": "TEXT", "uniqueidentifier": "TEXT",
	"enum": "TEXT", "set": "TEXT",
	"inet": "TEXT", "cidr": "TEXT", "macaddr": "TEXT",

	// Dates and times. SQLite has no date type; ISO-8601 text is what its own
	// date functions read.
	"date": "TEXT", "time": "TEXT", "datetime": "TEXT", "datetime2": "TEXT",
	"smalldatetime": "TEXT", "datetimeoffset": "TEXT",
	"timestamp": "TEXT", "timestamptz": "TEXT",
	"timestamp with time zone": "TEXT", "timestamp without time zone": "TEXT",
	"time with time zone": "TEXT", "time without time zone": "TEXT",
	"interval": "TEXT",

	// Binary.
	"blob": "BLOB", "tinyblob": "BLOB", "mediumblob": "BLOB", "longblob": "BLOB",
	"bytea": "BLOB", "binary": "BLOB", "varbinary": "BLOB", "image": "BLOB",
	"raw": "BLOB", "long raw": "BLOB", "bfile": "BLOB",
}

// sqliteColumnType renders a column's type, with the precision that survives.
func sqliteColumnType(col sqlmapper.Column) string {
	// An array has no SQLite equivalent, so it travels as text the way it does
	// on the way to MySQL.
	if col.IsArray {
		return "TEXT"
	}

	// A column typed by a user-defined enum carries a name SQLite has never
	// heard of. It has no enum either, so the values live in text.
	if len(col.EnumValues) > 0 {
		return "TEXT"
	}

	name := strings.ToLower(strings.Join(strings.Fields(col.DataType), " "))
	if i := strings.Index(name, "("); i > 0 {
		name = strings.TrimSpace(name[:i])
	}

	mapped, ok := toSQLiteType[name]
	if !ok {
		// An unrecognised name is left as written rather than guessed at.
		// SQLite accepts any type name and applies its affinity rules.
		return strings.ToUpper(col.DataType)
	}

	// Numeric types carry their precision across, which SQLite accepts and which
	// records what the column was for. A length on TEXT is dropped because
	// SQLite does not enforce one, and INTEGER has no width to state.
	if (mapped == "NUMERIC" || mapped == "REAL") && col.Length > 0 {
		if col.Scale > 0 {
			return fmt.Sprintf("%s(%d,%d)", mapped, col.Length, col.Scale)
		}
		return fmt.Sprintf("%s(%d)", mapped, col.Length)
	}
	return mapped
}

// generateConstraintSQL renders a table constraint.
func (s *SQLite) generateConstraintSQL(c sqlmapper.Constraint) string {
	var sb strings.Builder
	if c.Name != "" {
		fmt.Fprintf(&sb, "CONSTRAINT %s ", c.Name)
	}
	switch c.Type {
	case "PRIMARY KEY":
		fmt.Fprintf(&sb, "PRIMARY KEY (%s)", strings.Join(c.Columns, ", "))
	case "FOREIGN KEY":
		fmt.Fprintf(&sb, "FOREIGN KEY (%s) REFERENCES %s (%s)",
			strings.Join(c.Columns, ", "), c.RefTable, strings.Join(c.RefColumns, ", "))
		if c.DeleteRule != "" {
			sb.WriteString(" ON DELETE ")
			sb.WriteString(c.DeleteRule)
		}
		if c.UpdateRule != "" {
			sb.WriteString(" ON UPDATE ")
			sb.WriteString(c.UpdateRule)
		}
	case "UNIQUE":
		fmt.Fprintf(&sb, "UNIQUE (%s)", strings.Join(c.Columns, ", "))
	case "CHECK":
		fmt.Fprintf(&sb, "CHECK (%s)", expr.Condition(c.CheckExpression, expr.SQLite))
	}
	return sb.String()
}

// rowidAliasColumn returns the column that becomes SQLite's rowid alias, or an
// empty string when the table has none.
//
// AUTOINCREMENT is only legal directly after INTEGER PRIMARY KEY on a single
// column, so that case is written inline and the table-level PRIMARY KEY is
// left out to avoid declaring the key twice.
func rowidAliasColumn(table sqlmapper.Table) string {
	var pk *sqlmapper.Constraint
	for i, c := range table.Constraints {
		if c.Type == "PRIMARY KEY" {
			pk = &table.Constraints[i]
			break
		}
	}

	for _, col := range table.Columns {
		if !col.AutoIncrement {
			continue
		}
		if col.IsPrimaryKey {
			return col.Name
		}
		if pk != nil && len(pk.Columns) == 1 && pk.Columns[0] == col.Name {
			return col.Name
		}
	}
	return ""
}

// singleColumnPK returns the column that holds the table's whole primary key,
// whether it was recorded on the column or as a table constraint. It is empty
// for a composite key, which has to be declared at the end.
func singleColumnPK(table sqlmapper.Table) string {
	for _, c := range table.Constraints {
		if c.Type != "PRIMARY KEY" {
			continue
		}
		if len(c.Columns) == 1 {
			return c.Columns[0]
		}
		return ""
	}

	var found string
	for _, col := range table.Columns {
		if !col.IsPrimaryKey {
			continue
		}
		if found != "" {
			return "" // composite
		}
		found = col.Name
	}
	return found
}

// isSQLiteInternalTable reports whether a table belongs to SQLite rather than to
// the schema. Their names are reserved: SQLite refuses to create one.
func isSQLiteInternalTable(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "sqlite_")
}

// generatePermissionsSQL states the grants SQLite cannot hold.
//
// SQLite has no users and no GRANT: access is whoever can open the file. The
// grants are written as comments rather than dropped, because a schema that
// silently loses its access control looks converted and is not.
func (s *SQLite) generatePermissionsSQL(schema *sqlmapper.Schema) string {
	var sb strings.Builder
	for _, perm := range schema.Permissions {
		user, _ := sqlmapper.GranteeParts(perm.Grantee)
		if user == "" || perm.Object == "" {
			continue
		}
		// The tables are written unqualified, so the grant that names them has
		// to be too: a grant carried out of PostgreSQL said "ON public.orders",
		// which names nothing on any other target.
		object := sqlmapper.StripSchemaPrefix(perm.Object)
		verb := "GRANT"
		if strings.EqualFold(perm.Type, "REVOKE") {
			verb = "REVOKE"
		}
		fmt.Fprintf(&sb, "-- not carried, SQLite has no access control: %s %s ON %s -> %s\n",
			verb, sqlmapper.PrivilegeList(perm.Privileges), object, user)
	}
	return sb.String()
}

// generateViewSQL renders a view definition, without its terminator.
//
// Both Generate and GenerateStream call it. The stream used to write the
// definition as it stood, so a view carrying another dialect's schema qualifier
// or a bare boolean column went out untranslated.
func (s *SQLite) generateViewSQL(view sqlmapper.View) string {
	body := expr.TranslateViewBody(strings.TrimSuffix(strings.TrimSpace(view.Definition), ";"), expr.SQLite)
	return fmt.Sprintf("CREATE VIEW %s AS %s", view.Name, body)
}

// sqliteDefaultLiteral renders a column default as SQLite spells it.
//
// The schema holds the value rather than the literal, so a string has to be
// quoted here. Passing it through unquoted wrote DEFAULT active, which SQLite
// reads as a column reference.
func sqliteDefaultLiteral(value, columnType string) string {
	// SQLite has no boolean and stores 0 and 1, so a boolean default belongs on
	// an INTEGER or NUMERIC column as a number.
	numeric := strings.HasPrefix(columnType, "INTEGER") ||
		strings.HasPrefix(columnType, "NUMERIC") ||
		strings.HasPrefix(columnType, "REAL")

	return expr.DefaultLiteral(value, expr.SQLite, expr.DefaultOptions{NumericColumn: numeric})
}

// sqliteInlineFKRe captures a foreign key written on the column rather than at
// the end of the table.
var sqliteInlineFKRe = regexp.MustCompile(`(?is)\bREFERENCES\s+([` + "`" + `"\[\].\w]+)\s*\(([^)]*)\)(.*)$`)

// sqliteColumnCheckRe captures the body of a CHECK written on a column.
var sqliteColumnCheckRe = regexp.MustCompile(`(?is)\bCHECK\s*\(((?:[^()]|\([^()]*\))*)\)`)
