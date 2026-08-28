// Package mysql provides functionality for parsing and generating MySQL database schemas.
// It implements the Parser interface for handling MySQL specific SQL syntax and schema structures.
package mysql

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/internal/expr"
	"github.com/mstgnz/sqlmapper/internal/keyword"
	"github.com/mstgnz/sqlmapper/internal/routine"
	"github.com/mstgnz/sqlmapper/internal/sqlfmt"
)

// toMySQLType maps any source database type (MySQL or PostgreSQL) to the MySQL equivalent.
// This is used during Generate to convert parsed schemas to valid MySQL SQL.
var toMySQLType = map[string]string{
	// MySQL native types (passthrough)
	"tinyint":    "tinyint",
	"smallint":   "smallint",
	"mediumint":  "mediumint",
	"int":        "int",
	"bigint":     "bigint",
	"float":      "float",
	"double":     "double",
	"decimal":    "decimal",
	"numeric":    "decimal",
	"char":       "char",
	"varchar":    "varchar",
	"tinytext":   "tinytext",
	"text":       "text",
	"mediumtext": "mediumtext",
	"longtext":   "longtext",
	"json":       "json",
	"datetime":   "datetime",
	"date":       "date",
	"time":       "time",
	"blob":       "blob",
	"tinyblob":   "tinyblob",
	"mediumblob": "mediumblob",
	"longblob":   "longblob",
	"enum":       "enum",
	"set":        "set",
	"bool":       "tinyint(1)",
	"boolean":    "tinyint(1)",
	"bit":        "bit",
	"year":       "year",
	"timestamp":  "datetime",
	// PostgreSQL types → MySQL equivalents
	"integer":                     "int",
	"serial":                      "int",
	"bigserial":                   "bigint",
	"smallserial":                 "smallint",
	"real":                        "float",
	"double precision":            "double",
	"character varying":           "varchar",
	"character":                   "char",
	"bytea":                       "blob",
	"jsonb":                       "json",
	"uuid":                        "varchar(36)",
	"inet":                        "varchar(45)",
	"cidr":                        "varchar(45)",
	"macaddr":                     "varchar(17)",
	"interval":                    "varchar(255)",
	"point":                       "point",
	"line":                        "linestring",
	"lseg":                        "linestring",
	"box":                         "polygon",
	"path":                        "linestring",
	"polygon":                     "polygon",
	"circle":                      "polygon",
	"timestamptz":                 "datetime",
	"timestamp with time zone":    "datetime",
	"timestamp without time zone": "datetime",
}

// Package-level compiled regexes for performance.
var (
	mysqlCommentRe    = regexp.MustCompile(`(?m)--.*$|#.*$`)
	mysqlDelimiterRe  = regexp.MustCompile(`(?i)DELIMITER\s+[^\s]+`)
	mysqlWhitespaceRe = regexp.MustCompile(`\s+`)
	// A /*! ... */ block is not a comment: MySQL executes what is inside it when
	// the server is new enough, and mysqldump wraps every routine in one. This
	// keeps the contents and drops only the wrapper. Stripping the block whole
	// deleted every trigger, function and procedure a real dump contained.
	mysqlConditionalRe  = regexp.MustCompile(`(?s)/\*!\d*(.*?)\*/`)
	mysqlBlockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`) // /* regular block comments */
	mysqlLockRe         = regexp.MustCompile(`(?i)LOCK\s+TABLES\s+[^;]+;`)
	mysqlUnlockRe       = regexp.MustCompile(`(?i)UNLOCK\s+TABLES\s*;`)
	mysqlDBRe           = regexp.MustCompile(`(?i)CREATE\s+DATABASE\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)
	mysqlIndexRe        = regexp.MustCompile(`(?i)CREATE\s+(UNIQUE\s+)?(?:FULLTEXT\s+)?INDEX\s+(\w+)\s+ON\s+` + "`?" + `([\w.]+)` + "`?" + `\s*\((.*?)\)`)
	// mysqldump writes a view with the attributes it was created with, each in
	// its own version block:
	//
	//	/*!50001 CREATE ALGORITHM=UNDEFINED */
	//	/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */
	//	/*!50001 VIEW `v` AS select ... */;
	//
	// so everything between CREATE and VIEW is optional.
	mysqlViewRe = regexp.MustCompile(`(?i)CREATE(?:\s+OR\s+REPLACE)?` +
		`(?:\s+ALGORITHM\s*=\s*\S+)?(?:\s+DEFINER\s*=\s*\S+)?(?:\s+SQL\s+SECURITY\s+\w+)?` +
		`\s+VIEW\s+` + "`?" + `([\w.]+)` + "`?" + `\s+AS\s+(.*?);`)
	// mysqldump writes a routine with a DEFINER clause and backtick-quoted
	// names, so both are optional here. Without them a dumped trigger was read
	// as no trigger at all.
	mysqlFuncRe         = regexp.MustCompile(`(?i)CREATE\s+` + mysqlDefiner + "`?" + `([\w.]+)` + "`?" + `\s*\((.*?)\)\s+RETURNS\s+(\w+)\s+BEGIN\s+(.*?)\s+END`)
	mysqlProcRe         = regexp.MustCompile(`(?i)CREATE\s+` + mysqlDefinerProc + "`?" + `([\w.]+)` + "`?" + `\s*\((.*?)\)\s+BEGIN\s+(.*?)\s+END`)
	mysqlTriggerRe      = regexp.MustCompile(`(?i)CREATE\s+` + mysqlDefinerTrigger + "`?" + `([\w.]+)` + "`?" + `\s+(BEFORE|AFTER)\s+(INSERT|UPDATE|DELETE)\s+ON\s+` + "`?" + `([\w.]+)` + "`?" + `\s+FOR\s+EACH\s+ROW\s+BEGIN\s+(.*?)\s+END`)
	mysqlGrantRe        = regexp.MustCompile(`(?i)GRANT\s+(.*?)\s+ON\s+([\w.*]+)\s+TO\s+'([^']+)'@'([^']+)'(?:\s+WITH\s+GRANT\s+OPTION)?;`)
	mysqlGrantProcRe    = regexp.MustCompile(`(?i)GRANT\s+EXECUTE\s+ON\s+(?:PROCEDURE|FUNCTION)\s+(\w+)\s+TO\s+'([^']+)'@'([^']+)'(?:\s+WITH\s+GRANT\s+OPTION)?;`)
	mysqlRevokeRe       = regexp.MustCompile(`(?i)REVOKE\s+(.*?)\s+ON\s+([\w.*]+)\s+FROM\s+'([^']+)'@'([^']+)';`)
	mysqlTableCommentRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + "`?" + `([\w.]+)` + "`?" + `\s+COMMENT\s*=\s*'([^']+)';`)
	mysqlColCommentRe   = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+` + "`?" + `([\w.]+)` + "`?" + `\s+MODIFY\s+COLUMN\s+(\w+)[^']+COMMENT\s*'([^']+)';`)
	mysqlEnumValuesRe   = regexp.MustCompile(`(?i)(ENUM|SET)\s*\(([^)]+)\)`)
	mysqlEnumItemRe     = regexp.MustCompile(`'([^']*)'`)
	mysqlTypeWithLenRe  = regexp.MustCompile(`^(\w+)\s*\((\d+)(?:\s*,\s*(\d+))?\)$`)
	mysqlCheckRe        = regexp.MustCompile(`(?i)CHECK\s*\((.*)\)`)
	mysqlConstraintRe   = regexp.MustCompile(`(?i)CONSTRAINT\s+` + "`?" + `(\w+)` + "`?" + `\s+(.*)`)
	// A key column may carry a prefix length, PRIMARY KEY (email(255)), so the
	// list has to tolerate one level of nesting. Stopping at the first closing
	// parenthesis captured "email(255" and wrote a list that never closed.
	mysqlPKRe     = regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(((?:[^()]|\([^)]*\))+)\)`)
	mysqlFKRe     = regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(([^)]+)\)\s*REFERENCES\s+` + "`?" + `([\w.]+)` + "`?" + `\s*\(([^)]+)\)`)
	mysqlUniqueRe = regexp.MustCompile(`(?i)UNIQUE\s*(?:KEY\s+(\w+)\s*)?\(((?:[^()]|\([^)]*\))+)\)`)
)

