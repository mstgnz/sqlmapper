// Package sqlserver provides functionality for parsing and generating SQL Server database schemas.
// It implements the Parser interface for handling SQL Server specific SQL syntax and schema structures.
package sqlserver

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/mstgnz/sqlmapper/internal/keyword"
	"github.com/mstgnz/sqlmapper/internal/routine"
	"github.com/mstgnz/sqlmapper/internal/sqlsplit"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/internal/expr"
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
		schema: &sqlmapper.Schema{SourceDialect: sqlmapper.SQLServer},
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
	// Start from an empty schema. A parser used a second time used to add
	// to what it read the first, so a caller reusing one silently got two
	// schemas merged into one.
	s.schema = &sqlmapper.Schema{SourceDialect: sqlmapper.SQLServer}

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
		case keyword.HasPrefixBytes(upperStmt, "CREATE TABLE"):
			table, err := s.parseCreateTable(stmt)
			if err != nil {
				return nil, fmt.Errorf("error parsing CREATE TABLE: %v", err)
			}
			s.schema.Tables = append(s.schema.Tables, table)

		case mssIndexHeaderRe.Match(stmt):
			if err := s.parseCreateIndex(stmt); err != nil {
				return nil, fmt.Errorf("error parsing CREATE INDEX: %v", err)
			}

		case bytes.HasPrefix(upperStmt, []byte("GRANT ")), bytes.HasPrefix(upperStmt, []byte("REVOKE ")):
			s.parsePermission(stmt)

		case keyword.HasPrefixBytes(upperStmt, "CREATE SEQUENCE"):
			seq, err := s.parseCreateSequence(stmt)
			if err != nil {
				return nil, fmt.Errorf("error parsing CREATE SEQUENCE: %v", err)
			}
			s.schema.Sequences = append(s.schema.Sequences, seq)

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

		case mssExtendedPropertyRe.Match(stmt):
			s.applyExtendedProperty(string(stmt))

		// Functions and procedures were read by the stream parser only, so a
		// script converted through this path lost them without a word.
		case bytes.HasPrefix(upperStmt, []byte("CREATE FUNCTION")),
			bytes.HasPrefix(upperStmt, []byte("CREATE PROCEDURE")),
			bytes.HasPrefix(upperStmt, []byte("CREATE PROC ")):
			if err := s.parseFunctions(string(stmt)); err != nil {
				return nil, fmt.Errorf("error parsing routine: %v", err)
			}
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
	// Splitting on GO and then again on every semicolon truncated a procedure
	// body at its first inner statement. The splitter treats GO as the only
	// terminator here, which is what SQL Server itself does, and it drops
	// comments so a statement is not hidden behind one.
	splitter := sqlsplit.New(bytes.NewReader(content), "GO")

	for {
		raw, err := splitter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}

		stmt := bytes.TrimSpace([]byte(raw))
		if len(stmt) == 0 {
			continue
		}
		// "ALTER TABLE x CHECK CONSTRAINT y" only re-enables a constraint that
		// was already declared; there is nothing in it to parse.
		if mssCheckOnlyRe.Match(stmt) {
			continue
		}
		statements = append(statements, []byte(normalizeSQLServerDDL(string(stmt))))
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
			CheckExpression: expr.Normalize(m[3]),
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

// resolveType maps a column onto the SQL Server type it should be declared as.
// sqlServerKeyTextType is what an unbounded text column becomes when it has to
// be indexed. SQL Server refuses NVARCHAR(MAX) as a key column, and 450 is the
// longest nvarchar that fits the 900-byte index key limit.
const sqlServerKeyTextType = "NVARCHAR(450)"

// sqlServerUnboundedText are the types SQL Server will not index.
var sqlServerUnboundedText = map[string]bool{
	"NVARCHAR(MAX)": true, "VARCHAR(MAX)": true, "VARBINARY(MAX)": true,
	"TEXT": true, "NTEXT": true, "IMAGE": true, "XML": true,
}

func (s *SQLServer) resolveType(col sqlmapper.Column) string {
	return s.resolveTypeForKey(col, false)
}

