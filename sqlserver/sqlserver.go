// Package sqlserver provides functionality for parsing and generating SQL Server database schemas.
// It implements the Parser interface for handling SQL Server specific SQL syntax and schema structures.
package sqlserver

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mstgnz/sqlmapper"
)

// Real SQL Server scripts come out of SSMS "Generate Scripts", which brackets
// every identifier, appends a WITH (...) option block and an ON [PRIMARY]
// filegroup to each index and key, writes ASC/DESC in every key list, and
// re-states each constraint with a separate CHECK CONSTRAINT statement. None of
// that survives into the shared schema model, and all of it breaks naive
// splitting, so it is stripped up front.
var (
	mssWithOptionsRe = regexp.MustCompile(`(?is)\s*WITH\s*\(\s*(?:PAD_INDEX|STATISTICS_NORECOMPUTE|SORT_IN_TEMPDB|IGNORE_DUP_KEY|DROP_EXISTING|ONLINE|ALLOW_ROW_LOCKS|ALLOW_PAGE_LOCKS|FILLFACTOR|OPTIMIZE_FOR_SEQUENTIAL_KEY|DATA_COMPRESSION)\b[^)]*\)`)
	// A filegroup always follows the closing parenthesis of a key list or a table
	// body. Anchoring on that ")" is what separates it from the target of
	// "CREATE INDEX ix ON [dbo].[invoices]", which must survive, and from
	// "ON DELETE CASCADE", which an unbracketed pattern would also swallow.
	mssOnFilegroupRe = regexp.MustCompile(`(?i)\)\s*(?:TEXTIMAGE_)?ON\s+\[[^\]]+\]`)
	mssSortOrderRe   = regexp.MustCompile(`(?i)(\]|\w)\s+(?:ASC|DESC)\b`)
	mssWithCheckRe   = regexp.MustCompile(`(?i)\s+WITH\s+(?:NO)?CHECK\b`)
	mssClusteredRe   = regexp.MustCompile(`(?i)\s+(?:NON)?CLUSTERED\b`)
	mssIdentityRe    = regexp.MustCompile(`(?i)\s*IDENTITY\s*(?:\(\s*-?\d+\s*,\s*-?\d+\s*\))?`)
	mssTypeParenRe   = regexp.MustCompile(`\(\s*(\d+|MAX|max)\s*(?:,\s*(\d+)\s*)?\)`)
	mssWhitespaceRe  = regexp.MustCompile(`\s+`)
)