// mysqlDefaultStopWords marks the tokens that end a DEFAULT clause. NULL is
// absent on purpose: "DEFAULT NULL" is a value, not an attribute. ON is present
// so that "DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP" does not read the update
// trigger as the column's default.
var mysqlDefaultStopWords = map[string]bool{
	"ON": true, "NOT": true, "AUTO_INCREMENT": true, "COMMENT": true,
	"PRIMARY": true, "UNIQUE": true, "CHECK": true, "REFERENCES": true,
	"COLLATE": true, "CHARACTER": true, "GENERATED": true, "STORAGE": true,
	"VIRTUAL": true, "STORED": true, "INVISIBLE": true,
}

// takeUntilStopWord returns the leading tokens of s up to the first stop word
// that appears at paren depth zero and outside a string literal.
func takeUntilStopWord(s string, stop map[string]bool) string {
	var out []string
	depth := 0
	inString := false
	for _, tok := range strings.Fields(s) {
		if depth == 0 && !inString && stop[strings.ToUpper(tok)] {
			break
		}
		out = append(out, tok)
		for _, ch := range tok {
			switch ch {
			case '\'':
				inString = !inString
			case '(':
				if !inString {
					depth++
				}
			case ')':
				if !inString {
					depth--
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

// isDeferred reports whether a constraint is in the deferred set, matched on
// name when there is one and on shape otherwise.
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

// stripSchemaPrefix reduces "schema.table" to "table"; MySQL resolves unqualified
// names through the active database and a PostgreSQL "public." prefix would break
// the reference outright.
func stripSchemaPrefix(name string) string {
	name = strings.TrimSpace(strings.Trim(name, `"`))
	if parts := strings.Split(name, "."); len(parts) > 1 {
		return strings.Trim(parts[len(parts)-1], `"`)
	}
	return name
}

// columnIsAutoIncrement reports whether the named column of table is an
// auto-increment column.
func columnIsAutoIncrement(table sqlmapper.Table, name string) bool {
	for _, col := range table.Columns {
		if col.Name == name {
			return col.AutoIncrement
		}
	}
	return false
}

// MySQL represents a MySQL parser implementation.
type MySQL struct {
	schema *sqlmapper.Schema
}

// NewMySQL creates and initializes a new MySQL parser instance.
func NewMySQL() sqlmapper.Database {
	return &MySQL{
		schema: &sqlmapper.Schema{SourceDialect: sqlmapper.MySQL},
	}
}

// Parse takes a MySQL SQL dump and parses it into a common schema structure.
func (m *MySQL) Parse(content string) (*sqlmapper.Schema, error) {
	if content == "" {
		return nil, errors.New("empty content")
	}

	content = m.normalizeContent(content)

	if err := m.parseSchemas(content); err != nil {
		return nil, fmt.Errorf("error parsing schemas: %v", err)
	}
	if err := m.parseTables(content); err != nil {
		return nil, fmt.Errorf("error parsing tables: %v", err)
	}
	if err := m.parseIndexes(content); err != nil {
		return nil, fmt.Errorf("error parsing indexes: %v", err)
	}
	if err := m.parseViews(content); err != nil {
		return nil, fmt.Errorf("error parsing views: %v", err)
	}
	if err := m.parseFunctions(content); err != nil {
		return nil, fmt.Errorf("error parsing functions: %v", err)
	}
	if err := m.parseTriggers(content); err != nil {
		return nil, fmt.Errorf("error parsing triggers: %v", err)
	}
	if err := m.parsePermissions(content); err != nil {
		return nil, fmt.Errorf("error parsing permissions: %v", err)
	}

	return m.schema, nil
}

// Generate creates MySQL SQL from a schema structure.
// It applies type mapping so it correctly handles schemas parsed from PostgreSQL.
func (m *MySQL) Generate(schema *sqlmapper.Schema) (string, error) {
	if schema == nil {
		return "", errors.New("empty schema")
	}

	var result strings.Builder

	// Dump tools do not order tables by dependency, so a child table can precede
	// its parent and the foreign key would fail to resolve.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)

	for i, table := range tables {
		result.WriteString(m.generateTableSQL(table, deferredFKs[table.Name]))
		if i < len(tables)-1 {
			result.WriteString("\n\n")
		}

		if len(table.Indexes) > 0 {
			result.WriteString("\n")
			for _, index := range table.Indexes {
				result.WriteString(m.generateIndexSQL(table.Name, index, columnsByName(table)))
				result.WriteString("\n")
			}
		}
	}

	// Foreign keys that close a cycle cannot be satisfied by ordering, so they
	// are added once every table exists.
	for _, table := range tables {
		for _, c := range deferredFKs[table.Name] {
			result.WriteString(fmt.Sprintf("\nALTER TABLE %s ADD %s;\n", table.Name, m.generateConstraintSQL(c, columnsByName(table))))
		}
	}

	// Views are emitted last so the tables they select from already exist. The
	// body is carried over verbatim: this package converts DDL structure, not
	// query syntax, so a view written in another dialect's SQL may need editing.
	for _, view := range schema.Views {
		result.WriteString("\n")
		result.WriteString(m.generateViewSQL(view))
		result.WriteString("\n")
	}

	// Routines come after everything they can refer to.
	if routines := m.generateRoutinesSQL(schema); routines != "" {
		result.WriteString("\n")
		result.WriteString(routines)
	}

	return result.String(), nil
}

// generateViewSQL renders a view definition. The SELECT body is passed through
// unchanged; only the wrapper is dialect-specific. MySQL has no materialized
// views, so one arriving from PostgreSQL or Oracle degrades to a plain view.
func (m *MySQL) generateViewSQL(view sqlmapper.View) string {
	body := expr.TranslateViewBody(strings.TrimSpace(view.Definition), expr.MySQL)
	body = strings.TrimSuffix(body, ";")
	return fmt.Sprintf("CREATE VIEW %s AS %s;", stripSchemaPrefix(view.Name), body)
}

// normalizeContent preprocesses SQL by removing comments, DELIMITER statements,
// backtick quoting, and normalizing whitespace.
func (m *MySQL) normalizeContent(content string) string {
	// Unwrap mysqldump conditional blocks, keeping the SQL inside them.
	content = mysqlConditionalRe.ReplaceAllString(content, " $1 ")
	// Strip regular block comments
	content = mysqlBlockCommentRe.ReplaceAllString(content, "")
	// Strip line comments (-- and #)
	content = mysqlCommentRe.ReplaceAllString(content, "")
	// Strip DELIMITER statements
	content = mysqlDelimiterRe.ReplaceAllString(content, "")
	// Strip LOCK/UNLOCK TABLES (mysqldump artifact)
	content = mysqlLockRe.ReplaceAllString(content, "")
	content = mysqlUnlockRe.ReplaceAllString(content, "")
	// Strip backticks – MySQL dumps quote everything; we work without them internally
	content = strings.ReplaceAll(content, "`", "")
	content = strings.TrimSpace(content)
	content = mysqlWhitespaceRe.ReplaceAllString(content, " ")
	return content
}

func (m *MySQL) parseSchemas(content string) error {
	if matches := mysqlDBRe.FindStringSubmatch(content); len(matches) > 1 {
		m.schema.Name = matches[1]
	}
	return nil
}

// mysqlDefiner and its siblings match the optional DEFINER clause mysqldump
// writes between CREATE and the object keyword, together with that keyword.
const (
	definerClause       = `(?:DEFINER\s*=\s*\S+\s+)?`
	mysqlDefiner        = definerClause + `FUNCTION\s+`
	mysqlDefinerProc    = definerClause + `PROCEDURE\s+`
	mysqlDefinerTrigger = definerClause + `TRIGGER\s+`
)

var mysqlCreateTableRe = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w.]+)\s*\(`)

func (m *MySQL) parseTables(content string) error {
	// Split into statements first to prevent the regex from greedily matching
	// across multiple CREATE TABLE blocks.
	statements := strings.Split(content, ";")
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		loc := mysqlCreateTableRe.FindStringSubmatchIndex(stmt)
		if loc == nil {
			continue
		}
		tableName := stmt[loc[2]:loc[3]]
		openParen := loc[1] - 1

		body, _ := extractBalancedParens(stmt, openParen)
		if body == "" {
			continue
		}

		table := sqlmapper.Table{}
		if parts := strings.Split(tableName, "."); len(parts) > 1 {
			table.Schema = parts[0]
			table.Name = parts[1]
		} else {
			table.Name = tableName
		}

		if err := m.parseColumnsAndConstraints(body, &table); err != nil {
			return err
		}

		// Table-level comment via ALTER TABLE (search in full content)
		if cm := mysqlTableCommentRe.FindStringSubmatch(content); len(cm) > 2 && cm[1] == tableName {
			table.Comment = cm[2]
		}

		// Column comments via ALTER TABLE MODIFY COLUMN
		for _, cc := range mysqlColCommentRe.FindAllStringSubmatch(content, -1) {
			if len(cc) > 3 && cc[1] == tableName {
				for i := range table.Columns {
					if table.Columns[i].Name == cc[2] {
						table.Columns[i].Comment = cc[3]
						break
					}
				}
			}
		}

		for i := range table.Columns {
			table.Columns[i].Order = i + 1
		}
		m.schema.Tables = append(m.schema.Tables, table)
	}
	return nil
}

// extractBalancedParens finds the content between matching parentheses starting at openIdx.
func extractBalancedParens(s string, openIdx int) (string, int) {
	if openIdx >= len(s) || s[openIdx] != '(' {
		return "", -1
	}
	depth := 0
	inString := false
	for i := openIdx; i < len(s); i++ {
		ch := s[i]
		if inString {
			if ch == '\'' {
				inString = false
			}
		} else {
			switch ch {
			case '\'':
				inString = true
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return s[openIdx+1 : i], i
				}
			}
		}
	}
	return "", -1
}

// parseColumnsAndConstraints splits a CREATE TABLE body and processes each part.
func (m *MySQL) parseColumnsAndConstraints(columnDefs string, table *sqlmapper.Table) error {
	defs := strings.Split(columnDefs, ",")
	var finalDefs []string
	var current strings.Builder
	parenCount := 0

	for _, def := range defs {
		parenCount += strings.Count(def, "(") - strings.Count(def, ")")
		if parenCount > 0 {
			current.WriteString(def + ",")
		} else {
			if current.Len() > 0 {
				current.WriteString(def)
				finalDefs = append(finalDefs, current.String())
				current.Reset()
			} else {
				finalDefs = append(finalDefs, def)
			}
		}
	}

	for _, def := range finalDefs {
		def = strings.TrimSpace(def)
		if def == "" {
			continue
		}

		defUpper := strings.ToUpper(def)

		// KEY idx_name (col1, col2) → regular index inside CREATE TABLE (mysqldump style)
		if strings.HasPrefix(defUpper, "KEY ") || strings.HasPrefix(defUpper, "INDEX ") {
			// Extract optional index name and columns
			// Formats: KEY idx_name (cols)  or  KEY (cols)  or  INDEX idx_name (cols)
			keyRe := regexp.MustCompile(`(?i)(?:KEY|INDEX)\s+(\w+)?\s*\(([^)]+)\)`)
			if km := keyRe.FindStringSubmatch(def); len(km) > 2 {
				idx := sqlmapper.Index{
					Columns: splitAndTrim(km[2]),
				}
				if km[1] != "" {
					idx.Name = km[1]
				}
				table.Indexes = append(table.Indexes, idx)
			}
			continue
		}

		// Detect table-level constraints
		isConstraint := strings.HasPrefix(defUpper, "CONSTRAINT") ||
			strings.HasPrefix(defUpper, "PRIMARY KEY") ||
			strings.HasPrefix(defUpper, "FOREIGN KEY") ||
			strings.HasPrefix(defUpper, "UNIQUE") ||
			strings.HasPrefix(defUpper, "CHECK")

		if isConstraint {
			constraint, err := m.parseConstraint(def)
			if err != nil {
				return err
			}
			table.Constraints = append(table.Constraints, constraint)
			continue
		}

		// Column definition
		if strings.Contains(def, " ") {
			column, err := m.parseColumn(def)
			if err != nil {
				return err
			}

			// Inline PRIMARY KEY
			if strings.Contains(defUpper, "PRIMARY KEY") {
				column.IsPrimaryKey = true
				column.IsNullable = false
				table.Constraints = append(table.Constraints, sqlmapper.Constraint{
					Type:    "PRIMARY KEY",
					Columns: []string{column.Name},
				})
			}
			// Inline UNIQUE
			if strings.Contains(defUpper, "UNIQUE") {
				column.IsUnique = true
			}
			// Inline CHECK
			if strings.Contains(defUpper, "CHECK") {
				if m := mysqlCheckRe.FindStringSubmatch(def); len(m) > 1 {
					column.CheckExpression = m[1]
					table.Constraints = append(table.Constraints, sqlmapper.Constraint{
						Type:            "CHECK",
						Columns:         []string{column.Name},
						CheckExpression: m[1],
					})
				}
			}

			table.Columns = append(table.Columns, column)
		}
	}

	return nil
}

// parseColumn processes a single column definition.
func (m *MySQL) parseColumn(def string) (sqlmapper.Column, error) {
	parts := strings.Fields(def)
	if len(parts) < 2 {
		return sqlmapper.Column{}, fmt.Errorf("invalid column definition: %s", def)
	}

	column := sqlmapper.Column{
		Name:       parts[0],
		IsNullable: true,
	}

	defUpper := keyword.UpperASCII(def)

	// Handle ENUM and SET – they contain parenthesised string values
	if enumMatch := mysqlEnumValuesRe.FindStringSubmatch(def); len(enumMatch) > 2 {
		column.DataType = strings.ToLower(enumMatch[1])
		for _, v := range mysqlEnumItemRe.FindAllStringSubmatch(enumMatch[2], -1) {
			column.EnumValues = append(column.EnumValues, v[1])
		}
	} else {
		// Regular type – parts[1] holds the type (possibly with length)
		rawType := parts[1]
		if typeMatch := mysqlTypeWithLenRe.FindStringSubmatch(rawType); len(typeMatch) > 2 {
			column.DataType = strings.ToLower(typeMatch[1])
			column.Length = atoi(typeMatch[2])
			if len(typeMatch) > 3 && typeMatch[3] != "" {
				column.Scale = atoi(typeMatch[3])
			}
		} else {
			column.DataType = strings.ToLower(rawType)
		}
	}

	if strings.Contains(defUpper, "UNSIGNED") {
		column.IsUnsigned = true
	}
	if strings.Contains(defUpper, "AUTO_INCREMENT") {
		column.AutoIncrement = true
	}
	if strings.Contains(defUpper, "PRIMARY KEY") {
		column.IsPrimaryKey = true
		column.IsNullable = false
	}
	if strings.Contains(defUpper, "UNIQUE") {
		column.IsUnique = true
	}
	if strings.Contains(defUpper, "NOT NULL") {
		column.IsNullable = false
	}
	if strings.Contains(defUpper, "CHECK") {
		if cm := mysqlCheckRe.FindStringSubmatch(def); len(cm) > 1 {
			column.CheckExpression = cm[1]
		}
	}

	// DEFAULT value
	if idx := strings.Index(defUpper, "DEFAULT"); idx >= 0 {
		defaultPart := takeUntilStopWord(strings.TrimSpace(def[idx+len("DEFAULT"):]), mysqlDefaultStopWords)
		defaultPart = strings.TrimSuffix(strings.TrimSpace(defaultPart), ",")
		up := strings.ToUpper(defaultPart)

		switch {
		case up == "NULL" || defaultPart == "":
			// Implicit default, nothing to carry over.
		case strings.Contains(up, "CURRENT_TIMESTAMP"):
			column.DefaultValue = "CURRENT_TIMESTAMP"
		case strings.HasPrefix(defaultPart, "'"):
			if m := regexp.MustCompile(`'([^']*)'`).FindStringSubmatch(defaultPart); len(m) > 1 {
				column.DefaultValue = m[1]
			}
		default:
			column.DefaultValue = defaultPart
		}
	}

	return column, nil
}