// resolveTypeForKey renders a column's type, bounding an unbounded text column
// when the table indexes it.
func (s *SQLServer) resolveTypeForKey(col sqlmapper.Column, isKey bool) string {
	rendered := s.resolveTypeName(col)
	if isKey && col.Length == 0 && sqlServerUnboundedText[strings.ToUpper(rendered)] {
		return sqlServerKeyTextType
	}
	return rendered
}

func (s *SQLServer) resolveTypeName(col sqlmapper.Column) string {
	lower := strings.ToLower(strings.TrimSpace(col.DataType))

	// SQL Server has no array type; a serialised value is the closest it gets.
	if col.IsArray {
		return "NVARCHAR(MAX)"
	}

	// A column typed by a user-defined enum carries a name SQL Server does not
	// know, and it has no enum of its own.
	if len(col.EnumValues) > 0 {
		return "NVARCHAR(255)"
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

	return expr.DefaultLiteral(dv, expr.SQLServer, expr.DefaultOptions{
		NumericColumn: mssIsNumericType(mssType),
	})
}

// mssIsNumericType reports whether a rendered type holds a number, which
// decides what a boolean default becomes. SQL Server has no boolean, so BIT is
// one of them.
func mssIsNumericType(rendered string) bool {
	name := rendered
	if i := strings.IndexByte(name, '('); i != -1 {
		name = name[:i]
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BIT", "TINYINT", "SMALLINT", "INT", "BIGINT",
		"DECIMAL", "NUMERIC", "FLOAT", "REAL", "MONEY", "SMALLMONEY":
		return true
	}
	return false
}

// generateConstraintSQL renders a table constraint.
func (s *SQLServer) generateConstraintSQL(c sqlmapper.Constraint, table string) string {
	var sb strings.Builder
	if c.Name != "" {
		sb.WriteString("CONSTRAINT ")
		sb.WriteString(c.Name)
		sb.WriteString(" ")
	}
	switch c.Type {
	case "PRIMARY KEY":
		fmt.Fprintf(&sb, "PRIMARY KEY (%s)", strings.Join(c.Columns, ", "))
	case "UNIQUE":
		fmt.Fprintf(&sb, "UNIQUE (%s)", strings.Join(c.Columns, ", "))
	case "FOREIGN KEY":
		fmt.Fprintf(&sb, "FOREIGN KEY (%s) REFERENCES %s (%s)",
			strings.Join(c.Columns, ", "), c.RefTable, strings.Join(c.RefColumns, ", "))

		// SQL Server refuses a cascading action on a key that points at its own
		// table: "may cause cycles or multiple cascade paths". The key itself is
		// kept and the action dropped, because a constraint the server will not
		// create is worse than one that enforces a little less.
		selfReference := table != "" && strings.EqualFold(table, c.RefTable)

		if rule := mssReferentialAction(c.DeleteRule); rule != "" && !selfReference {
			sb.WriteString(" ON DELETE " + rule)
		}
		if rule := mssReferentialAction(c.UpdateRule); rule != "" && !selfReference {
			sb.WriteString(" ON UPDATE " + rule)
		}
	case "CHECK":
		fmt.Fprintf(&sb, "CHECK (%s)", expr.Condition(c.CheckExpression, expr.SQLServer))
	}
	return sb.String()
}

// generateTableSQL creates a CREATE TABLE statement with its columns and
// constraints. deferred lists the foreign keys added afterwards.
func (s *SQLServer) generateTableSQL(table sqlmapper.Table, deferred []sqlmapper.Constraint) string {
	var out strings.Builder
	out.WriteString("CREATE TABLE ")
	out.WriteString(table.Name)
	out.WriteString(" (\n")

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
			if expr.IsJSONGuardSQL(c.CheckExpression) {
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

	// A column the table indexes cannot be NVARCHAR(MAX), so the type
	// resolution needs to know which ones those are.
	keyCols := sqlmapper.KeyColumns(table)

	parts := make([]string, 0, len(table.Columns)+len(tableConstraints))
	for _, col := range table.Columns {
		mssType := s.resolveTypeForKey(col, keyCols[col.Name])
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
		if sql := s.generateConstraintSQL(c, table.Name); sql != "" {
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
	startIdx := bytes.Index(keyword.UpperASCIIBytes(stmt), []byte("TABLE")) + 5
	endIdx := bytes.Index(stmt[startIdx:], []byte("("))
	if endIdx == -1 {
		return table, fmt.Errorf("invalid CREATE TABLE statement")
	}
	table.Name = splitBracketedName(string(bytes.TrimSpace(stmt[startIdx : startIdx+endIdx])))

	// Extract column definitions. Splitting on every comma would cut a
	// definition in half at IDENTITY(1,1) or decimal(10, 2).
	bodyEnd := bytes.LastIndex(stmt, []byte(")"))
	if bodyEnd <= startIdx+endIdx {
		return table, fmt.Errorf("invalid CREATE TABLE statement")
	}
	columnsBytes := stmt[startIdx+endIdx+1 : bodyEnd]
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

	// IDENTITY(1,1) carries a comma of its own and must come out before the
	// definition is read, or it splits in the middle. It is stripped first so
	// that the fields below are the fields of what is left: taking them from
	// the original text and then slicing the shortened one ran off the end.
	autoIncrement := mssIdentityRe.MatchString(raw)
	if autoIncrement {
		raw = mssIdentityRe.ReplaceAllString(raw, "")
	}

	parts := strings.Fields(raw)
	if len(parts) < 2 {
		return sqlmapper.Column{}
	}

	column := sqlmapper.Column{
		Name:          unbracketIdent(parts[0]),
		IsNullable:    true, // SQL Server columns are nullable by default
		AutoIncrement: autoIncrement,
	}

	rest := strings.TrimSpace(raw[len(parts[0]):])
	typeExpr := takeSQLServerType(rest)

	// Split an embedded length or precision off the type.
	if m := mssTypeParenRe.FindStringSubmatch(typeExpr); len(m) > 1 {
		base := unbracketIdent(mssTypeParenRe.ReplaceAllString(typeExpr, ""))
		column.DataType = normalizeSQLServerTypeName(base)
		if !strings.EqualFold(m[1], "MAX") {
			column.Length = atoi(m[1])
			if len(m) > 2 && m[2] != "" {
				column.Scale = atoi(m[2])
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

	upper := keyword.UpperASCII(raw)
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

	upperDef := keyword.UpperASCIIBytes(def)
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
			if startIdx != -1 && endIdx > startIdx {
				// REFERENCES is ten characters. Slicing from nine left its
				// final S on the front of the table name, so the generator
				// wrote REFERENCES S customers.
				tableName := bytes.TrimSpace(refPart[len("REFERENCES"):startIdx])
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
			if startIdx != -1 && endIdx > idx+startIdx {
				constraint.CheckExpression = string(bytes.TrimSpace(def[idx+startIdx+1 : endIdx]))
			}
		}
	}

	return constraint
}

// extractColumns extracts column names from a constraint definition.
func (s *SQLServer) extractColumns(def []byte, afterKeyword string) []string {
	upperDef := keyword.UpperASCIIBytes(def)
	keyword := []byte(afterKeyword)

	if idx := bytes.Index(upperDef, keyword); idx != -1 {
		rest := def[idx+len(keyword):]
		// Present is not enough: the pair has to be in order, or the slice runs
		// backwards and panics.
		startIdx := bytes.Index(rest, []byte("("))
		endIdx := bytes.Index(rest, []byte(")"))
		if startIdx != -1 && endIdx > startIdx {
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

	// SSMS opens every script it generates with these two, and a filtered index
	// is refused without them: "CREATE INDEX failed because the following SET
	// options have incorrect settings: QUOTED_IDENTIFIER" (Msg 1934). An indexed
	// view and a computed column want the same, so they are stated once at the
	// top rather than guessed at per statement.
	s.buf.WriteString(mssScriptPreamble)

	// Sequences come first: a column default may name one.
	for _, seq := range schema.Sequences {
		fmt.Fprintf(s.buf, "%s;\nGO\n", s.generateSequenceSQL(seq))
	}

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
			fmt.Fprintf(s.buf, "ALTER TABLE %s ADD %s;\nGO\n", table.Name, s.generateConstraintSQL(c, table.Name))
		}
	}

	// Views are emitted last so the tables they select from already exist.
	for _, view := range schema.Views {
		fmt.Fprintf(s.buf, "%s;\nGO\n", s.generateViewSQL(view))
	}

	// SQL Server keeps a comment in an extended property, which is a call rather
	// than a statement.
	for _, table := range tables {
		for _, c := range sqlmapper.CommentStatements(table) {
			s.buf.WriteString(mssExtendedProperty(c))
		}
	}

	// Routines come after everything they can refer to.
	if routines := s.generateRoutinesSQL(schema); routines != "" {
		s.buf.WriteString("\n")
		s.buf.WriteString(routines)
	}

	// Grants come last: they name tables, views and routines, all of which have
	// to exist before the grant is read.
	if perms := s.generatePermissionsSQL(schema); perms != "" {
		s.buf.WriteString("\n")
		s.buf.WriteString(perms)
	}

	return s.buf.String(), nil
}

// mssGrantRe and mssRevokeRe read an object grant. T-SQL brackets both the
// object and the grantee, and spells the grant option WITH GRANT OPTION.
var mssGrantRe = regexp.MustCompile(`(?i)^\s*GRANT\s+(.+?)\s+ON\s+([\[\]."\w]+)\s+TO\s+([\[\]."\w]+)(\s+WITH\s+GRANT\s+OPTION)?\s*;?\s*$`)

var mssRevokeRe = regexp.MustCompile(`(?i)^\s*REVOKE\s+(.+?)\s+ON\s+([\[\]."\w]+)\s+FROM\s+([\[\]."\w]+)\s*;?\s*$`)

// parsePermission reads a GRANT or REVOKE. Nothing read these, so a SQL Server
// schema arrived with no access control at all while PostgreSQL and MySQL kept
// theirs.
func (s *SQLServer) parsePermission(stmt []byte) bool {
	text := string(bytes.TrimSpace(stmt))
	if m := mssGrantRe.FindStringSubmatch(text); m != nil {
		s.schema.Permissions = append(s.schema.Permissions, sqlmapper.Permission{
			Type:       "GRANT",
			Privileges: sqlmapper.SplitPrivileges(m[1]),
			Object:     unbracketIdent(m[2]),
			Grantee:    unbracketIdent(m[3]),
			WithGrant:  strings.TrimSpace(m[4]) != "",
		})
		return true
	}
	if m := mssRevokeRe.FindStringSubmatch(text); m != nil {
		s.schema.Permissions = append(s.schema.Permissions, sqlmapper.Permission{
			Type:       "REVOKE",
			Privileges: sqlmapper.SplitPrivileges(m[1]),
			Object:     unbracketIdent(m[2]),
			Grantee:    unbracketIdent(m[3]),
		})
		return true
	}
	return false
}

// mssScriptPreamble is what SSMS opens every generated script with. A filtered
// index is refused without it: "CREATE INDEX failed because the following SET
// options have incorrect settings: 'QUOTED_IDENTIFIER'" (Msg 1934). An indexed
// view and a persisted computed column want the same.
const mssScriptPreamble = "SET ANSI_NULLS ON;\nGO\nSET QUOTED_IDENTIFIER ON;\nGO\n\n"

// mssSequenceNameRe reads the name off a CREATE SEQUENCE, which SSMS brackets
// and schema-qualifies.
var mssSequenceNameRe = regexp.MustCompile(`(?i)^\s*CREATE\s+SEQUENCE\s+([\[\]."\w]+)`)

// parseCreateSequence reads a SQL Server sequence.
//
// SQL Server has had sequences since 2012 and this package read none of them, so
// every sequence in a PostgreSQL or Oracle schema was dropped on the way in and
// on the way out. The options are matched one at a time because SQL Server does
// not fix their order.
func (s *SQLServer) parseCreateSequence(stmt []byte) (sqlmapper.Sequence, error) {
	m := mssSequenceNameRe.FindSubmatch(stmt)
	if m == nil {
		return sqlmapper.Sequence{}, fmt.Errorf("invalid CREATE SEQUENCE statement")
	}

	seq := sqlmapper.Sequence{StartValue: 1, IncrementBy: 1}
	seq.Name = splitBracketedName(string(m[1]))
	if raw := string(m[1]); strings.Contains(raw, ".") {
		seq.Schema = unbracketIdent(strings.SplitN(raw, ".", 2)[0])
	}

	text := string(stmt)
	readInt := func(pattern string, target *int) {
		if v := regexp.MustCompile(pattern).FindStringSubmatch(text); len(v) > 1 {
			n, err := strconv.Atoi(v[1])
			if err == nil {
				*target = n
			}
		}
	}
	readInt(`(?i)START\s+WITH\s+(-?\d+)`, &seq.StartValue)
	readInt(`(?i)INCREMENT\s+BY\s+(-?\d+)`, &seq.IncrementBy)
	readInt(`(?i)(?:^|\s)MINVALUE\s+(-?\d+)`, &seq.MinValue)
	readInt(`(?i)(?:^|\s)MAXVALUE\s+(-?\d+)`, &seq.MaxValue)
	readInt(`(?i)(?:^|\s)CACHE\s+(-?\d+)`, &seq.Cache)

	upper := strings.ToUpper(text)
	seq.Cycle = strings.Contains(upper, "CYCLE") && !strings.Contains(upper, "NO CYCLE")
	// A cache of one is how the schema states no cache at all, which is what the
	// other dialects' NOCACHE means.
	if strings.Contains(upper, "NO CACHE") {
		seq.Cache = 1
	}

	return seq, nil
}

// generateSequenceSQL renders a sequence, without its terminator.
func (s *SQLServer) generateSequenceSQL(seq sqlmapper.Sequence) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "CREATE SEQUENCE %s AS BIGINT", seq.Name)
	if seq.StartValue > 0 {
		fmt.Fprintf(&sb, " START WITH %d", seq.StartValue)
	}
	if seq.IncrementBy > 0 {
		fmt.Fprintf(&sb, " INCREMENT BY %d", seq.IncrementBy)
	}
	if seq.MinValue > 0 {
		fmt.Fprintf(&sb, " MINVALUE %d", seq.MinValue)
	}
	if seq.MaxValue > 0 {
		fmt.Fprintf(&sb, " MAXVALUE %d", seq.MaxValue)
	}
	// SQL Server spells it NO CACHE, in two words, and a cache of one is how the
	// schema carries that from PostgreSQL, which writes CACHE 1 for no cache.
	switch {
	case seq.Cache > 1:
		fmt.Fprintf(&sb, " CACHE %d", seq.Cache)
	case seq.Cache == 1:
		sb.WriteString(" NO CACHE")
	}
	if seq.Cycle {
		sb.WriteString(" CYCLE")
	}
	return sb.String()
}

// mssIndexHeaderRe reads a CREATE INDEX header. UNIQUE and CLUSTERED /
// NONCLUSTERED are both optional, and each shifts every fixed token position, so
// the header is matched rather than counted.
var mssIndexHeaderRe = regexp.MustCompile(`(?i)^\s*CREATE\s+(UNIQUE\s+)?(CLUSTERED\s+|NONCLUSTERED\s+)?INDEX\s+([\[\]."\w]+)\s+ON\s+([\[\]."\w]+)\s*\(([^)]*)\)`)

// mssIndexFilterRe reads the WHERE of a filtered index, which follows the column
// list and precedes the storage clauses. Losing it turns a filtered index into a
// full one, and a filtered UNIQUE index into a constraint the source never had:
// rows that were legal before start failing to insert.
var mssIndexFilterRe = regexp.MustCompile(`(?i)\)\s*WHERE\s+(.+?)(?:\s+WITH\s*\(|\s+ON\s+[\[\w"]|\s*;|\s*$)`)

// readIndex reads a CREATE INDEX and returns the index with the table it belongs
// to. Both the whole-file parser and the stream call it. There used to be a
// second regex in the stream package for the same job, and the pair disagreed:
// the stream one had no filter clause, so a filtered index read a statement at a
// time came back as a full one.
func readIndex(stmt []byte) (sqlmapper.Index, string, error) {
	m := mssIndexHeaderRe.FindSubmatch(stmt)
	if m == nil {
		return sqlmapper.Index{}, "", fmt.Errorf("invalid CREATE INDEX statement")
	}

	index := sqlmapper.Index{
		Name:        unbracketIdent(string(m[3])),
		Columns:     unbracketList(string(m[5])),
		IsUnique:    len(bytes.TrimSpace(m[1])) > 0,
		IsClustered: strings.EqualFold(strings.TrimSpace(string(m[2])), "CLUSTERED"),
	}
	if f := mssIndexFilterRe.FindSubmatch(stmt); f != nil {
		index.Condition = expr.Normalize(string(bytes.TrimSpace(f[1])))
	}
	return index, splitBracketedName(string(m[4])), nil
}

// parseCreateIndex parses a CREATE INDEX statement and adds the index to the appropriate table.
func (s *SQLServer) parseCreateIndex(stmt []byte) error {
	index, tableName, err := readIndex(stmt)
	if err != nil {
		return err
	}

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
	upperStmt := keyword.UpperASCIIBytes(stmt)
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
// mssViewHeaderRe reads the view name, allowing the OR ALTER SQL Server writes
// for a view that may already exist.
var mssViewHeaderRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:OR\s+ALTER\s+)?VIEW\s+([\[\]."\w]+)`)

func (s *SQLServer) parseCreateView(stmt []byte) (sqlmapper.View, error) {
	view := sqlmapper.View{}

	// Extract view name. Reading the third field breaks on CREATE OR ALTER
	// VIEW, which SQL Server has written since 2016 SP1: the name is the fifth
	// field there and the third is ALTER.
	m := mssViewHeaderRe.FindSubmatch(stmt)
	if m == nil {
		return view, fmt.Errorf("invalid CREATE VIEW statement")
	}
	view.Name = splitBracketedName(string(m[1]))

	// Extract view definition
	if idx := bytes.Index(keyword.UpperASCIIBytes(stmt), []byte(" AS ")); idx != -1 {
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

	// The name may be schema-qualified and bracketed, [dbo].[bump]. Trimming
	// the outer brackets alone left the qualifier and one bracket behind, so the
	// trigger came out named dbo].[bump.
	trigger.Name = splitBracketedName(string(parts[2]))

	upperStmt := keyword.UpperASCIIBytes(stmt)

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

// parseTablesFromStatement reads one CREATE TABLE with the same code the file
// parser uses. The stream path had a second implementation of its own, and on a
// real SSMS script it left the brackets on every name and found one column.
func (s *SQLServer) parseTablesFromStatement(stmt []byte) error {
	table, err := s.parseCreateTable(stmt)
	if err != nil {
		return err
	}
	s.schema.Tables = append(s.schema.Tables, table)
	return nil
}

// mssRoutineRe reads a CREATE FUNCTION or CREATE PROCEDURE. The parameter list
// is optional, because a procedure that takes none is written without one, and
// the body runs to the end of the batch.
var mssRoutineRe = regexp.MustCompile(`(?is)CREATE\s+(FUNCTION|PROCEDURE|PROC)\s+([.\w\[\]]+)` +
	`\s*(.*?)\s*(?:RETURNS\s+(\w+(?:\s*\([^)]*\))?)\s*)?\bAS\b\s+(.*)$`)

func (s *SQLServer) parseFunctions(statement string) error {
	matches := mssRoutineRe.FindStringSubmatch(statement)

	if len(matches) > 5 {
		isProc := !strings.EqualFold(matches[1], "FUNCTION")
		functionName := matches[2]
		function := sqlmapper.Function{
			IsProc: isProc,
			// Everything after AS is the body, BEGIN and END included. Matching
			// what sat between them dropped the keywords, and a body without
			// them runs only its first statement.
			Body: strings.TrimSpace(matches[5]),
		}

		if !isProc && matches[4] != "" {
			function.Returns = matches[4]
		}

		// Parse schema if exists
		parts := strings.Split(strings.Trim(functionName, "[]"), ".")
		if len(parts) > 1 {
			function.Schema = unbracketIdent(parts[0])
			function.Name = unbracketIdent(parts[1])
		} else {
			function.Name = unbracketIdent(functionName)
		}

		// Parse parameters. A procedure may write them without parentheses,
		// "archive_orders @cutoff DATE", which T-SQL allows and the pattern has
		// to leave room for.
		if raw := strings.TrimSpace(strings.Trim(strings.TrimSpace(matches[3]), "()")); raw != "" {
			params := strings.Split(raw, ",")
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

	// SQL Server calls a partial index a filtered index and spells it the same
	// way. The condition is translated because the source may write a bare
	// boolean, which has no type here.
	if index.Condition != "" {
		sql += " WHERE " + expr.ConditionWithBooleans(index.Condition, expr.SQLServer, nil)
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

// atoi reads a base-10 integer out of a string that a pattern has already
// matched as digits. Nothing malformed can reach here, and zero is the right
// answer if anything ever does.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// generateRoutinesSQL renders the routine section of a dump. Batches are
// separated by GO, which is what the client splits on; the semicolon inside a
// body does not end the batch.
func (s *SQLServer) generateRoutinesSQL(schema *sqlmapper.Schema) string {
	if routine.Count(schema) == 0 {
		return ""
	}
	if !schema.RoutinesAreNativeTo(sqlmapper.SQLServer) {
		return routine.ForeignSQL(schema)
	}

	var stmts []string

	for _, fn := range schema.Functions {
		if fn.IsProc {
			stmts = append(stmts, fmt.Sprintf("CREATE PROCEDURE %s%s\nAS\n%s",
				fn.Name, mssParams(fn.Parameters), strings.TrimSpace(fn.Body)))
			continue
		}
		stmts = append(stmts, fmt.Sprintf("CREATE FUNCTION %s%s\nRETURNS %s\nAS\n%s",
			fn.Name, mssParams(fn.Parameters), fn.Returns, strings.TrimSpace(fn.Body)))
	}

	for _, pr := range schema.Procedures {
		stmts = append(stmts, fmt.Sprintf("CREATE PROCEDURE %s%s\nAS\n%s",
			pr.Name, mssParams(pr.Parameters), strings.TrimSpace(pr.Body)))
	}

	for _, tr := range schema.Triggers {
		stmts = append(stmts, fmt.Sprintf("CREATE TRIGGER %s ON %s\n%s %s\nAS\n%s",
			tr.Name, tr.Table, tr.Timing, tr.Event, strings.TrimSpace(tr.Body)))
	}

	var sb strings.Builder
	for _, stmt := range stmts {
		sb.WriteString(strings.TrimRight(stmt, " \n"))
		sb.WriteString("\nGO\n\n")
	}
	return sb.String()
}

// mssParams renders a parameter list with the @ every T-SQL parameter carries.
// An empty list is written as nothing at all, which is how a procedure with no
// parameters is declared.
func mssParams(params []sqlmapper.Parameter) string {
	if len(params) == 0 {
		return ""
	}
	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, "@"+strings.TrimPrefix(p.Name, "@")+" "+p.DataType)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// generatePermissionsSQL renders the grants the schema carries.
//
// Nothing wrote these. Two dialects read a GRANT into the schema and all five
// dropped it again on the way out, so a converted schema quietly had different
// access than the one it came from. That is the same defect a dropped comment
// had, except this one is a security boundary: an application that lost its
// SELECT grant fails closed, and one that kept a REVOKE it should have lost
// fails open.
//
// The roles named here are not created. They have to exist on the target before
// the script runs, and the load says so plainly if they do not.
//
// T-SQL has no ALL PRIVILEGES and no WITH GRANT OPTION spelled that way: the
// widest object privilege is ALL and the grant option is WITH GRANT_OPTION.
func (s *SQLServer) generatePermissionsSQL(schema *sqlmapper.Schema) string {
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
		privs := sqlmapper.PrivilegeList(perm.Privileges)
		if strings.EqualFold(privs, "ALL PRIVILEGES") {
			privs = "ALL"
		}
		if strings.EqualFold(perm.Type, "REVOKE") {
			fmt.Fprintf(&sb, "REVOKE %s ON %s FROM %s;\nGO\n", privs, object, user)
			continue
		}
		fmt.Fprintf(&sb, "GRANT %s ON %s TO %s", privs, object, user)
		if perm.WithGrant {
			sb.WriteString(" WITH GRANT OPTION")
		}
		sb.WriteString(";\nGO\n")
	}
	return sb.String()
}

// generateViewSQL renders a view definition, without its terminator.
//
// Both Generate and GenerateStream call it. The stream used to write the
// definition as it stood, and SQL Server has no boolean, so a view saying
// WHERE is_active did not load there.
func (s *SQLServer) generateViewSQL(view sqlmapper.View) string {
	body := expr.TranslateViewBody(strings.TrimSuffix(strings.TrimSpace(view.Definition), ";"), expr.SQLServer)
	return fmt.Sprintf("CREATE VIEW %s AS %s", view.Name, body)
}

// mssReferentialAction maps a foreign key's rule onto what SQL Server accepts.
// It has NO ACTION, CASCADE, SET NULL and SET DEFAULT; RESTRICT is what the
// standard calls the same behaviour as NO ACTION, and writing it produced
// "Incorrect syntax near the keyword 'RESTRICT'".
func mssReferentialAction(rule string) string {
	switch strings.ToUpper(strings.Join(strings.Fields(rule), " ")) {
	case "":
		return ""
	case "RESTRICT", "NO ACTION":
		return "NO ACTION"
	case "CASCADE":
		return "CASCADE"
	case "SET NULL":
		return "SET NULL"
	case "SET DEFAULT":
		return "SET DEFAULT"
	}
	return ""
}

// SQL Server keeps a comment in an extended property rather than in the DDL,
// and SSMS scripts it as a call:
//
//	EXEC sys.sp_addextendedproperty @name=N'MS_Description', @value=N'...',
//	    @level0type=N'SCHEMA', @level0name=N'dbo',
//	    @level1type=N'TABLE',  @level1name=N'customers',
//	    @level2type=N'COLUMN', @level2name=N'email';
//
// Nothing read those, so a commented SQL Server schema arrived with none of them
// while the other four dialects kept theirs.
var (
	mssExtendedPropertyRe = regexp.MustCompile(`(?is)^\s*EXEC(?:UTE)?\s+(?:sys\.)?sp_addextendedproperty\b`)
	mssPropertyArgRe      = regexp.MustCompile(`(?is)@(\w+)\s*=\s*N?'((?:[^']|'')*)'`)
)

// applyExtendedProperty records a description stated on a table or a column.
func (s *SQLServer) applyExtendedProperty(stmt string) {
	args := map[string]string{}
	for _, m := range mssPropertyArgRe.FindAllStringSubmatch(stmt, -1) {
		args[strings.ToLower(m[1])] = strings.ReplaceAll(m[2], "''", "'")
	}

	if !strings.EqualFold(args["name"], "MS_Description") {
		return
	}
	if !strings.EqualFold(args["level1type"], "TABLE") {
		return
	}

	table := s.findTable(args["level1name"])
	if table == nil {
		return
	}

	if strings.EqualFold(args["level2type"], "COLUMN") {
		for i := range table.Columns {
			if strings.EqualFold(table.Columns[i].Name, args["level2name"]) {
				table.Columns[i].Comment = args["value"]
				return
			}
		}
		return
	}

	table.Comment = args["value"]
}

// findTable returns the table by name, or nil when the script has not declared
// it yet.
func (s *SQLServer) findTable(name string) *sqlmapper.Table {
	name = splitBracketedName(name)
	for i := range s.schema.Tables {
		if strings.EqualFold(s.schema.Tables[i].Name, name) {
			return &s.schema.Tables[i]
		}
	}
	return nil
}

// mssExtendedProperty renders a comment the way SQL Server stores one.
func mssExtendedProperty(c sqlmapper.Comment) string {
	table, column := c.Name, ""
	if i := strings.LastIndex(c.Name, "."); i != -1 && c.Object == "COLUMN" {
		table, column = c.Name[:i], c.Name[i+1:]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "EXEC sys.sp_addextendedproperty @name=N'MS_Description', @value=N'%s',\n",
		strings.ReplaceAll(c.Comment, "'", "''"))
	fmt.Fprintf(&sb, "    @level0type=N'SCHEMA', @level0name=N'dbo',\n")
	fmt.Fprintf(&sb, "    @level1type=N'TABLE', @level1name=N'%s'", table)
	if column != "" {
		fmt.Fprintf(&sb, ",\n    @level2type=N'COLUMN', @level2name=N'%s'", column)
	}
	sb.WriteString(";\nGO\n")
	return sb.String()
}