// SSMS states defaults, foreign keys and checks as separate ALTER TABLE
// statements rather than inline, and follows each constraint with a redundant
// CHECK CONSTRAINT statement that only re-enables it.
var (
	mssAlterDefaultRe = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(\S+)\s+ADD\s+CONSTRAINT\s+(\S+)\s+DEFAULT\s+(.+?)\s+FOR\s+(\S+)\s*$`)
	mssAlterFKRe      = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(\S+)\s+ADD\s+CONSTRAINT\s+(\S+)\s+FOREIGN\s+KEY\s*\(([^)]+)\)\s*REFERENCES\s+(\S+)\s*\(([^)]+)\)(.*)$`)
	mssAlterCheckRe   = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(\S+)\s+ADD\s+CONSTRAINT\s+(\S+)\s+CHECK\s*\((.*)\)\s*$`)
	mssAlterKeyRe     = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(\S+)\s+ADD\s+CONSTRAINT\s+(\S+)\s+(PRIMARY\s+KEY|UNIQUE)\s*\(([^)]+)\)`)
	mssCheckOnlyRe    = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+\S+\s+(?:NO)?CHECK\s+CONSTRAINT\b`)
)

// mssTypeStopWords marks the tokens that end a type expression.
var mssTypeStopWords = map[string]bool{
	"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true, "UNIQUE": true,
	"CHECK": true, "REFERENCES": true, "CONSTRAINT": true, "IDENTITY": true,
	"COLLATE": true, "ROWGUIDCOL": true, "SPARSE": true, "FILESTREAM": true,
}

// unbracketIdent strips the [] SSMS wraps around every identifier.
func unbracketIdent(s string) string {
	return strings.Trim(strings.TrimSpace(s), "[]\"")
}

// splitBracketedName reduces [dbo].[customers] to its bare table name.
func splitBracketedName(raw string) string {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	return unbracketIdent(parts[len(parts)-1])
}

// normalizeSQLServerDDL removes the SSMS scripting noise described above.
func normalizeSQLServerDDL(stmt string) string {
	stmt = mssWithOptionsRe.ReplaceAllString(stmt, "")
	stmt = mssOnFilegroupRe.ReplaceAllString(stmt, ")")
	stmt = mssSortOrderRe.ReplaceAllString(stmt, "$1")
	stmt = mssWithCheckRe.ReplaceAllString(stmt, "")
	// SSMS writes "ADD  CONSTRAINT" with two spaces and spreads a key list over
	// several lines. Collapsing whitespace lets every keyword match be written
	// once instead of once per spacing variant.
	return strings.TrimSpace(mssWhitespaceRe.ReplaceAllString(stmt, " "))
}

// normalizeSQLServerTypeName folds SQL Server type names onto the shared
// vocabulary the other dialects use.
func normalizeSQLServerTypeName(name string) string {
	switch strings.ToLower(strings.Join(strings.Fields(name), " ")) {
	case "nvarchar", "varchar":
		return "varchar"
	case "nchar", "char":
		return "char"
	case "ntext", "text":
		return "text"
	case "bit":
		// Not boolean. SQL Server code compares a bit to 1, and view bodies and
		// check expressions are carried over verbatim, so a real boolean target
		// fails with "operator does not exist: boolean = integer".
		return "smallint"
	case "tinyint":
		return "smallint"
	case "datetime", "datetime2", "smalldatetime":
		return "timestamp"
	case "datetimeoffset":
		return "timestamp with time zone"
	case "uniqueidentifier":
		return "uuid"
	case "money", "smallmoney":
		return "decimal"
	case "image", "varbinary", "binary":
		return "blob"
	case "real":
		return "real"
	case "float":
		return "double precision"
	case "xml":
		return "text"
	}
	return strings.ToLower(name)
}

// takeSQLServerType returns the leading tokens of rest that form the type.
func takeSQLServerType(rest string) string {
	var out []string
	depth := 0
	for _, tok := range strings.Fields(rest) {
		if depth == 0 && mssTypeStopWords[strings.ToUpper(strings.Trim(tok, ","))] {
			break
		}
		out = append(out, tok)
		depth += strings.Count(tok, "(") - strings.Count(tok, ")")
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

// SQLServer represents a SQL Server parser implementation that handles parsing and generating
// SQL Server database schemas. It maintains an internal schema representation and provides
// methods for converting between SQL Server SQL and the common schema format.
type SQLServer struct {
	schema *sqlmapper.Schema
	buf    *bytes.Buffer // Buffer for parsing operations
}

// NewSQLServer creates and initializes a new SQL Server parser instance.
// It returns a parser that can handle SQL Server specific SQL syntax and schema structures.
func NewSQLServer() sqlmapper.Database {
	return &SQLServer{
		schema: &sqlmapper.Schema{},
		buf:    bytes.NewBuffer(nil),
	}
}

// Parse takes a SQL Server SQL dump content and parses it into a common schema structure.
// It processes various SQL Server objects including:
// - Tables with columns and constraints
// - Indexes (including UNIQUE indexes)
// - Views
// - Triggers
// - ALTER TABLE statements
//
// Parameters:
//   - content: The SQL Server SQL dump content to parse
//
// Returns:
//   - *sqlmapper.Schema: The parsed schema structure
//   - error: An error if parsing fails or if the content is empty
func (s *SQLServer) Parse(content string) (*sqlmapper.Schema, error) {
	if content == "" {
		return nil, errors.New("empty content")
	}

	// Convert content to bytes
	contentBytes := []byte(content)

	// Split content into statements
	statements := s.splitStatements(contentBytes)

	for _, stmt := range statements {
		stmt = bytes.TrimSpace(stmt)
		if len(stmt) == 0 {
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

		case mssIndexHeaderRe.Match(stmt):
			if err := s.parseCreateIndex(stmt); err != nil {
				return nil, fmt.Errorf("error parsing CREATE INDEX: %v", err)
			}

		case bytes.HasPrefix(upperStmt, []byte("ALTER TABLE")):
			if err := s.parseAlterTable(stmt); err != nil {
				return nil, fmt.Errorf("error parsing ALTER TABLE: %v", err)
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

// splitStatements splits the SQL content into individual statements.
// It handles both semicolon and GO statement terminators.
func (s *SQLServer) splitStatements(content []byte) [][]byte {
	var statements [][]byte
	s.buf.Reset()

	// First split by GO statements
	content = []byte(stripSQLComments(string(content)))

	goBlocks := bytes.Split(content, []byte("GO"))

	for _, block := range goBlocks {
		block = bytes.TrimSpace(block)
		if len(block) == 0 {
			continue
		}

		// Then split each GO block by semicolons
		stmts := bytes.Split(block, []byte(";"))
		for _, stmt := range stmts {
			stmt = bytes.TrimSpace(stmt)
			if len(stmt) == 0 || bytes.HasPrefix(stmt, []byte("--")) {
				continue
			}
			// "ALTER TABLE x CHECK CONSTRAINT y" only re-enables a constraint
			// that was already declared; there is nothing in it to parse.
			if mssCheckOnlyRe.Match(stmt) {
				continue
			}
			statements = append(statements, []byte(normalizeSQLServerDDL(string(stmt))))
		}
	}

	return statements
}

// applySQLServerAlterConstraint handles the ALTER TABLE forms SSMS scripts use
// for defaults, foreign keys, checks and keys. It reports whether the statement
// was recognised, so the caller can fall back for anything else.
func (s *SQLServer) applySQLServerAlterConstraint(tableIndex int, stmt string) bool {
	table := &s.schema.Tables[tableIndex]

	// A default belongs to a column, not to the constraint list.
	if m := mssAlterDefaultRe.FindStringSubmatch(stmt); len(m) > 4 {
		name := unbracketIdent(m[4])
		for i := range table.Columns {
			if strings.EqualFold(table.Columns[i].Name, name) {
				table.Columns[i].DefaultValue = normalizeSQLServerDefault(m[3])
				return true
			}
		}
		return true
	}

	if m := mssAlterFKRe.FindStringSubmatch(stmt); len(m) > 5 {
		c := sqlmapper.Constraint{
			Name:       unbracketIdent(m[2]),
			Type:       "FOREIGN KEY",
			Columns:    unbracketList(m[3]),
			RefTable:   splitBracketedName(m[4]),
			RefColumns: unbracketList(m[5]),
		}
		rules := strings.ToUpper(m[6])
		for _, r := range []string{"CASCADE", "SET NULL", "SET DEFAULT", "NO ACTION"} {
			if strings.Contains(rules, "ON DELETE "+r) {
				c.DeleteRule = r
			}
			if strings.Contains(rules, "ON UPDATE "+r) {
				c.UpdateRule = r
			}
		}
		table.Constraints = append(table.Constraints, c)
		return true
	}

	if m := mssAlterKeyRe.FindStringSubmatch(stmt); len(m) > 4 {
		kind := "UNIQUE"
		if strings.HasPrefix(strings.ToUpper(strings.Join(strings.Fields(m[3]), " ")), "PRIMARY") {
			kind = "PRIMARY KEY"
		}
		table.Constraints = append(table.Constraints, sqlmapper.Constraint{
			Name:    unbracketIdent(m[2]),
			Type:    kind,
			Columns: unbracketList(m[4]),
		})
		return true
	}

	if m := mssAlterCheckRe.FindStringSubmatch(stmt); len(m) > 3 {
		table.Constraints = append(table.Constraints, sqlmapper.Constraint{
			Name:            unbracketIdent(m[2]),
			Type:            "CHECK",
			CheckExpression: cleanSQLServerExpression(m[3]),
		})
		return true
	}

	return false
}

// unbracketList splits a bracketed column list into bare names.
func unbracketList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := unbracketIdent(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// cleanSQLServerExpression removes the brackets and the redundant parentheses
// SSMS wraps around every expression, so a check reads the same in any dialect.
func cleanSQLServerExpression(expr string) string {
	expr = strings.NewReplacer("[", "", "]", "").Replace(strings.TrimSpace(expr))
	for strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
		inner := strings.TrimSpace(expr[1 : len(expr)-1])
		if inner == "" || strings.Count(inner, "(") != strings.Count(inner, ")") {
			break
		}
		expr = inner
	}
	return expr
}

// toSQLServerType maps the shared type vocabulary onto SQL Server's own types.
// SQL Server has no boolean beyond BIT, no native JSON column and no unbounded
// TEXT, so those fold onto the nearest thing it does have.
var toSQLServerType = map[string]string{
	"varchar": "NVARCHAR", "character varying": "NVARCHAR", "char": "NCHAR",
	"text": "NVARCHAR(MAX)", "tinytext": "NVARCHAR(MAX)", "mediumtext": "NVARCHAR(MAX)",
	"longtext": "NVARCHAR(MAX)", "clob": "NVARCHAR(MAX)",
	"smallint": "SMALLINT", "int": "INT", "integer": "INT", "mediumint": "INT",
	"bigint": "BIGINT", "tinyint": "TINYINT",
	"decimal": "DECIMAL", "numeric": "DECIMAL",
	"real": "REAL", "float": "REAL", "double": "FLOAT", "double precision": "FLOAT",
	"boolean": "BIT", "bool": "BIT", "bit": "BIT",
	"date": "DATE", "time": "TIME", "timestamp": "DATETIME2",
	"datetime": "DATETIME2", "timestamptz": "DATETIMEOFFSET",
	"timestamp with time zone": "DATETIMEOFFSET",
	"json":                     "NVARCHAR(MAX)", "jsonb": "NVARCHAR(MAX)", "xml": "XML",
	"blob": "VARBINARY(MAX)", "bytea": "VARBINARY(MAX)", "tinyblob": "VARBINARY(MAX)",
	"mediumblob": "VARBINARY(MAX)", "longblob": "VARBINARY(MAX)",
	"uuid": "UNIQUEIDENTIFIER", "inet": "NVARCHAR(45)", "cidr": "NVARCHAR(45)",
	"macaddr": "NVARCHAR(17)", "interval": "NVARCHAR(255)",
	"enum": "NVARCHAR(255)", "set": "NVARCHAR(255)",
	"serial": "INT", "bigserial": "BIGINT", "smallserial": "SMALLINT",
	"money": "MONEY",
}

// sqlServerNoLengthTypes never take a length, either because SQL Server forbids
// it or because the mapping already carries one.
var sqlServerNoLengthTypes = map[string]bool{
	"DATE": true, "DATETIME2": true, "DATETIMEOFFSET": true, "TIME": true,
	"XML": true, "REAL": true, "FLOAT": true, "BIT": true, "INT": true,
	"BIGINT": true, "SMALLINT": true, "TINYINT": true, "MONEY": true,
	"UNIQUEIDENTIFIER": true,
}

// mssForeignSchemaRe matches the default schema qualifier of the other dialects.
var mssForeignSchemaRe = regexp.MustCompile(`(?i)\bpublic\.`)

// mssPGCastRe matches PostgreSQL's ::type cast suffix, which SQL Server rejects.
var mssPGCastRe = regexp.MustCompile(`::\s*[a-zA-Z_][\w]*(\s+[a-zA-Z_][\w]*)*(\s*\(\s*\d+(\s*,\s*\d+)?\s*\))?(\s*\[\s*\])?`)

// toSQLServerExpression rewrites an expression borrowed from another dialect.
func toSQLServerExpression(expr string) string {
	expr = mssPGCastRe.ReplaceAllString(expr, "")
	expr = mssForeignSchemaRe.ReplaceAllString(expr, "")
	return strings.TrimSpace(expr)
}

// resolveType maps a column onto the SQL Server type it should be declared as.
func (s *SQLServer) resolveType(col sqlmapper.Column) string {
	lower := strings.ToLower(strings.TrimSpace(col.DataType))

	// SQL Server has no array type; a serialised value is the closest it gets.
	if col.IsArray {
		return "NVARCHAR(MAX)"
	}

	mapped, ok := toSQLServerType[lower]
	if !ok {
		mapped = strings.ToUpper(col.DataType)
	}

	if col.Length > 0 && !sqlServerNoLengthTypes[mapped] && !strings.Contains(mapped, "(") {
		if col.Scale > 0 {
			return fmt.Sprintf("%s(%d,%d)", mapped, col.Length, col.Scale)
		}
		return fmt.Sprintf("%s(%d)", mapped, col.Length)
	}
	return mapped
}

// defaultLiteral renders a column default the way SQL Server expects it.
func (s *SQLServer) defaultLiteral(col sqlmapper.Column, mssType string) string {
	dv := strings.TrimSpace(col.DefaultValue)
	if dv == "" || strings.EqualFold(dv, "NULL") {
		return ""
	}

	if mssType == "BIT" {
		switch strings.ToLower(strings.Trim(dv, "'")) {
		case "true", "t", "yes", "1":
			return "1"
		case "false", "f", "no", "0":
			return "0"
		}
	}
	if strings.EqualFold(dv, "CURRENT_TIMESTAMP") {
		return "SYSUTCDATETIME()"
	}
	if isNumericLiteral(dv) {
		return dv
	}
	if strings.ContainsAny(dv, "()") {
		return toSQLServerExpression(dv)
	}
	return "'" + strings.ReplaceAll(dv, "'", "''") + "'"
}

// isNumericLiteral reports whether s looks like a bare number.
func isNumericLiteral(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// generateConstraintSQL renders a table constraint.
func (s *SQLServer) generateConstraintSQL(c sqlmapper.Constraint) string {
	var sb strings.Builder
	if c.Name != "" {
		sb.WriteString("CONSTRAINT " + c.Name + " ")
	}
	switch c.Type {
	case "PRIMARY KEY":
		sb.WriteString(fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(c.Columns, ", ")))
	case "UNIQUE":
		sb.WriteString(fmt.Sprintf("UNIQUE (%s)", strings.Join(c.Columns, ", ")))
	case "FOREIGN KEY":
		sb.WriteString(fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
			strings.Join(c.Columns, ", "), c.RefTable, strings.Join(c.RefColumns, ", ")))
		if c.DeleteRule != "" {
			sb.WriteString(" ON DELETE " + c.DeleteRule)
		}
		if c.UpdateRule != "" {
			sb.WriteString(" ON UPDATE " + c.UpdateRule)
		}
	case "CHECK":
		sb.WriteString(fmt.Sprintf("CHECK (%s)", toSQLServerExpression(c.CheckExpression)))
	}
	return sb.String()
}

// generateTableSQL creates a CREATE TABLE statement with its columns and
// constraints. deferred lists the foreign keys added afterwards.
func (s *SQLServer) generateTableSQL(table sqlmapper.Table, deferred []sqlmapper.Constraint) string {
	var out strings.Builder
	out.WriteString("CREATE TABLE " + table.Name + " (\n")

	inlinePKCols := map[string]bool{}
	var tableConstraints []sqlmapper.Constraint
	for _, c := range table.Constraints {
		switch c.Type {
		case "PRIMARY KEY":
			if len(c.Columns) == 1 && c.Name == "" && columnIsAutoIncrement(table, c.Columns[0]) {
				inlinePKCols[c.Columns[0]] = true
				continue
			}
			tableConstraints = append(tableConstraints, c)
		case "FOREIGN KEY":
			if isDeferred(deferred, c) {
				continue
			}
			tableConstraints = append(tableConstraints, c)
		case "CHECK":
			// MariaDB emulates a JSON column with LONGTEXT plus a json_valid()
			// guard. SQL Server has no such function, so the check would only
			// break the load.
			if sqlmapper.IsJSONEmulationCheck(c.CheckExpression) {
				continue
			}
			tableConstraints = append(tableConstraints, c)
		case "UNIQUE":
			tableConstraints = append(tableConstraints, c)
		}
	}

	hasPKConstraint := false
	for _, c := range tableConstraints {
		if c.Type == "PRIMARY KEY" {
			hasPKConstraint = true
		}
	}
	if !hasPKConstraint && len(inlinePKCols) == 0 {
		for _, col := range table.Columns {
			if col.IsPrimaryKey {
				inlinePKCols[col.Name] = true
			}
		}
	}

	parts := make([]string, 0, len(table.Columns)+len(tableConstraints))
	for _, col := range table.Columns {
		mssType := s.resolveType(col)
		def := "    " + col.Name + " " + mssType
		if col.AutoIncrement {
			def += " IDENTITY(1,1)"
		}
		if inlinePKCols[col.Name] {
			def += " PRIMARY KEY"
		} else if !col.IsNullable {
			def += " NOT NULL"
		}
		if dv := s.defaultLiteral(col, mssType); dv != "" {
			def += " DEFAULT " + dv
		}
		if col.IsUnique && !inlinePKCols[col.Name] && !hasNamedUnique(tableConstraints, col.Name) {
			def += " UNIQUE"
		}
		parts = append(parts, def)
	}
	for _, c := range tableConstraints {
		if sql := s.generateConstraintSQL(c); sql != "" {
			parts = append(parts, "    "+sql)
		}
	}

	out.WriteString(strings.Join(parts, ",\n"))
	out.WriteString("\n);\n")
	return out.String()
}

// hasNamedUnique reports whether a table-level UNIQUE constraint already covers
// the column on its own, so the column does not also carry an inline marker.
func hasNamedUnique(constraints []sqlmapper.Constraint, column string) bool {
	for _, c := range constraints {
		if c.Type == "UNIQUE" && len(c.Columns) == 1 && c.Columns[0] == column {
			return true
		}
	}
	return false
}

// columnIsAutoIncrement reports whether the named column is auto-increment.
func columnIsAutoIncrement(table sqlmapper.Table, name string) bool {
	for _, col := range table.Columns {
		if col.Name == name {
			return col.AutoIncrement
		}
	}
	return false
}

// isDeferred reports whether a constraint is in the deferred set.
func isDeferred(deferred []sqlmapper.Constraint, c sqlmapper.Constraint) bool {
	for _, d := range deferred {
		if d.Name != "" && d.Name == c.Name {
			return true
		}
		if d.Name == "" && c.Name == "" && d.RefTable == c.RefTable &&
			strings.Join(d.Columns, ",") == strings.Join(c.Columns, ",") {
			return true
		}
	}
	return false
}

// parseCreateTable parses a CREATE TABLE statement and returns a Table structure.
func (s *SQLServer) parseCreateTable(stmt []byte) (sqlmapper.Table, error) {
	table := sqlmapper.Table{}

	// Strip the SSMS option blocks and filegroup clauses before anything else:
	// they contain their own parentheses and commas and would otherwise be read
	// as column definitions.
	stmt = []byte(normalizeSQLServerDDL(string(stmt)))

	// Extract table name using bytes.Index and bytes.LastIndex
	startIdx := bytes.Index(bytes.ToUpper(stmt), []byte("TABLE")) + 5
	endIdx := bytes.Index(stmt[startIdx:], []byte("("))
	if endIdx == -1 {
		return table, fmt.Errorf("invalid CREATE TABLE statement")
	}
	table.Name = splitBracketedName(string(bytes.TrimSpace(stmt[startIdx : startIdx+endIdx])))

	// Extract column definitions. Splitting on every comma would cut a
	// definition in half at IDENTITY(1,1) or decimal(10, 2).
	columnsBytes := stmt[startIdx+endIdx+1 : bytes.LastIndex(stmt, []byte(")"))]
	columnDefs := splitTopLevelCommasBytes(columnsBytes)

	for _, colDef := range columnDefs {
		colDef = bytes.TrimSpace(colDef)
		if len(colDef) == 0 {
			continue
		}

		// Handle table constraints
		upperColDef := bytes.ToUpper(colDef)
		if bytes.HasPrefix(upperColDef, []byte("CONSTRAINT")) ||
			bytes.HasPrefix(upperColDef, []byte("PRIMARY KEY")) ||
			bytes.HasPrefix(upperColDef, []byte("FOREIGN KEY")) ||
			bytes.HasPrefix(upperColDef, []byte("UNIQUE")) {
			constraint := s.parseConstraint(colDef)
			table.Constraints = append(table.Constraints, constraint)
			continue
		}

		// Parse column
		column := s.parseColumn(colDef)
		table.Columns = append(table.Columns, column)
	}

	return table, nil
}

// parseColumn parses a column definition and returns a Column structure.
func (s *SQLServer) parseColumn(def []byte) sqlmapper.Column {
	raw := string(def)
	parts := strings.Fields(raw)
	if len(parts) < 2 {
		return sqlmapper.Column{}
	}

	column := sqlmapper.Column{
		Name:       unbracketIdent(parts[0]),
		IsNullable: true, // SQL Server columns are nullable by default
	}

	// IDENTITY(1,1) carries a comma of its own and must come out before the
	// type is read, or the column definition splits in the middle.
	if mssIdentityRe.MatchString(raw) {
		column.AutoIncrement = true
		raw = mssIdentityRe.ReplaceAllString(raw, "")
	}

	rest := strings.TrimSpace(raw[len(parts[0]):])
	typeExpr := takeSQLServerType(rest)

	// Split an embedded length or precision off the type.
	if m := mssTypeParenRe.FindStringSubmatch(typeExpr); len(m) > 1 {
		base := unbracketIdent(mssTypeParenRe.ReplaceAllString(typeExpr, ""))
		column.DataType = normalizeSQLServerTypeName(base)
		if !strings.EqualFold(m[1], "MAX") {
			fmt.Sscanf(m[1], "%d", &column.Length)
			if len(m) > 2 && m[2] != "" {
				fmt.Sscanf(m[2], "%d", &column.Scale)
			}
			switch column.DataType {
			case "timestamp", "timestamp with time zone", "time":
				// A fractional-seconds precision, not a length. Carried through
				// it produced DATETIME(7), which MySQL rejects: its maximum is 6.
				column.Length = 0
			}
		} else if column.DataType == "varchar" {
			// NVARCHAR(MAX) has no length limit; text is the portable answer.
			column.DataType = "text"
		}
	} else {
		column.DataType = normalizeSQLServerTypeName(unbracketIdent(typeExpr))
	}

	upper := strings.ToUpper(raw)
	if strings.Contains(upper, "NOT NULL") {
		column.IsNullable = false
	}
	if strings.Contains(upper, "PRIMARY KEY") {
		column.IsPrimaryKey = true
		column.IsNullable = false
	}
	if strings.Contains(upper, "UNIQUE") {
		column.IsUnique = true
	}

	if idx := strings.Index(upper, "DEFAULT"); idx >= 0 {
		column.DefaultValue = normalizeSQLServerDefault(strings.TrimSpace(raw[idx+len("DEFAULT"):]))
	}

	return column
}

// normalizeSQLServerDefault unwraps the parentheses SSMS puts around every
// default and folds the timestamp functions onto CURRENT_TIMESTAMP.
func normalizeSQLServerDefault(raw string) string {
	v := strings.TrimSpace(takeSQLServerType(raw))
	for strings.HasPrefix(v, "(") && strings.HasSuffix(v, ")") {
		inner := strings.TrimSpace(v[1 : len(v)-1])
		if inner == "" || strings.Count(inner, "(") != strings.Count(inner, ")") {
			break
		}
		v = inner
	}
	switch strings.ToUpper(strings.TrimSuffix(v, "()")) {
	case "GETDATE", "GETUTCDATE", "SYSDATETIME", "SYSUTCDATETIME", "CURRENT_TIMESTAMP":
		return "CURRENT_TIMESTAMP"
	case "NEWID", "NEWSEQUENTIALID":
		return ""
	}
	return strings.Trim(v, "'")
}

// splitTopLevelCommasBytes splits a table body on the commas that separate
// definitions, ignoring the ones nested inside parentheses or string literals.
func splitTopLevelCommasBytes(body []byte) [][]byte {
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

// parseConstraint parses a table constraint definition and returns a Constraint structure.
func (s *SQLServer) parseConstraint(def []byte) sqlmapper.Constraint {
	constraint := sqlmapper.Constraint{}
	def = bytes.TrimSpace(def)

	// Extract constraint name if exists
	if bytes.HasPrefix(bytes.ToUpper(def), []byte("CONSTRAINT")) {
		parts := bytes.Fields(def)
		if len(parts) > 1 {
			constraint.Name = string(bytes.Trim(parts[1], "[]"))
			def = bytes.Join(parts[2:], []byte(" "))
		}
	}

	upperDef := bytes.ToUpper(def)
	switch {
	case bytes.Contains(upperDef, []byte("PRIMARY KEY")):
		constraint.Type = "PRIMARY KEY"
		constraint.Columns = s.extractColumns(def, "PRIMARY KEY")

	case bytes.Contains(upperDef, []byte("FOREIGN KEY")):
		constraint.Type = "FOREIGN KEY"
		constraint.Columns = s.extractColumns(def, "FOREIGN KEY")

		// Extract referenced table and columns
		if idx := bytes.Index(upperDef, []byte("REFERENCES")); idx != -1 {
			refPart := def[idx:]
			startIdx := bytes.Index(refPart, []byte("("))
			endIdx := bytes.Index(refPart, []byte(")"))
			if startIdx != -1 && endIdx != -1 {
				tableName := bytes.TrimSpace(refPart[9:startIdx])
				// Remove schema prefix and brackets
				if idx := bytes.LastIndex(tableName, []byte(".")); idx != -1 {
					tableName = tableName[idx+1:]
				}
				tableName = bytes.Trim(tableName, "[]")
				constraint.RefTable = string(tableName)

				colStr := string(refPart[startIdx+1 : endIdx])
				constraint.RefColumns = s.splitAndTrim(colStr)
			}
		}

		// Extract ON DELETE rule
		if bytes.Contains(upperDef, []byte("ON DELETE CASCADE")) {
			constraint.DeleteRule = "CASCADE"
		}

	case bytes.Contains(upperDef, []byte("UNIQUE")):
		constraint.Type = "UNIQUE"
		constraint.Columns = s.extractColumns(def, "UNIQUE")

	case bytes.Contains(upperDef, []byte("CHECK")):
		constraint.Type = "CHECK"
		if idx := bytes.Index(upperDef, []byte("CHECK")); idx != -1 {
			startIdx := bytes.Index(def[idx:], []byte("("))
			endIdx := bytes.LastIndex(def, []byte(")"))
			if startIdx != -1 && endIdx != -1 {
				constraint.CheckExpression = string(bytes.TrimSpace(def[idx+startIdx+1 : endIdx]))
			}
		}
	}

	return constraint
}

// extractColumns extracts column names from a constraint definition.
func (s *SQLServer) extractColumns(def []byte, afterKeyword string) []string {
	upperDef := bytes.ToUpper(def)
	keyword := []byte(afterKeyword)

	if idx := bytes.Index(upperDef, keyword); idx != -1 {
		rest := def[idx+len(keyword):]
		startIdx := bytes.Index(rest, []byte("("))
		endIdx := bytes.Index(rest, []byte(")"))
		if startIdx != -1 && endIdx != -1 {
			colStr := string(rest[startIdx+1 : endIdx])
			return s.splitAndTrim(colStr)
		}
	}
	return nil
}

// splitAndTrim splits a string by commas and trims whitespace and brackets from each part.
func (s *SQLServer) splitAndTrim(str string) []string {
	parts := strings.Split(str, ",")
	result := make([]string, len(parts))
	for i, part := range parts {
		result[i] = strings.Trim(strings.TrimSpace(part), "[]")
	}
	return result
}

// Generate creates a SQL Server SQL dump from a schema structure.
// It generates SQL statements for all database objects in the schema, including:
// - Tables with columns and constraints
// - Indexes (including UNIQUE indexes)
// - Views
// - Triggers
//
// Parameters:
//   - schema: The schema structure to convert to SQL Server SQL
//
// Returns:
//   - string: The generated SQL Server SQL statements
//   - error: An error if generation fails or if the schema is nil
func (s *SQLServer) Generate(schema *sqlmapper.Schema) (string, error) {
	if schema == nil {
		return "", errors.New("empty schema")
	}

	s.buf.Reset()

	// Dump tools do not order tables by dependency, so a child table can precede
	// its parent and the foreign key would fail to resolve.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)

	// SQL Server batches statements with GO, and some statements are required to
	// start one: without it CREATE VIEW fails with "CREATE VIEW must be the
	// first statement in a query batch".
	for _, table := range tables {
		s.buf.WriteString(s.generateTableSQL(table, deferredFKs[table.Name]))
		s.buf.WriteString("GO\n")

		// Add indexes
		for _, idx := range table.Indexes {
			s.buf.WriteString(s.generateIndexSQL(table.Name, idx))
			s.buf.WriteString(";\nGO\n")
		}
	}

	// Foreign keys that close a cycle are added once every table exists.
	for _, table := range tables {
		for _, c := range deferredFKs[table.Name] {
			fmt.Fprintf(s.buf, "ALTER TABLE %s ADD %s;\nGO\n", table.Name, s.generateConstraintSQL(c))
		}
	}

	// Views are emitted last so the tables they select from already exist.
	for _, view := range schema.Views {
		body := toSQLServerExpression(strings.TrimSuffix(strings.TrimSpace(view.Definition), ";"))
		fmt.Fprintf(s.buf, "CREATE VIEW %s AS %s;\nGO\n", view.Name, body)
	}

	return s.buf.String(), nil
}

// mssIndexHeaderRe reads a CREATE INDEX header. UNIQUE and CLUSTERED /
// NONCLUSTERED are both optional, and each shifts every fixed token position, so
// the header is matched rather than counted.
var mssIndexHeaderRe = regexp.MustCompile(`(?i)^\s*CREATE\s+(UNIQUE\s+)?(CLUSTERED\s+|NONCLUSTERED\s+)?INDEX\s+([\[\]."\w]+)\s+ON\s+([\[\]."\w]+)\s*\(([^)]*)\)`)

// parseCreateIndex parses a CREATE INDEX statement and adds the index to the appropriate table.
func (s *SQLServer) parseCreateIndex(stmt []byte) error {
	m := mssIndexHeaderRe.FindSubmatch(stmt)
	if m == nil {
		return fmt.Errorf("invalid CREATE INDEX statement")
	}

	index := sqlmapper.Index{
		Name:        unbracketIdent(string(m[3])),
		Columns:     unbracketList(string(m[5])),
		IsUnique:    len(bytes.TrimSpace(m[1])) > 0,
		IsClustered: strings.EqualFold(strings.TrimSpace(string(m[2])), "CLUSTERED"),
	}
	tableName := splitBracketedName(string(m[4]))

	for i, table := range s.schema.Tables {
		if table.Name == tableName {
			s.schema.Tables[i].Indexes = append(s.schema.Tables[i].Indexes, index)
			return nil
		}
	}

	return fmt.Errorf("table not found for index: %s", tableName)
}

// parseAlterTable parses an ALTER TABLE statement and modifies the appropriate table.
func (s *SQLServer) parseAlterTable(stmt []byte) error {
	// Extract table name
	parts := bytes.Fields(stmt)
	if len(parts) < 3 {
		return fmt.Errorf("invalid ALTER TABLE statement")
	}

	tableName := string(bytes.Trim(parts[2], "[]"))
	// Remove schema prefix if exists
	if idx := bytes.LastIndex(parts[2], []byte(".")); idx != -1 {
		tableName = string(bytes.Trim(parts[2][idx+1:], "[]"))
	}

	// Find the table
	var tableIndex = -1
	for i, table := range s.schema.Tables {
		if table.Name == tableName {
			tableIndex = i
			break
		}
	}

	if tableIndex == -1 {
		// Create new table if it doesn't exist
		s.schema.Tables = append(s.schema.Tables, sqlmapper.Table{Name: tableName})
		tableIndex = len(s.schema.Tables) - 1
	}

	// Handle different ALTER TABLE operations
	upperStmt := bytes.ToUpper(stmt)
	switch {
	case bytes.Contains(upperStmt, []byte("ADD CONSTRAINT")):
		if s.applySQLServerAlterConstraint(tableIndex, string(stmt)) {
			break
		}
		if idx := bytes.Index(upperStmt, []byte("ADD CONSTRAINT")); idx != -1 {
			constraint := s.parseConstraint(stmt[idx:])
			s.schema.Tables[tableIndex].Constraints = append(s.schema.Tables[tableIndex].Constraints, constraint)
		}

	case bytes.Contains(upperStmt, []byte("ADD COLUMN")) || bytes.Contains(upperStmt, []byte("ADD ")):
		// Extract column definition
		addIndex := bytes.Index(upperStmt, []byte("ADD "))
		if addIndex == -1 {
			return fmt.Errorf("invalid ALTER TABLE ADD statement")
		}

		colDef := bytes.TrimSpace(stmt[addIndex+4:])
		if bytes.HasPrefix(bytes.ToUpper(colDef), []byte("COLUMN")) {
			colDef = bytes.TrimSpace(colDef[6:])
		}

		column := s.parseColumn(colDef)
		s.schema.Tables[tableIndex].Columns = append(s.schema.Tables[tableIndex].Columns, column)
	}

	return nil
}

// parseCreateView parses a CREATE VIEW statement and returns a View structure.
func (s *SQLServer) parseCreateView(stmt []byte) (sqlmapper.View, error) {
	view := sqlmapper.View{}

	// Extract view name
	parts := bytes.Fields(stmt)
	if len(parts) < 3 {
		return view, fmt.Errorf("invalid CREATE VIEW statement")
	}

	viewName := string(bytes.Trim(parts[2], "[]"))
	// Remove schema prefix if exists
	if idx := bytes.LastIndex(parts[2], []byte(".")); idx != -1 {
		viewName = string(bytes.Trim(parts[2][idx+1:], "[]"))
	}
	view.Name = viewName

	// Extract view definition
	if idx := bytes.Index(bytes.ToUpper(stmt), []byte(" AS ")); idx != -1 {
		view.Definition = string(bytes.TrimSpace(stmt[idx+4:]))
	}

	return view, nil
}

// parseCreateTrigger parses a CREATE TRIGGER statement and returns a Trigger structure.
func (s *SQLServer) parseCreateTrigger(stmt []byte) (sqlmapper.Trigger, error) {
	trigger := sqlmapper.Trigger{}

	// Extract trigger name
	parts := bytes.Fields(stmt)
	if len(parts) < 3 {
		return trigger, fmt.Errorf("invalid CREATE TRIGGER statement")
	}

	triggerName := string(bytes.Trim(parts[2], "[]"))
	trigger.Name = triggerName

	upperStmt := bytes.ToUpper(stmt)

	// Extract timing (AFTER/FOR/INSTEAD OF)
	switch {
	case bytes.Contains(upperStmt, []byte("AFTER")):
		trigger.Timing = "AFTER"
	case bytes.Contains(upperStmt, []byte("INSTEAD OF")):
		trigger.Timing = "INSTEAD OF"
	default:
		trigger.Timing = "FOR"
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
			trigger.Table = string(bytes.Trim(tableName, "[]"))
		}
	}

	// Extract trigger body
	if idx := bytes.Index(upperStmt, []byte(" AS ")); idx != -1 {
		trigger.Body = string(bytes.TrimSpace(stmt[idx+4:]))
	}

	return trigger, nil
}

func (s *SQLServer) parseTables(statement string) error {
	re := regexp.MustCompile(`CREATE\s+TABLE\s+([.\w\[\]]+)\s*\((.*?)\)(?:\s+ON\s+(\w+))?`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 2 {
		tableName := matches[1]
		columnDefs := matches[2]

		table := sqlmapper.Table{}

		// Parse schema if exists
		parts := strings.Split(strings.Trim(tableName, "[]"), ".")
		if len(parts) > 1 {
			table.Schema = parts[0]
			table.Name = parts[1]
		} else {
			table.Name = tableName
		}

		// Parse filegroup if exists
		if len(matches) > 3 && matches[3] != "" {
			table.TableSpace = matches[3]
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
				Name:       strings.Trim(parts[0], "[]"),
				DataType:   parts[1],
				IsNullable: true,
			}

			// Parse length/precision
			if strings.Contains(column.DataType, "(") {
				re := regexp.MustCompile(`(\w+)\((\d+|MAX)(?:,(\d+))?\)`)
				if matches := re.FindStringSubmatch(column.DataType); len(matches) > 2 {
					column.DataType = matches[1]
					if matches[2] == "MAX" {
						column.Length = -1
					} else {
						fmt.Sscanf(matches[2], "%d", &column.Length)
					}
					if len(matches) > 3 && matches[3] != "" {
						fmt.Sscanf(matches[3], "%d", &column.Scale)
					}
				}
			}

			// Parse constraints
			if strings.Contains(strings.ToUpper(col), "NOT NULL") {
				column.IsNullable = false
			}
			if strings.Contains(strings.ToUpper(col), "PRIMARY KEY") {
				column.IsPrimaryKey = true
			}
			if strings.Contains(strings.ToUpper(col), "IDENTITY") {
				column.AutoIncrement = true
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

func (s *SQLServer) parseViews(statement string) error {
	re := regexp.MustCompile(`CREATE\s+VIEW\s+([.\w\[\]]+)\s+AS\s+(.+)$`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 2 {
		viewName := matches[1]
		view := sqlmapper.View{
			Definition: matches[2],
		}

		// Parse schema if exists
		parts := strings.Split(strings.Trim(viewName, "[]"), ".")
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

func (s *SQLServer) parseFunctions(statement string) error {
	re := regexp.MustCompile(`CREATE\s+(FUNCTION|PROCEDURE)\s+([.\w\[\]]+)\s*\((.*?)\)(?:\s+RETURNS\s+(\w+(?:\s*\([^)]*\))?))?\s+AS\s+BEGIN\s+(.*?)\s+END`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 4 {
		isProc := matches[1] == "PROCEDURE"
		functionName := matches[2]
		function := sqlmapper.Function{
			IsProc: isProc,
			Body:   matches[5],
		}

		if !isProc && matches[4] != "" {
			function.Returns = matches[4]
		}

		// Parse schema if exists
		parts := strings.Split(strings.Trim(functionName, "[]"), ".")
		if len(parts) > 1 {
			function.Schema = parts[0]
			function.Name = parts[1]
		} else {
			function.Name = functionName
		}

		// Parse parameters
		if matches[3] != "" {
			params := strings.Split(matches[3], ",")
			for _, param := range params {
				parts := strings.Fields(strings.TrimSpace(param))
				if len(parts) >= 2 {
					parameter := sqlmapper.Parameter{
						Name:     strings.TrimPrefix(parts[0], "@"),
						DataType: parts[1],
					}
					function.Parameters = append(function.Parameters, parameter)
				}
			}
		}

		s.schema.Functions = append(s.schema.Functions, function)
	}

	return nil
}

func (s *SQLServer) parseTriggers(statement string) error {
	re := regexp.MustCompile(`CREATE\s+TRIGGER\s+([.\w\[\]]+)\s+ON\s+([.\w\[\]]+)\s+(AFTER|INSTEAD\s+OF|FOR)\s+(INSERT|UPDATE|DELETE)(?:\s*,\s*(INSERT|UPDATE|DELETE))*\s+AS\s+BEGIN\s+(.*?)\s+END`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 6 {
		triggerName := matches[1]
		trigger := sqlmapper.Trigger{
			Table:  matches[2],
			Timing: matches[3],
			Event:  matches[4],
			Body:   matches[6],
		}

		// Parse schema if exists
		parts := strings.Split(strings.Trim(triggerName, "[]"), ".")
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

// generateIndexSQL generates SQL for an index
func (s *SQLServer) generateIndexSQL(tableName string, index sqlmapper.Index) string {
	// The keyword order is fixed: CREATE [UNIQUE] [CLUSTERED|NONCLUSTERED] INDEX.
	// Emitting NONCLUSTERED before UNIQUE is a syntax error.
	sql := "CREATE "
	if index.IsUnique {
		sql += "UNIQUE "
	}
	if index.IsClustered {
		sql += "CLUSTERED "
	} else {
		sql += "NONCLUSTERED "
	}
	sql += "INDEX "

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