func (m *MySQL) parseConstraint(def string) (sqlmapper.Constraint, error) {
	constraint := sqlmapper.Constraint{}

	// Strip CONSTRAINT name prefix
	if strings.HasPrefix(strings.ToUpper(def), "CONSTRAINT") {
		if cm := mysqlConstraintRe.FindStringSubmatch(def); len(cm) > 2 {
			constraint.Name = cm[1]
			def = cm[2]
		}
	}

	defUpper := strings.ToUpper(def)

	switch {
	case strings.Contains(defUpper, "PRIMARY KEY"):
		constraint.Type = "PRIMARY KEY"
		if m := mysqlPKRe.FindStringSubmatch(def); len(m) > 1 {
			constraint.Columns = splitKeyColumns(m[1])
		}

	case strings.Contains(defUpper, "FOREIGN KEY"):
		constraint.Type = "FOREIGN KEY"
		if m := mysqlFKRe.FindStringSubmatch(def); len(m) > 3 {
			constraint.Columns = splitKeyColumns(m[1])
			constraint.RefTable = stripSchemaPrefix(m[2])
			constraint.RefColumns = splitKeyColumns(m[3])
		}
		if strings.Contains(defUpper, "ON DELETE") {
			switch {
			case strings.Contains(defUpper, "ON DELETE CASCADE"):
				constraint.DeleteRule = "CASCADE"
			case strings.Contains(defUpper, "ON DELETE SET NULL"):
				constraint.DeleteRule = "SET NULL"
			case strings.Contains(defUpper, "ON DELETE RESTRICT"):
				constraint.DeleteRule = "RESTRICT"
			case strings.Contains(defUpper, "ON DELETE NO ACTION"):
				constraint.DeleteRule = "NO ACTION"
			case strings.Contains(defUpper, "ON DELETE SET DEFAULT"):
				constraint.DeleteRule = "SET DEFAULT"
			}
		}
		if strings.Contains(defUpper, "ON UPDATE") {
			switch {
			case strings.Contains(defUpper, "ON UPDATE CASCADE"):
				constraint.UpdateRule = "CASCADE"
			case strings.Contains(defUpper, "ON UPDATE SET NULL"):
				constraint.UpdateRule = "SET NULL"
			case strings.Contains(defUpper, "ON UPDATE RESTRICT"):
				constraint.UpdateRule = "RESTRICT"
			case strings.Contains(defUpper, "ON UPDATE NO ACTION"):
				constraint.UpdateRule = "NO ACTION"
			}
		}

	case strings.HasPrefix(defUpper, "UNIQUE") || strings.HasPrefix(defUpper, "UNIQUE KEY"):
		constraint.Type = "UNIQUE"
		if m := mysqlUniqueRe.FindStringSubmatch(def); len(m) > 2 {
			// mysqldump puts the index name between the keyword and the column
			// list. Skipping over it dropped a name the target can keep.
			if constraint.Name == "" && m[1] != "" {
				constraint.Name = m[1]
			}
			constraint.Columns = splitKeyColumns(m[2])
		}

	case strings.Contains(defUpper, "CHECK"):
		constraint.Type = "CHECK"
		if m := mysqlCheckRe.FindStringSubmatch(def); len(m) > 1 {
			constraint.CheckExpression = m[1]
		}
	}

	return constraint, nil
}

