// Package sqlite provides functionality for parsing and generating SQLite database schemas.
// It implements the Parser interface for handling SQLite specific SQL syntax and schema structures.
package sqlite

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

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
		schema: &sqlmapper.Schema{},
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

	content = stripSQLComments(content)

	s.buf = bytes.NewBuffer([]byte(content))
	s.schema = &sqlmapper.Schema{}

	// Split content into statements
	statements := bytes.Split([]byte(content), []byte(";"))

	for _, stmt := range statements {
		stmt = bytes.TrimSpace(stmt)
		if len(stmt) == 0 {
			continue
		}

		// Skip comments
		if bytes.HasPrefix(stmt, []byte("--")) || bytes.HasPrefix(stmt, []byte("/*")) {
			continue
		}

		upperStmt := bytes.ToUpper(stmt)

		switch {
		case bytes.HasPrefix(upperStmt, []byte("CREATE TABLE")):
			table, err := s.parseCreateTable(stmt)
			if err != nil {
				return nil, fmt.Errorf("error parsing CREATE TABLE: %v", err)
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

	return s.schema, nil
}

// parseCreateTable parses a CREATE TABLE statement and returns a Table structure.
func (s *SQLite) parseCreateTable(stmt []byte) (sqlmapper.Table, error) {
	table := sqlmapper.Table{}

	// Extract table name
	parts := bytes.Fields(stmt)
	if len(parts) < 3 {
		return table, fmt.Errorf("invalid CREATE TABLE statement")
	}

	tableName := string(bytes.Trim(parts[2], "`"))
	// Remove schema prefix if exists
	if idx := bytes.LastIndex(parts[2], []byte(".")); idx != -1 {
		tableName = string(bytes.Trim(parts[2][idx+1:], "`"))
	}
	table.Name = tableName

	// Extract columns and table options
	startIdx := bytes.Index(stmt, []byte("("))
	endIdx := bytes.LastIndex(stmt, []byte(")"))
	if startIdx == -1 || endIdx == -1 {
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

		// Skip if it's a constraint or key definition
		upperColDef := bytes.ToUpper(colDef)
		if bytes.HasPrefix(upperColDef, []byte("CONSTRAINT")) ||
			bytes.HasPrefix(upperColDef, []byte("PRIMARY KEY")) ||
			bytes.HasPrefix(upperColDef, []byte("FOREIGN KEY")) ||
			bytes.HasPrefix(upperColDef, []byte("UNIQUE KEY")) ||
			bytes.HasPrefix(upperColDef, []byte("KEY")) {
			continue
		}

		// Parse column
		parts := bytes.Fields(colDef)
		if len(parts) < 2 {
			continue
		}

		column := sqlmapper.Column{
			Name:     string(bytes.Trim(parts[0], "`")),
			DataType: string(bytes.ToUpper(parts[1])),
		}

		// Check for additional properties
		upperDef := bytes.ToUpper(colDef)
		column.IsNullable = !bytes.Contains(upperDef, []byte("NOT NULL"))
		column.AutoIncrement = bytes.Contains(upperDef, []byte("AUTOINCREMENT"))

		if bytes.Contains(upperDef, []byte("DEFAULT")) {
			if idx := bytes.Index(upperDef, []byte("DEFAULT")); idx != -1 {
				rest := colDef[idx+7:]
				if spaceIdx := bytes.Index(rest, []byte(" ")); spaceIdx != -1 {
					column.DefaultValue = string(bytes.TrimSpace(rest[:spaceIdx]))
				} else {
					column.DefaultValue = string(bytes.TrimSpace(rest))
				}
			}
		}

		table.Columns = append(table.Columns, column)
	}

	return table, nil
}

// sqliteIndexHeaderRe reads the index name and target table out of a CREATE
// INDEX header, tolerating the optional UNIQUE and IF NOT EXISTS keywords.
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
	if startIdx == -1 || endIdx == -1 {
		return fmt.Errorf("no columns found in CREATE INDEX statement")
	}

	columns := s.splitAndTrim(string(bytes.TrimSpace(stmt[startIdx+1 : endIdx])))

	// Find the table and add the index
	for i, table := range s.schema.Tables {
		if table.Name == tableName {
			s.schema.Tables[i].Indexes = append(s.schema.Tables[i].Indexes, sqlmapper.Index{
				Name:     indexName,
				Columns:  columns,
				IsUnique: isUnique,
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
	if idx := bytes.Index(bytes.ToUpper(stmt), []byte(" AS ")); idx != -1 {
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

	upperStmt := bytes.ToUpper(stmt)

	// Extract timing (BEFORE/AFTER)
	switch {
	case bytes.Contains(upperStmt, []byte("BEFORE")):
		trigger.Timing = "BEFORE"
	case bytes.Contains(upperStmt, []byte("AFTER")):
		trigger.Timing = "AFTER"
	}

	// Extract event (INSERT/UPDATE/DELETE)
	switch {
	case bytes.Contains(upperStmt, []byte("INSERT")):
		trigger.Event = "INSERT"
	case bytes.Contains(upperStmt, []byte("UPDATE")):
		trigger.Event = "UPDATE"
	case bytes.Contains(upperStmt, []byte("DELETE")):
		trigger.Event = "DELETE"
	}

	// Extract table name
	if idx := bytes.Index(upperStmt, []byte(" ON ")); idx != -1 {
		rest := stmt[idx+4:]
		if spaceIdx := bytes.Index(rest, []byte(" ")); spaceIdx != -1 {
			tableName := bytes.TrimSpace(rest[:spaceIdx])
			// Remove schema prefix if exists
			if dotIdx := bytes.LastIndex(tableName, []byte(".")); dotIdx != -1 {
				tableName = tableName[dotIdx+1:]
			}
			trigger.Table = string(bytes.Trim(tableName, "`"))
		}
	}

	// Extract trigger body
	if idx := bytes.Index(upperStmt, []byte(" BEGIN ")); idx != -1 {
		endIdx := bytes.LastIndex(upperStmt, []byte(" END"))
		if endIdx != -1 {
			trigger.Body = string(bytes.TrimSpace(stmt[idx+7 : endIdx]))
		}
	}

	return trigger, nil
}

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

	// Generate tables
	for i, table := range schema.Tables {
		s.buf.WriteString("CREATE TABLE ")
		s.buf.WriteString(table.Name)
		s.buf.WriteString(" (\n")

		// Generate columns
		for j, col := range table.Columns {
			s.buf.WriteString("    ")
			s.buf.WriteString(col.Name)
			s.buf.WriteByte(' ')
			s.buf.WriteString(col.DataType)

			if col.IsPrimaryKey && col.DataType == "INTEGER" {
				s.buf.WriteString(" PRIMARY KEY")
			} else {
				if col.Length > 0 && col.DataType != "TEXT" {
					if col.Scale > 0 {
						fmt.Fprintf(s.buf, "(%d,%d)", col.Length, col.Scale)
					} else {
						fmt.Fprintf(s.buf, "(%d)", col.Length)
					}
				}

				if !col.IsNullable {
					s.buf.WriteString(" NOT NULL")
				}

				if col.IsUnique {
					s.buf.WriteString(" UNIQUE")
				}
			}

			if j < len(table.Columns)-1 {
				s.buf.WriteByte(',')
			}
			s.buf.WriteByte('\n')
		}

		s.buf.WriteString(");\n")

		// Add indexes
		for _, idx := range table.Indexes {
			if idx.IsUnique {
				s.buf.WriteString("CREATE UNIQUE INDEX ")
			} else {
				s.buf.WriteString("CREATE INDEX ")
			}
			s.buf.WriteString(idx.Name)
			s.buf.WriteString(" ON ")
			s.buf.WriteString(table.Name)
			s.buf.WriteByte('(')
			s.buf.WriteString(strings.Join(idx.Columns, ", "))
			s.buf.WriteString(");\n")
		}

		if i < len(schema.Tables)-1 {
			s.buf.WriteByte('\n')
		}
	}

	return s.buf.String(), nil
}

func (s *SQLite) parseTables(statement string) error {
	re := regexp.MustCompile(`CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([.\w]+)\s*\((.*?)\)`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 2 {
		tableName := matches[1]
		columnDefs := matches[2]

		table := sqlmapper.Table{}

		// Parse schema if exists
		parts := strings.Split(tableName, ".")
		if len(parts) > 1 {
			table.Schema = parts[0]
			table.Name = parts[1]
		} else {
			table.Name = tableName
		}

		// Parse columns and constraints
		columns := strings.Split(columnDefs, ",")
		for _, col := range columns {
			col = strings.TrimSpace(col)
			if strings.HasPrefix(strings.ToUpper(col), "CONSTRAINT") {
				continue // Skip constraints for now
			}

			parts := strings.Fields(col)
			if len(parts) < 2 {
				continue
			}

			column := sqlmapper.Column{
				Name:       parts[0],
				DataType:   parts[1],
				IsNullable: true,
			}

			// Parse length/precision
			if strings.Contains(column.DataType, "(") {
				re := regexp.MustCompile(`(\w+)\((\d+)(?:,(\d+))?\)`)
				if matches := re.FindStringSubmatch(column.DataType); len(matches) > 2 {
					column.DataType = matches[1]
					column.Length = atoi(matches[2])
					if len(matches) > 3 && matches[3] != "" {
						column.Scale = atoi(matches[3])
					}
				}
			}

			// Parse constraints
			if strings.Contains(strings.ToUpper(col), "NOT NULL") {
				column.IsNullable = false
			}
			if strings.Contains(strings.ToUpper(col), "PRIMARY KEY") {
				column.IsPrimaryKey = true
				if strings.Contains(strings.ToUpper(col), "AUTOINCREMENT") {
					column.AutoIncrement = true
				}
			}
			if strings.Contains(strings.ToUpper(col), "UNIQUE") {
				column.IsUnique = true
			}
			if strings.Contains(strings.ToUpper(col), "DEFAULT") {
				re := regexp.MustCompile(`DEFAULT\s+([^,\s]+)`)
				if matches := re.FindStringSubmatch(col); len(matches) > 1 {
					column.DefaultValue = matches[1]
				}
			}

			table.Columns = append(table.Columns, column)
		}

		s.schema.Tables = append(s.schema.Tables, table)
	}

	return nil
}

func (s *SQLite) parseViews(statement string) error {
	re := regexp.MustCompile(`CREATE(?:\s+TEMP|\s+TEMPORARY)?\s+VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?([.\w]+)\s+AS\s+(.+)$`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 2 {
		viewName := matches[1]
		view := sqlmapper.View{
			Definition: matches[2],
		}

		// Parse schema if exists
		parts := strings.Split(viewName, ".")
		if len(parts) > 1 {
			view.Schema = parts[0]
			view.Name = parts[1]
		} else {
			view.Name = viewName
		}

		s.schema.Views = append(s.schema.Views, view)
	}

	return nil
}

func (s *SQLite) parseTriggers(statement string) error {
	re := regexp.MustCompile(`CREATE\s+TRIGGER\s+(?:IF\s+NOT\s+EXISTS\s+)?([.\w]+)\s+(BEFORE|AFTER|INSTEAD\s+OF)\s+(DELETE|INSERT|UPDATE(?:\s+OF\s+[^ON]+)?)\s+ON\s+([.\w]+)(?:\s+FOR\s+EACH\s+ROW)?(?:\s+WHEN\s+([^BEGIN]+))?\s+BEGIN\s+(.*?)\s+END`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 6 {
		triggerName := matches[1]
		trigger := sqlmapper.Trigger{
			Timing:     matches[2],
			Event:      matches[3],
			Table:      matches[4],
			Condition:  matches[5],
			Body:       matches[6],
			ForEachRow: strings.Contains(statement, "FOR EACH ROW"),
		}

		// Parse schema if exists
		parts := strings.Split(triggerName, ".")
		if len(parts) > 1 {
			trigger.Schema = parts[0]
			trigger.Name = parts[1]
		} else {
			trigger.Name = triggerName
		}

		s.schema.Triggers = append(s.schema.Triggers, trigger)
	}

	return nil
}

// generateTableSQL generates SQL for a table
func (s *SQLite) generateTableSQL(table sqlmapper.Table) string {
	sql := "CREATE TABLE " + table.Name + " (\n"

	// Generate columns
	for i, col := range table.Columns {
		sql += "    " + col.Name + " " + col.DataType
		if col.Length > 0 {
			sql += fmt.Sprintf("(%d", col.Length)
			if col.Scale > 0 {
				sql += fmt.Sprintf(",%d", col.Scale)
			}
			sql += ")"
		}

		if col.IsPrimaryKey {
			sql += " PRIMARY KEY"
			if col.AutoIncrement {
				sql += " AUTOINCREMENT"
			}
		}
		if !col.IsNullable {
			sql += " NOT NULL"
		}
		if col.IsUnique {
			sql += " UNIQUE"
		}
		if col.DefaultValue != "" {
			sql += " DEFAULT " + col.DefaultValue
		}

		if i < len(table.Columns)-1 {
			sql += ",\n"
		}
	}

	sql += "\n)"

	return sql
}

// generateIndexSQL generates SQL for an index
func (s *SQLite) generateIndexSQL(tableName string, index sqlmapper.Index) string {
	var sql string
	if index.IsUnique {
		sql = "CREATE UNIQUE INDEX "
	} else {
		sql = "CREATE INDEX "
	}

	sql += index.Name + " ON " + tableName + " (" + strings.Join(index.Columns, ", ") + ")"

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