func (m *MySQL) parseIndexes(content string) error {
	matches := mysqlIndexRe.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) < 5 {
			continue
		}
		isUnique := strings.TrimSpace(match[1]) != ""
		indexName := match[2]
		tableName := match[3]
		columns := splitAndTrim(match[4])

		for i, table := range m.schema.Tables {
			if table.Name == tableName || fmt.Sprintf("%s.%s", table.Schema, table.Name) == tableName {
				m.schema.Tables[i].Indexes = append(m.schema.Tables[i].Indexes, sqlmapper.Index{
					Name:     indexName,
					Columns:  columns,
					IsUnique: isUnique,
				})
				break
			}
		}
	}
	return nil
}

func (m *MySQL) parseViews(content string) error {
	for _, match := range mysqlViewRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 3 {
			continue
		}
		view := sqlmapper.View{Definition: match[2]}
		parts := strings.Split(match[1], ".")
		if len(parts) > 1 {
			view.Schema = parts[0]
			view.Name = parts[1]
		} else {
			view.Name = match[1]
		}
		// mysqldump writes each view twice: a stand-in of SELECT 1 AS col early
		// on, so that anything referring to it can be created, and the real
		// definition at the end. The last one wins, which is how the file is
		// meant to be replayed.
		if i := indexOfView(m.schema.Views, view.Schema, view.Name); i != -1 {
			m.schema.Views[i] = view
			continue
		}
		m.schema.Views = append(m.schema.Views, view)
	}
	return nil
}

func indexOfView(views []sqlmapper.View, schema, name string) int {
	for i, v := range views {
		if v.Name == name && v.Schema == schema {
			return i
		}
	}
	return -1
}

func (m *MySQL) parseFunctions(content string) error {
	for _, match := range mysqlFuncRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 5 {
			continue
		}
		fn := sqlmapper.Function{Returns: match[3], Body: match[4]}
		parts := strings.Split(match[1], ".")
		if len(parts) > 1 {
			fn.Schema = parts[0]
			fn.Name = parts[1]
		} else {
			fn.Name = match[1]
		}
		if match[2] != "" {
			for _, p := range strings.Split(match[2], ",") {
				pp := strings.Fields(strings.TrimSpace(p))
				if len(pp) >= 2 {
					fn.Parameters = append(fn.Parameters, sqlmapper.Parameter{Name: pp[0], DataType: pp[1]})
				}
			}
		}
		m.schema.Functions = append(m.schema.Functions, fn)
	}

	for _, match := range mysqlProcRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 4 {
			continue
		}
		fn := sqlmapper.Function{Body: match[3], IsProc: true}
		parts := strings.Split(match[1], ".")
		if len(parts) > 1 {
			fn.Schema = parts[0]
			fn.Name = parts[1]
		} else {
			fn.Name = match[1]
		}
		if match[2] != "" {
			for _, p := range strings.Split(match[2], ",") {
				pp := strings.Fields(strings.TrimSpace(p))
				if len(pp) >= 3 {
					fn.Parameters = append(fn.Parameters, sqlmapper.Parameter{
						Name: pp[1], DataType: pp[2], Direction: pp[0],
					})
				}
			}
		}
		m.schema.Functions = append(m.schema.Functions, fn)
	}
	return nil
}

func (m *MySQL) parseTriggers(content string) error {
	for _, match := range mysqlTriggerRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 6 {
			continue
		}
		trig := sqlmapper.Trigger{
			Name: match[1], Timing: match[2], Event: match[3],
			Body: match[5], ForEachRow: true,
		}
		parts := strings.Split(match[4], ".")
		if len(parts) > 1 {
			trig.Schema = parts[0]
			trig.Table = parts[1]
		} else {
			trig.Table = match[4]
		}
		m.schema.Triggers = append(m.schema.Triggers, trig)
	}
	return nil
}

func (m *MySQL) parsePermissions(content string) error {
	for _, match := range mysqlGrantRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 5 {
			continue
		}
		privStr := strings.TrimSpace(match[1])
		var privs []string
		if strings.ToUpper(privStr) == "ALL PRIVILEGES" {
			privs = []string{"ALL PRIVILEGES"}
		} else {
			for _, p := range strings.Split(privStr, ",") {
				privs = append(privs, strings.TrimSpace(p))
			}
		}
		m.schema.Permissions = append(m.schema.Permissions, sqlmapper.Permission{
			Type: "GRANT", Privileges: privs, Object: match[2],
			Grantee:   fmt.Sprintf("%s@%s", match[3], match[4]),
			WithGrant: strings.Contains(match[0], "WITH GRANT OPTION"),
		})
	}

	for _, match := range mysqlGrantProcRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 4 {
			continue
		}
		m.schema.Permissions = append(m.schema.Permissions, sqlmapper.Permission{
			Type: "GRANT", Privileges: []string{"EXECUTE"}, Object: match[1],
			Grantee:   fmt.Sprintf("%s@%s", match[2], match[3]),
			WithGrant: strings.Contains(match[0], "WITH GRANT OPTION"),
		})
	}

	for _, match := range mysqlRevokeRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 5 {
			continue
		}
		privStr := strings.TrimSpace(match[1])
		var privs []string
		if strings.ToUpper(privStr) == "ALL PRIVILEGES" {
			privs = []string{"ALL PRIVILEGES"}
		} else {
			for _, p := range strings.Split(privStr, ",") {
				privs = append(privs, strings.TrimSpace(p))
			}
		}
		m.schema.Permissions = append(m.schema.Permissions, sqlmapper.Permission{
			Type: "REVOKE", Privileges: privs, Object: match[2],
			Grantee: fmt.Sprintf("%s@%s", match[3], match[4]),
		})
	}
	return nil
}

// generateTableSQL creates a CREATE TABLE statement, applying type mapping and
// outputting all constraints (PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK).
func (m *MySQL) generateTableSQL(table sqlmapper.Table, deferred []sqlmapper.Constraint) string {
	var result strings.Builder

	result.WriteString(fmt.Sprintf("CREATE TABLE %s (\n", table.Name))

	// A single-column PK on an auto-increment column reads better inline; every
	// other PK is emitted as a table-level constraint. Exactly one of the two must
	// fire, otherwise MySQL rejects the table with "Multiple primary key defined".
	inlinePKCols := map[string]bool{}
	var tableConstraints []sqlmapper.Constraint
	for _, c := range table.Constraints {
		switch c.Type {
		case "PRIMARY KEY":
			if len(c.Columns) == 1 && columnIsAutoIncrement(table, c.Columns[0]) {
				inlinePKCols[c.Columns[0]] = true
				continue
			}
			tableConstraints = append(tableConstraints, c)
		case "FOREIGN KEY":
			if isDeferred(deferred, c) {
				continue
			}
			tableConstraints = append(tableConstraints, c)
		case "UNIQUE", "CHECK":
			tableConstraints = append(tableConstraints, c)
		}
	}

	// Columns flagged as PK without a matching constraint (inline "id int PRIMARY
	// KEY" in the source) still need the marker.
	if len(inlinePKCols) == 0 {
		hasPKConstraint := false
		for _, c := range table.Constraints {
			if c.Type == "PRIMARY KEY" {
				hasPKConstraint = true
				break
			}
		}
		if !hasPKConstraint {
			for _, col := range table.Columns {
				if col.IsPrimaryKey {
					inlinePKCols[col.Name] = true
				}
			}
		}
	}

	totalItems := len(table.Columns) + len(tableConstraints)

	for i, col := range table.Columns {
		result.WriteString("    " + m.generateColumnSQL(col, inlinePKCols[col.Name]))
		if i < totalItems-1 {
			result.WriteString(",")
		}
		result.WriteString("\n")
	}

	for i, c := range tableConstraints {
		result.WriteString("    " + m.generateConstraintSQL(c, columnsByName(table)))
		if len(table.Columns)+i < totalItems-1 {
			result.WriteString(",")
		}
		result.WriteString("\n")
	}

	result.WriteString(");")
	return result.String()
}

// generateColumnSQL creates the SQL for a single column, applying type mapping.
// inlinePK reports whether this column should carry the PRIMARY KEY marker itself;
// when the table emits an explicit PK constraint it must not, or MySQL sees two.
func (m *MySQL) generateColumnSQL(column sqlmapper.Column, inlinePK bool) string {
	var parts []string
	parts = append(parts, column.Name)

	mysqlType := m.resolveType(column)
	parts = append(parts, mysqlType)

	if column.AutoIncrement {
		parts = append(parts, "AUTO_INCREMENT")
	}
	if inlinePK {
		parts = append(parts, "PRIMARY KEY")
	} else if !column.IsNullable {
		parts = append(parts, "NOT NULL")
	}

	if dv := m.defaultLiteral(column, mysqlType); dv != "" {
		parts = append(parts, "DEFAULT", dv)
	}

	if column.IsUnique && !inlinePK {
		parts = append(parts, "UNIQUE")
	}

	return strings.Join(parts, " ")
}

// defaultLiteral renders a column default as MySQL expects it, returning an empty
// string when there is nothing to emit. Booleans arriving from PostgreSQL are the
// notable case: MySQL stores them as TINYINT(1) and rejects DEFAULT 'true'.
func (m *MySQL) defaultLiteral(column sqlmapper.Column, mysqlType string) string {
	dv := strings.TrimSpace(column.DefaultValue)
	if dv == "" || strings.EqualFold(dv, "NULL") {
		return ""
	}

	if strings.HasPrefix(strings.ToUpper(mysqlType), "TINYINT(1)") {
		switch strings.ToLower(dv) {
		case "true", "t", "1":
			return "1"
		case "false", "f", "0":
			return "0"
		}
	}

	// TEXT/BLOB/JSON columns cannot carry a literal default in MySQL.
	upperType := strings.ToUpper(mysqlType)
	for _, t := range []string{"TEXT", "BLOB", "JSON", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB"} {
		if upperType == t {
			return ""
		}
	}

	if strings.EqualFold(dv, "CURRENT_TIMESTAMP") {
		return "CURRENT_TIMESTAMP"
	}
	if isNumeric(dv) {
		return dv
	}
	if strings.ContainsAny(dv, "()") {
		return expr.Value(dv, expr.MySQL)
	}
	return fmt.Sprintf("'%s'", strings.ReplaceAll(dv, "'", "''"))
}

// resolveType maps a column's DataType to the MySQL equivalent.
func (m *MySQL) resolveType(col sqlmapper.Column) string {
	lower := strings.ToLower(col.DataType)

	// MySQL has no array type; JSON is the closest lossless container.
	if col.IsArray {
		return "JSON"
	}

	// ENUM/SET get special treatment to preserve values
	if lower == "enum" || lower == "set" {
		if len(col.EnumValues) > 0 {
			quoted := make([]string, len(col.EnumValues))
			for i, v := range col.EnumValues {
				quoted[i] = fmt.Sprintf("'%s'", v)
			}
			return fmt.Sprintf("%s(%s)", strings.ToUpper(lower), strings.Join(quoted, ","))
		}
		if lower == "enum" {
			return "VARCHAR(255)"
		}
		return "TEXT"
	}

	// For serial types from PostgreSQL, output the base int type (AUTO_INCREMENT handles the rest)
	if lower == "serial" {
		return "INT"
	}
	if lower == "bigserial" {
		return "BIGINT"
	}
	if lower == "smallserial" {
		return "SMALLINT"
	}

	// UUID / network types from PG that embed length in type name (e.g. varchar(36))
	if strings.Contains(lower, "(") {
		return strings.ToUpper(lower)
	}

	if mapped, ok := toMySQLType[lower]; ok {
		t := strings.ToUpper(mapped)
		if col.Length > 0 && !strings.Contains(t, "(") &&
			t != "TEXT" && t != "BLOB" && t != "JSON" &&
			t != "TINYTEXT" && t != "MEDIUMTEXT" && t != "LONGTEXT" &&
			t != "TINYBLOB" && t != "MEDIUMBLOB" && t != "LONGBLOB" {
			if col.Scale > 0 {
				t = fmt.Sprintf("%s(%d,%d)", t, col.Length, col.Scale)
			} else {
				t = fmt.Sprintf("%s(%d)", t, col.Length)
			}
		}
		return t
	}

	// Fallback – return as-is with length if present
	if col.Length > 0 {
		if col.Scale > 0 {
			return fmt.Sprintf("%s(%d,%d)", strings.ToUpper(col.DataType), col.Length, col.Scale)
		}
		return fmt.Sprintf("%s(%d)", strings.ToUpper(col.DataType), col.Length)
	}
	return strings.ToUpper(col.DataType)
}

func (m *MySQL) generateConstraintSQL(c sqlmapper.Constraint, cols map[string]sqlmapper.Column) string {
	var sb strings.Builder
	if c.Name != "" {
		sb.WriteString(fmt.Sprintf("CONSTRAINT %s ", c.Name))
	}
	switch c.Type {
	case "PRIMARY KEY":
		sb.WriteString(fmt.Sprintf("PRIMARY KEY (%s)", m.mysqlKeyColumns(c.Columns, cols)))
	case "FOREIGN KEY":
		sb.WriteString(fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
			strings.Join(c.Columns, ", "), c.RefTable, strings.Join(c.RefColumns, ", ")))
		if c.DeleteRule != "" {
			sb.WriteString(" ON DELETE " + c.DeleteRule)
		}
		if c.UpdateRule != "" {
			sb.WriteString(" ON UPDATE " + c.UpdateRule)
		}
	case "UNIQUE":
		sb.WriteString(fmt.Sprintf("UNIQUE (%s)", m.mysqlKeyColumns(c.Columns, cols)))
	case "CHECK":
		sb.WriteString(fmt.Sprintf("CHECK (%s)", expr.Condition(c.CheckExpression, expr.MySQL)))
	}
	return sb.String()
}

func (m *MySQL) generateIndexSQL(tableName string, index sqlmapper.Index, cols map[string]sqlmapper.Column) string {
	var sb strings.Builder
	if index.IsUnique {
		sb.WriteString("CREATE UNIQUE INDEX ")
	} else {
		sb.WriteString("CREATE INDEX ")
	}
	sb.WriteString(fmt.Sprintf("%s ON %s(%s);", index.Name, tableName, m.mysqlKeyColumns(index.Columns, cols)))
	return sb.String()
}

// mysqlTextKeyPrefix is how much of an unbounded column a key covers.
//
// MySQL refuses to index a TEXT or BLOB column without one, and a source that
// has no length to give, such as SQLite, produces exactly that: "BLOB/TEXT
// column 'email' used in key specification without a key length". 255 is the
// conventional choice, and at four bytes per character it stays inside the
// 3072-byte limit an InnoDB index has.
const mysqlTextKeyPrefix = 255

// unboundedKeyTypes are the MySQL types that carry no length of their own.
var unboundedKeyTypes = map[string]bool{
	"tinytext": true, "text": true, "mediumtext": true, "longtext": true,
	"tinyblob": true, "blob": true, "mediumblob": true, "longblob": true,
}

// mysqlKeyColumns renders a key's column list, adding the prefix length MySQL
// requires on a column that has no length of its own.
func (m *MySQL) mysqlKeyColumns(columns []string, cols map[string]sqlmapper.Column) string {
	parts := make([]string, 0, len(columns))
	for _, name := range columns {
		col, ok := cols[name]
		if ok && col.Length == 0 && unboundedKeyTypes[mysqlBaseType(m.resolveType(col))] {
			parts = append(parts, fmt.Sprintf("%s(%d)", name, mysqlTextKeyPrefix))
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// mysqlBaseType strips any length off a rendered type, leaving the name.
func mysqlBaseType(rendered string) string {
	if i := strings.IndexByte(rendered, '('); i != -1 {
		rendered = rendered[:i]
	}
	return strings.ToLower(strings.TrimSpace(rendered))
}

// columnsByName indexes a table's columns so a key can look up what it covers.
func columnsByName(table sqlmapper.Table) map[string]sqlmapper.Column {
	out := make(map[string]sqlmapper.Column, len(table.Columns))
	for _, col := range table.Columns {
		out[col.Name] = col
	}
	return out
}

// splitAndTrim splits a comma-separated string and trims whitespace from each element.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// isNumeric reports whether s looks like a bare number (not needing quotes).
func isNumeric(s string) bool {
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

// atoi reads a base-10 integer out of a string that a pattern has already
// matched as digits. Nothing malformed can reach here, and zero is the right
// answer if anything ever does.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// routines returns the schema's functions, procedures and triggers as complete
// MySQL statements, in the order the server needs to see them.
func (m *MySQL) routines(schema *sqlmapper.Schema) []string {
	var out []string

	for _, fn := range schema.Functions {
		kw, tail := "FUNCTION", " RETURNS "+fn.Returns
		if fn.IsProc {
			kw, tail = "PROCEDURE", ""
		}
		out = append(out, fmt.Sprintf("CREATE %s %s(%s)%s\n%s",
			kw, fn.Name, routine.Params(fn.Parameters), tail, strings.TrimSpace(fn.Body)))
	}

	for _, pr := range schema.Procedures {
		out = append(out, fmt.Sprintf("CREATE PROCEDURE %s(%s)\n%s",
			pr.Name, routine.Params(pr.Parameters), strings.TrimSpace(pr.Body)))
	}

	for _, tr := range schema.Triggers {
		// MySQL has no statement-level triggers: FOR EACH ROW is required, and
		// leaving it out produced a statement the server rejects outright.
		header := sqlfmt.TriggerHeader(tr.Name, tr.Timing, tr.Event, tr.Table, true)
		// The parser captures what is between BEGIN and END, so they have to be
		// put back: without them the server runs only the first statement.
		out = append(out, sqlfmt.SourceRoutineSQL(header, sqlfmt.BlockBody(tr.Body)))
	}

	return out
}

// generateRoutinesSQL renders the routine section of a dump.
//
// Generate and GenerateStream both call it because they used to disagree: the
// file path dropped every routine, and the stream path wrote one MySQL could
// not load, having neither FOR EACH ROW nor the DELIMITER block that keeps the
// body's own semicolons from ending the statement.
func (m *MySQL) generateRoutinesSQL(schema *sqlmapper.Schema) string {
	if routine.Count(schema) == 0 {
		return ""
	}
	if !schema.RoutinesAreNativeTo(sqlmapper.MySQL) {
		return routine.ForeignSQL(schema)
	}

	// The bodies carry semicolons, so the terminator has to change around them,
	// exactly as mysqldump writes it.
	var sb strings.Builder
	sb.WriteString("DELIMITER ;;\n\n")
	for _, stmt := range m.routines(schema) {
		sb.WriteString(sqlfmt.Terminate(stmt, " ;;"))
		sb.WriteString("\n\n")
	}
	sb.WriteString("DELIMITER ;\n\n")

	return sb.String()
}

// splitKeyColumns splits a key's column list and drops the prefix length MySQL
// allows on one, "email(255)".
//
// The length says how much of the column the index covers, which is a MySQL
// index detail rather than part of the schema: carrying it into the column name
// wrote UNIQUE (email(255)) into targets that have no such syntax. The
// generator puts it back where it is needed.
func splitKeyColumns(list string) []string {
	parts := splitAndTrim(list)
	for i, p := range parts {
		if open := strings.IndexByte(p, '('); open > 0 && strings.HasSuffix(p, ")") {
			parts[i] = strings.TrimSpace(p[:open])
		}
	}
	return parts
}
