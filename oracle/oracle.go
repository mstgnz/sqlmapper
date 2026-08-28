// Package oracle provides functionality for parsing and generating Oracle database schemas.
// It implements the Parser interface for handling Oracle specific SQL syntax and schema structures.
package oracle

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/mstgnz/sqlmapper/internal/keyword"
	"github.com/mstgnz/sqlmapper/internal/sqlsplit"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/internal/expr"
)

// oracleTypeWithLenRe splits an embedded precision off a type, e.g.
// NUMBER(10,2) or VARCHAR2(255).
var oracleTypeWithLenRe = regexp.MustCompile(`^(\w+)\s*\(\s*(\d+)(?:\s*,\s*(\d+))?\s*\)$`)

// Real Oracle DDL comes from DBMS_METADATA.GET_DDL, which quotes every
// identifier, qualifies it with the schema, appends ENABLE to each constraint,
// and spells identity columns out in full. These patterns strip that back to
// something the shared schema model can hold.
var (
	oracleEnableRe     = regexp.MustCompile(`(?i)\s+(?:ENABLE|DISABLE)\b`)
	oracleUsingIndexRe = regexp.MustCompile(`(?i)\s+USING\s+INDEX\b`)
	oracleIdentityRe   = regexp.MustCompile(`(?i)\s*GENERATED\s+(?:ALWAYS|BY\s+DEFAULT(?:\s+ON\s+NULL)?)\s+AS\s+IDENTITY` +
		`(?:\s*\([^)]*\))?` +
		`(?:\s+(?:MINVALUE|MAXVALUE|INCREMENT\s+BY|START\s+WITH|CACHE|NOCACHE|ORDER|NOORDER|CYCLE|NOCYCLE|KEEP|NOKEEP|SCALE|NOSCALE|EXTEND|NOEXTEND|SESSION|GLOBAL)(?:\s+-?\d+)?)*`)
	oracleSpaceBeforeParenRe = regexp.MustCompile(`\s+\(`)
)

// oracleTypeStopWords marks the tokens that end a type expression and begin the
// column's attributes.
var oracleTypeStopWords = map[string]bool{
	"DEFAULT": true, "NOT": true, "NULL": true, "PRIMARY": true, "UNIQUE": true,
	"CHECK": true, "REFERENCES": true, "CONSTRAINT": true, "GENERATED": true,
	"COLLATE": true, "VISIBLE": true, "INVISIBLE": true, "ENABLE": true,
	"DISABLE": true, "AS": true, "VIRTUAL": true,
}

// unquoteOracleIdent strips the double quotes DBMS_METADATA wraps around every
// identifier and undoes Oracle's case folding.
//
// Oracle stores an unquoted identifier in upper case, so DBMS_METADATA hands
// back CUSTOMERS for a table the author wrote as customers. Carrying that upper
// case forward breaks on MySQL, where names are case sensitive and the view
// bodies copied out of the same dump still say customers. A name that contains
// any lower-case letter was quoted at creation time and is left alone.
func unquoteOracleIdent(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s != "" && s == strings.ToUpper(s) {
		return strings.ToLower(s)
	}
	return s
}

// splitOracleQualifiedName splits "APP"."CUSTOMERS" into its schema and name.
func splitOracleQualifiedName(raw string) (schema, name string) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	for i := range parts {
		parts[i] = unquoteOracleIdent(parts[i])
	}
	if len(parts) > 1 {
		return parts[0], parts[len(parts)-1]
	}
	return "", parts[0]
}

// takeOracleType returns the leading tokens of rest that make up the type
// expression. Counting tokens is not enough: DBMS_METADATA writes "TIMESTAMP (6)"
// with a space, and "INTERVAL DAY TO SECOND" spans four words.
func takeOracleType(rest string) string {
	var out []string
	depth := 0
	for _, tok := range strings.Fields(rest) {
		if depth == 0 && oracleTypeStopWords[strings.ToUpper(strings.Trim(tok, ","))] {
			break
		}
		out = append(out, tok)
		depth += strings.Count(tok, "(") - strings.Count(tok, ")")
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

// normalizeOracleTypeName folds Oracle's type names onto the shared vocabulary
// the other dialects use, so the existing type maps can pick them up.
func normalizeOracleTypeName(name string, scale int) string {
	switch strings.ToUpper(strings.Join(strings.Fields(name), " ")) {
	case "VARCHAR2", "NVARCHAR2", "VARCHAR":
		return "varchar"
	case "CHAR", "NCHAR":
		return "char"
	case "CLOB", "NCLOB", "LONG":
		return "text"
	case "BLOB", "RAW", "LONG RAW", "BFILE":
		return "blob"
	case "BINARY_FLOAT":
		return "real"
	case "BINARY_DOUBLE":
		return "double precision"
	case "DATE":
		// An Oracle DATE carries a time component, so it is a timestamp
		// everywhere else. Mapping it to a bare date would silently drop it.
		return "timestamp"
	case "TIMESTAMP":
		return "timestamp"
	case "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITH LOCAL TIME ZONE":
		return "timestamp with time zone"
	case "NUMBER", "NUMERIC", "DECIMAL":
		if scale > 0 {
			return "decimal"
		}
		return "numeric"
	case "FLOAT":
		return "double precision"
	case "XMLTYPE":
		return "text"
	}
	return strings.ToLower(name)
}

// applyOracleType fills DataType, Length and Scale from a raw type expression
// such as "VARCHAR2(255)", "NUMBER(10,2)" or "TIMESTAMP (6)".
func applyOracleType(col *sqlmapper.Column, typeExpr string) {
	typeExpr = oracleSpaceBeforeParenRe.ReplaceAllString(strings.TrimSpace(typeExpr), "(")

	if m := oracleTypeWithLenRe.FindStringSubmatch(typeExpr); len(m) > 2 {
		col.Length = atoi(m[2])
		if m[3] != "" {
			col.Scale = atoi(m[3])
		}
		col.DataType = normalizeOracleTypeName(m[1], col.Scale)

		// NUMBER(p) and NUMBER(p,0) are integers, not decimals; TIMESTAMP(6) is
		// a precision, not a length. Neither should carry a length onwards.
		switch col.DataType {
		case "numeric":
			if col.Scale == 0 {
				col.DataType = oracleIntegerWidth(col.Length)
				col.Length = 0
			}
		case "timestamp", "timestamp with time zone":
			col.Length = 0
		}
		return
	}

	col.DataType = normalizeOracleTypeName(typeExpr, 0)

	// An unqualified NUMBER is Oracle's idiomatic surrogate key. Mapping it to
	// numeric is defensible on paper but makes every foreign key onto an identity
	// column fail to build, so it becomes an integer here. A column that
	// genuinely needs scale declares it, and DBMS_METADATA always writes it.
	if col.DataType == "numeric" {
		col.DataType = oracleIntegerWidth(0)
	}
}

// oracleIntegerWidth picks the narrowest integer type that holds a NUMBER of the
// given precision. Oracle NUMBER without a precision is unbounded, so it maps to
// the widest integer available rather than guessing small.
func oracleIntegerWidth(precision int) string {
	switch {
	case precision == 0:
		return "bigint"
	case precision <= 4:
		return "smallint"
	case precision <= 9:
		return "int"
	default:
		return "bigint"
	}
}

// normalizeOracleDDL removes the noise DBMS_METADATA adds around real DDL:
// trailing ENABLE/DISABLE flags and USING INDEX clauses carry no information the
// schema model keeps, and both break naive attribute matching.
func normalizeOracleDDL(stmt string) string {
	stmt = oracleUsingIndexRe.ReplaceAllString(stmt, "")
	stmt = oracleEnableRe.ReplaceAllString(stmt, "")
	return stmt
}

// Oracle represents an Oracle parser implementation that handles parsing and generating
// Oracle database schemas. It maintains an internal schema representation and provides
// methods for converting between Oracle SQL and the common schema format.
type Oracle struct {
	schema *sqlmapper.Schema
}

// NewOracle creates and initializes a new Oracle parser instance.
// It returns a parser that can handle Oracle specific SQL syntax and schema structures.
func NewOracle() sqlmapper.Database {
	return &Oracle{
		schema: &sqlmapper.Schema{},
	}
}

// Parse takes an Oracle SQL dump content and parses it into a common schema structure.
// It processes various Oracle objects including:
// - Tables with columns and constraints
// - Sequences
// - Views
// - Triggers
// - User privileges
//
// Parameters:
//   - content: The Oracle SQL dump content to parse
//
// Returns:
//   - *sqlmapper.Schema: The parsed schema structure
//   - error: An error if parsing fails or if the content is empty
func (o *Oracle) Parse(content string) (*sqlmapper.Schema, error) {
	if content == "" {
		return nil, errors.New("empty content")
	}

	// Splitting on every semicolon truncated a PL/SQL body at its first inner
	// statement: the trigger was registered but its body arrived empty. The
	// splitter knows a semicolon inside a body is not a terminator and that a
	// slash on its own line is.
	//
	// Statements are then collapsed onto one line, because the patterns below
	// were written against the line-joined form this replaced.
	var statements []string
	splitter := sqlsplit.New(strings.NewReader(content), "/")
	for {
		raw, err := splitter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading statement: %v", err)
		}
		if joined := strings.Join(strings.Fields(raw), " "); joined != "" {
			statements = append(statements, joined)
		}
	}

	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}

		// CREATE TABLE
		if keyword.HasPrefix(stmt, "CREATE TABLE") {
			table, err := o.parseCreateTable(stmt)
			if err != nil {
				return nil, err
			}
			o.schema.Tables = append(o.schema.Tables, table)
		}

		// CREATE INDEX. Without this branch a standalone CREATE INDEX in the
		// dump was read and then silently discarded.
		upperStmt := strings.ToUpper(stmt)
		if strings.HasPrefix(upperStmt, "CREATE INDEX") ||
			strings.HasPrefix(upperStmt, "CREATE UNIQUE INDEX") ||
			strings.HasPrefix(upperStmt, "CREATE BITMAP INDEX") {
			if err := o.parseIndexes(stmt); err != nil {
				return nil, err
			}
		}

		// CREATE SEQUENCE
		if strings.HasPrefix(strings.ToUpper(stmt), "CREATE SEQUENCE") {
			seq, err := o.parseCreateSequence(stmt)
			if err != nil {
				return nil, err
			}
			o.schema.Sequences = append(o.schema.Sequences, seq)
		}

		// CREATE VIEW
		if strings.HasPrefix(strings.ToUpper(stmt), "CREATE") && strings.Contains(strings.ToUpper(stmt), "VIEW") {
			view, err := o.parseCreateView(stmt)
			if err != nil {
				return nil, err
			}
			o.schema.Views = append(o.schema.Views, view)
		}

		// CREATE TRIGGER
		if strings.HasPrefix(strings.ToUpper(stmt), "CREATE") && strings.Contains(strings.ToUpper(stmt), "TRIGGER") {
			trigger, err := o.parseCreateTrigger(stmt)
			if err != nil {
				return nil, err
			}
			o.schema.Triggers = append(o.schema.Triggers, trigger)
		}
	}

	return o.schema, nil
}

// parseCreateTable processes a CREATE TABLE statement and extracts table structure.
// It handles various table components including:
// - Table name and schema
// - Column definitions with data types and constraints
// - Table-level constraints (PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK)
// - Column-level constraints and properties
//
// Parameters:
//   - stmt: The CREATE TABLE statement to parse
//
// Returns:
//   - sqlmapper.Table: The parsed table structure
//   - error: An error if parsing fails
func (o *Oracle) parseCreateTable(stmt string) (sqlmapper.Table, error) {
	table := sqlmapper.Table{}

	// Read the table name, which may be schema-qualified. Matching only \w
	// truncated "app.users" to "app", so every schema-qualified table in a dump
	// came out under the wrong name and nothing could be attached to it.
	tableNameRegex := regexp.MustCompile(`(?i)CREATE\s+(?:GLOBAL\s+TEMPORARY\s+)?TABLE\s+([."\w]+)`)
	matches := tableNameRegex.FindStringSubmatch(stmt)
	if len(matches) > 1 {
		table.Schema, table.Name = splitOracleQualifiedName(matches[1])
	}

	// Strip the ENABLE / USING INDEX noise DBMS_METADATA appends before looking
	// at anything inside the table body.
	stmt = normalizeOracleDDL(stmt)

	// Parse the columns. A statement that reached here without a column list is
	// not a CREATE TABLE at all, or is truncated; either way slicing on the
	// missing parenthesis would panic.
	open, close := strings.Index(stmt, "("), strings.LastIndex(stmt, ")")
	if open == -1 || close < open {
		return table, fmt.Errorf("no columns found in CREATE TABLE statement")
	}
	columnsStr := stmt[open+1 : close]
	// Splitting on every comma would cut a definition in half at the comma inside
	// NUMBER(10,2) or inside a CHECK list, so the split tracks parentheses and
	// string literals.
	columnDefs := splitTopLevelCommas(columnsStr)

	for _, colDef := range columnDefs {
		colDef = strings.TrimSpace(colDef)
		if strings.HasPrefix(colDef, "CONSTRAINT") {
			constraint := sqlmapper.Constraint{}

			// Read the constraint name
			nameRegex := regexp.MustCompile(`(?i)CONSTRAINT\s+("?[\w]+"?)`)
			matches := nameRegex.FindStringSubmatch(colDef)
			if len(matches) > 1 {
				constraint.Name = unquoteOracleIdent(matches[1])
			}

			if strings.Contains(colDef, "PRIMARY KEY") {
				constraint.Type = "PRIMARY KEY"
				// Read the columns
				colsRegex := regexp.MustCompile(`PRIMARY\s+KEY\s*\(([^)]+)\)`)
				matches = colsRegex.FindStringSubmatch(colDef)
				if len(matches) > 1 {
					cols := strings.Split(matches[1], ",")
					for i, col := range cols {
						cols[i] = unquoteOracleIdent(col)
					}
					constraint.Columns = cols
				}
			} else if strings.Contains(colDef, "FOREIGN KEY") {
				constraint.Type = "FOREIGN KEY"
				// Read the foreign key columns
				fkRegex := regexp.MustCompile(`FOREIGN\s+KEY\s*\(([^)]+)\)`)
				matches = fkRegex.FindStringSubmatch(colDef)
				if len(matches) > 1 {
					cols := strings.Split(matches[1], ",")
					for i, col := range cols {
						cols[i] = unquoteOracleIdent(col)
					}
					constraint.Columns = cols
				}
				// Read the referenced table and columns
				refRegex := regexp.MustCompile(`(?i)REFERENCES\s+([."\w]+)\s*\(([^)]+)\)`)
				matches = refRegex.FindStringSubmatch(colDef)
				if len(matches) > 2 {
					_, constraint.RefTable = splitOracleQualifiedName(matches[1])
					refCols := strings.Split(matches[2], ",")
					for i, col := range refCols {
						refCols[i] = unquoteOracleIdent(col)
					}
					constraint.RefColumns = refCols
				}
				// Read the ON DELETE rule
				if strings.Contains(colDef, "ON DELETE") {
					if strings.Contains(colDef, "CASCADE") {
						constraint.DeleteRule = "CASCADE"
					}
				}
			} else if strings.Contains(colDef, "UNIQUE") {
				constraint.Type = "UNIQUE"
				// Read the columns
				colsRegex := regexp.MustCompile(`UNIQUE\s*\(([^)]+)\)`)
				matches = colsRegex.FindStringSubmatch(colDef)
				if len(matches) > 1 {
					cols := strings.Split(matches[1], ",")
					for i, col := range cols {
						cols[i] = unquoteOracleIdent(col)
					}
					constraint.Columns = cols
				}
			} else if strings.Contains(colDef, "CHECK") {
				constraint.Type = "CHECK"
				// Check ifadesini al
				checkRegex := regexp.MustCompile(`CHECK\s*\(([^)]+)\)`)
				matches = checkRegex.FindStringSubmatch(colDef)
				if len(matches) > 1 {
					constraint.CheckExpression = strings.TrimSpace(matches[1])
				}
			}
			table.Constraints = append(table.Constraints, constraint)
			continue
		}

		parts := strings.Fields(colDef)
		if len(parts) < 2 {
			continue
		}

		col := sqlmapper.Column{
			Name: unquoteOracleIdent(parts[0]),
			// Oracle columns are nullable unless the definition says otherwise.
			// Leaving this at the zero value marked every column NOT NULL.
			IsNullable: true,
		}

		// An identity column carries a long option list that would otherwise be
		// read as part of the type and the default.
		if oracleIdentityRe.MatchString(colDef) {
			col.AutoIncrement = true
			colDef = oracleIdentityRe.ReplaceAllString(colDef, "")
		}

		rest := strings.TrimSpace(colDef[len(parts[0]):])
		applyOracleType(&col, takeOracleType(rest))

		if strings.Contains(strings.ToUpper(colDef), "NOT NULL") {
			col.IsNullable = false
		}

		if strings.Contains(strings.ToUpper(colDef), "DEFAULT") {
			defaultIdx := strings.Index(strings.ToUpper(colDef), "DEFAULT")
			// TrimSpace before looking for the terminator: the character right
			// after DEFAULT is a space, so searching the untrimmed remainder
			// found index 0 and every default came out empty.
			restStr := strings.TrimSpace(colDef[defaultIdx+len("DEFAULT"):])
			defaultEnd := strings.Index(restStr, " ")
			if defaultEnd == -1 {
				defaultEnd = len(restStr)
			}
			col.DefaultValue = strings.TrimSpace(restStr[:defaultEnd])

			// SYSDATE and SYSTIMESTAMP are Oracle's now(); quoting them as a
			// literal produced DEFAULT 'SYSTIMESTAMP', which no other dialect
			// accepts on a timestamp column.
			switch strings.ToUpper(col.DefaultValue) {
			case "SYSDATE", "SYSTIMESTAMP", "CURRENT_TIMESTAMP", "LOCALTIMESTAMP", "CURRENT_DATE":
				col.DefaultValue = "CURRENT_TIMESTAMP"
			}
		}

		if strings.Contains(colDef, "PRIMARY KEY") {
			col.IsPrimaryKey = true
			constraint := sqlmapper.Constraint{
				Type:    "PRIMARY KEY",
				Columns: []string{col.Name},
			}
			table.Constraints = append(table.Constraints, constraint)
		}

		if strings.Contains(colDef, "UNIQUE") {
			col.IsUnique = true
			constraint := sqlmapper.Constraint{
				Type:    "UNIQUE",
				Columns: []string{col.Name},
			}
			table.Constraints = append(table.Constraints, constraint)
		}

		if strings.Contains(colDef, "CHECK") {
			checkRegex := regexp.MustCompile(`CHECK\s*\(([^)]+)\)`)
			matches := checkRegex.FindStringSubmatch(colDef)
			if len(matches) > 1 {
				constraint := sqlmapper.Constraint{
					Type:            "CHECK",
					CheckExpression: strings.TrimSpace(matches[1]),
				}
				table.Constraints = append(table.Constraints, constraint)
			}
		}

		table.Columns = append(table.Columns, col)
	}

	return table, nil
}

// parseCreateSequence processes a CREATE SEQUENCE statement.
// It extracts sequence properties including:
// - Sequence name and schema
// - Start value and increment
// - Min and max values
// - Cycle option
//
// Parameters:
//   - stmt: The CREATE SEQUENCE statement to parse
//
// Returns:
//   - sqlmapper.Sequence: The parsed sequence structure
//   - error: An error if parsing fails
func (o *Oracle) parseCreateSequence(stmt string) (sqlmapper.Sequence, error) {
	seq := sqlmapper.Sequence{StartValue: 1, IncrementBy: 1}

	// Read the sequence name, which may be schema-qualified.
	seqNameRegex := regexp.MustCompile(`(?i)CREATE\s+SEQUENCE\s+([.\w]+)`)
	matches := seqNameRegex.FindStringSubmatch(stmt)
	if len(matches) > 1 {
		seq.Name = matches[1]
		if parts := strings.Split(seq.Name, "."); len(parts) > 1 {
			seq.Schema = parts[0]
			seq.Name = parts[1]
		}
	}

	// Read the options. Each is matched on its own because Oracle does not fix
	// their order, and the captured value is what gets stored: an earlier
	// version discarded it and wrote a hardcoded 1, so every sequence came out
	// as START WITH 1 INCREMENT BY 1 no matter what the source said.
	readInt := func(pattern string, target *int) {
		m := regexp.MustCompile(pattern).FindStringSubmatch(stmt)
		if len(m) > 1 {
			*target = atoi(m[1])
		}
	}

	readInt(`(?i)START\s+WITH\s+(-?\d+)`, &seq.StartValue)
	readInt(`(?i)INCREMENT\s+BY\s+(-?\d+)`, &seq.IncrementBy)
	readInt(`(?i)(?:^|\s)MINVALUE\s+(-?\d+)`, &seq.MinValue)
	readInt(`(?i)(?:^|\s)MAXVALUE\s+(-?\d+)`, &seq.MaxValue)
	readInt(`(?i)CACHE\s+(-?\d+)`, &seq.Cache)

	upper := strings.ToUpper(stmt)
	seq.Cycle = strings.Contains(upper, "CYCLE") && !strings.Contains(upper, "NOCYCLE") &&
		!strings.Contains(upper, "NO CYCLE")

	return seq, nil
}

// parseCreateView processes a CREATE VIEW statement.
// It extracts view properties including:
// - View name and schema
// - View definition (SELECT statement)
// - View options (FORCE, WITH CHECK OPTION)
//
// Parameters:
//   - stmt: The CREATE VIEW statement to parse
//
// Returns:
//   - sqlmapper.View: The parsed view structure
//   - error: An error if parsing fails
func (o *Oracle) parseCreateView(stmt string) (sqlmapper.View, error) {
	view := sqlmapper.View{}

	// Read the view name. It may be schema-qualified, and the statement may
	// declare a materialized view, in which case the name sat behind a keyword
	// the pattern did not allow for and came out empty.
	// DBMS_METADATA writes "CREATE OR REPLACE FORCE EDITIONABLE VIEW
	// "APP"."V" ("ID", "EMAIL") AS", so the optional keywords, the quoting and
	// the column list all have to be allowed for.
	viewNameRegex := regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:FORCE\s+|NOFORCE\s+)?(?:EDITIONABLE\s+|NONEDITIONABLE\s+)?(MATERIALIZED\s+)?VIEW\s+([."\w]+)`)
	matches := viewNameRegex.FindStringSubmatch(stmt)
	if len(matches) > 2 {
		view.IsMaterialized = strings.TrimSpace(matches[1]) != ""
		view.Schema, view.Name = splitOracleQualifiedName(matches[2])
	}

	// Read the view definition
	asIndex := strings.Index(strings.ToUpper(stmt), " AS ")
	if asIndex != -1 {
		view.Definition = strings.TrimSpace(stmt[asIndex+4:])
	}

	return view, nil
}

// parseCreateTrigger processes a CREATE TRIGGER statement.
// It extracts trigger properties including:
// - Trigger name and schema
// - Triggering event (INSERT, UPDATE, DELETE)
// - Trigger timing (BEFORE, AFTER, INSTEAD OF)
// - Table name
// - Trigger body
//
// Parameters:
//   - stmt: The CREATE TRIGGER statement to parse
//
// Returns:
//   - sqlmapper.Trigger: The parsed trigger structure
//   - error: An error if parsing fails
func (o *Oracle) parseCreateTrigger(stmt string) (sqlmapper.Trigger, error) {
	trigger := sqlmapper.Trigger{}

	// Read the trigger name, which may be schema-qualified. Matching only \w
	// truncated "app.users_bi" to "app".
	triggerNameRegex := regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?TRIGGER\s+([.\w]+)`)
	matches := triggerNameRegex.FindStringSubmatch(stmt)
	if len(matches) > 1 {
		trigger.Name = matches[1]
		if parts := strings.Split(trigger.Name, "."); len(parts) > 1 {
			trigger.Schema = parts[0]
			trigger.Name = parts[1]
		}
	}

	// Read the trigger timing
	if strings.Contains(strings.ToUpper(stmt), "BEFORE") {
		trigger.Timing = "BEFORE"
	} else if strings.Contains(strings.ToUpper(stmt), "AFTER") {
		trigger.Timing = "AFTER"
	}

	// Read the trigger event
	if strings.Contains(strings.ToUpper(stmt), "INSERT") {
		trigger.Event = "INSERT"
	} else if strings.Contains(strings.ToUpper(stmt), "UPDATE") {
		trigger.Event = "UPDATE"
	} else if strings.Contains(strings.ToUpper(stmt), "DELETE") {
		trigger.Event = "DELETE"
	}

	// Read the table name, dropping any schema qualifier.
	tableRegex := regexp.MustCompile(`(?i)\sON\s+([.\w]+)`)
	matches = tableRegex.FindStringSubmatch(stmt)
	if len(matches) > 1 {
		trigger.Table = matches[1]
		if parts := strings.Split(trigger.Table, "."); len(parts) > 1 {
			trigger.Table = parts[1]
		}
	}

	// Check for FOR EACH ROW
	trigger.ForEachRow = strings.Contains(strings.ToUpper(stmt), "FOR EACH ROW")

	// Read the trigger body
	beginIndex := strings.Index(strings.ToUpper(stmt), "BEGIN")
	endIndex := strings.LastIndex(strings.ToUpper(stmt), "END")
	if beginIndex != -1 && endIndex != -1 {
		trigger.Body = strings.TrimSpace(stmt[beginIndex : endIndex+3])
	}

	return trigger, nil
}

// Generate creates an Oracle SQL dump from a schema structure.
// It generates SQL statements for all database objects in the schema, including:
// - Tables with columns, constraints, and indexes
// - Sequences
// - Views
// - Triggers
// - User privileges
//
// Parameters:
//   - schema: The schema structure to convert to Oracle SQL
//
// Returns:
//   - string: The generated Oracle SQL statements
//   - error: An error if generation fails or if the schema is nil
func (o *Oracle) Generate(schema *sqlmapper.Schema) (string, error) {
	if schema == nil {
		return "", errors.New("empty schema")
	}

	var result strings.Builder

	// Create sequences
	for _, seq := range schema.Sequences {
		fmt.Fprintf(&result, "CREATE SEQUENCE %s START WITH %d INCREMENT BY %d;\n\n",
			seq.Name, seq.StartValue, seq.IncrementBy)
	}

	// Dump tools do not order tables by dependency, so a child table can precede
	// its parent and the foreign key would fail to resolve.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)

	// Create tables
	for _, table := range tables {
		result.WriteString(o.generateTableSQL(table, deferredFKs[table.Name]))

		// Build the indexes
		for _, index := range table.Indexes {
			result.WriteString(o.generateIndexSQL(table.Name, index))
			result.WriteString(";\n")
		}

		result.WriteString("\n")
	}

	// Foreign keys that close a cycle cannot be satisfied by ordering, so they
	// are added once every table exists.
	for _, table := range tables {
		for _, c := range deferredFKs[table.Name] {
			fmt.Fprintf(&result, "ALTER TABLE %s ADD %s;\n\n", table.Name, o.generateConstraintSQL(c))
		}
	}

	// Create views
	for _, view := range schema.Views {
		fmt.Fprintf(&result, "CREATE OR REPLACE VIEW %s AS\n%s;\n\n",
			view.Name, expr.TranslateViewBody(strings.TrimSuffix(strings.TrimSpace(view.Definition), ";"), expr.Oracle))
	}

	// Create triggers
	for _, trigger := range schema.Triggers {
		result.WriteString(fmt.Sprintf("CREATE OR REPLACE TRIGGER %s\n", trigger.Name))
		if trigger.Timing != "" {
			result.WriteString(trigger.Timing + " ")
		}
		if trigger.Event != "" {
			result.WriteString(trigger.Event + " ")
		}
		if trigger.Table != "" {
			result.WriteString("ON " + trigger.Table + " ")
		}
		if trigger.ForEachRow {
			result.WriteString("FOR EACH ROW\n")
		}
		if trigger.Body != "" {
			result.WriteString(trigger.Body)
		}
		result.WriteString("\n/\n\n")
	}

	return result.String(), nil
}

func (o *Oracle) parseTables(statement string) error {
	re := regexp.MustCompile(`CREATE\s+TABLE\s+([.\w]+)\s*\((.*?)\)(?:\s+TABLESPACE\s+(\w+))?`)
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

		// Parse tablespace if exists
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

		o.schema.Tables = append(o.schema.Tables, table)
	}

	return nil
}

func (o *Oracle) parseViews(statement string) error {
	re := regexp.MustCompile(`CREATE(?:\s+OR\s+REPLACE)?\s+VIEW\s+([.\w]+)\s+AS\s+(.*?)(?:WITH\s+READ\s+ONLY)?$`)
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

		o.schema.Views = append(o.schema.Views, view)
	}

	return nil
}

func (o *Oracle) parseFunctions(statement string) error {
	re := regexp.MustCompile(`CREATE(?:\s+OR\s+REPLACE)?\s+(FUNCTION|PROCEDURE)\s+([.\w]+)\s*\((.*?)\)(?:\s+RETURN\s+(\w+))?\s+IS|AS\s+(.*?)(?:END\s+\w+)?$`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 4 {
		isProc := matches[1] == "PROCEDURE"
		functionName := matches[2]
		function := sqlmapper.Function{
			IsProc: isProc,
			Body:   matches[5],
		}

		if !isProc {
			function.Returns = matches[4]
		}

		// Parse schema if exists
		parts := strings.Split(functionName, ".")
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
						Name:     parts[0],
						DataType: parts[1],
					}
					function.Parameters = append(function.Parameters, parameter)
				}
			}
		}

		o.schema.Functions = append(o.schema.Functions, function)
	}

	return nil
}

func (o *Oracle) parseTriggers(statement string) error {
	re := regexp.MustCompile(`CREATE(?:\s+OR\s+REPLACE)?\s+TRIGGER\s+([.\w]+)\s+(BEFORE|AFTER|INSTEAD\s+OF)\s+(INSERT|UPDATE|DELETE)\s+ON\s+([.\w]+)(?:\s+FOR\s+EACH\s+ROW)?\s+(.*?)(?:END\s+\w+)?$`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 5 {
		triggerName := matches[1]
		trigger := sqlmapper.Trigger{
			Timing:     matches[2],
			Event:      matches[3],
			Table:      matches[4],
			Body:       matches[5],
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

		o.schema.Triggers = append(o.schema.Triggers, trigger)
	}

	return nil
}

func (o *Oracle) parseSequences(statement string) error {
	re := regexp.MustCompile(`CREATE\s+SEQUENCE\s+([.\w]+)(?:\s+START\s+WITH\s+(\d+))?(?:\s+INCREMENT\s+BY\s+(\d+))?(?:\s+MINVALUE\s+(\d+))?(?:\s+MAXVALUE\s+(\d+))?(?:\s+CACHE\s+(\d+))?(?:\s+CYCLE)?`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 1 {
		seqName := matches[1]
		seq := sqlmapper.Sequence{
			Name: seqName,
		}

		// Parse schema if exists
		parts := strings.Split(seqName, ".")
		if len(parts) > 1 {
			seq.Schema = parts[0]
			seq.Name = parts[1]
		}

		// Parse optional parameters
		if len(matches) > 2 && matches[2] != "" {
			seq.StartValue = atoi(matches[2])
		}
		if len(matches) > 3 && matches[3] != "" {
			seq.IncrementBy = atoi(matches[3])
		}
		if len(matches) > 4 && matches[4] != "" {
			seq.MinValue = atoi(matches[4])
		}
		if len(matches) > 5 && matches[5] != "" {
			seq.MaxValue = atoi(matches[5])
		}
		if len(matches) > 6 && matches[6] != "" {
			seq.Cache = atoi(matches[6])
		}
		seq.Cycle = strings.Contains(statement, "CYCLE")

		o.schema.Sequences = append(o.schema.Sequences, seq)
	}

	return nil
}

func (o *Oracle) parseTypes(statement string) error {
	re := regexp.MustCompile(`CREATE(?:\s+OR\s+REPLACE)?\s+TYPE\s+([.\w]+)\s+(?:AS\s+|IS\s+|UNDER\s+)?(.+?)(?:NOT\s+FINAL)?$`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 2 {
		typeName := matches[1]
		typ := sqlmapper.Type{
			Definition: matches[2],
		}

		// Parse schema if exists
		parts := strings.Split(typeName, ".")
		if len(parts) > 1 {
			typ.Schema = parts[0]
			typ.Name = parts[1]
		} else {
			typ.Name = typeName
		}

		o.schema.Types = append(o.schema.Types, typ)
	}

	return nil
}

func (o *Oracle) parseIndexes(statement string) error {
	re := regexp.MustCompile(`(?i)CREATE(?:\s+UNIQUE|\s+BITMAP)?\s+INDEX\s+([."\w]+)\s+ON\s+([."\w]+)\s*\((.*?)\)(?:\s+TABLESPACE\s+(\w+))?`)
	matches := re.FindStringSubmatch(statement)

	if len(matches) > 3 {
		_, indexName := splitOracleQualifiedName(matches[1])
		_, tableName := splitOracleQualifiedName(matches[2])
		columns := strings.Split(matches[3], ",")

		// Find the table
		for i, table := range o.schema.Tables {
			if table.Name == tableName || fmt.Sprintf("%s.%s", table.Schema, table.Name) == tableName {
				index := sqlmapper.Index{
					Name:     indexName,
					Columns:  make([]string, len(columns)),
					IsUnique: strings.Contains(statement, "UNIQUE"),
					IsBitmap: strings.Contains(statement, "BITMAP"),
				}

				// Clean column names
				for j, col := range columns {
					index.Columns[j] = unquoteOracleIdent(col)
				}

				// Parse tablespace if exists
				if len(matches) > 4 && matches[4] != "" {
					index.TableSpace = matches[4]
				}

				o.schema.Tables[i].Indexes = append(o.schema.Tables[i].Indexes, index)
				break
			}
		}
	}

	return nil
}

// generateSequenceSQL generates SQL for a sequence
func (o *Oracle) generateSequenceSQL(sequence sqlmapper.Sequence) string {
	sql := fmt.Sprintf("CREATE SEQUENCE %s\n", sequence.Name)
	if sequence.StartValue > 0 {
		sql += fmt.Sprintf("START WITH %d\n", sequence.StartValue)
	}
	if sequence.IncrementBy > 0 {
		sql += fmt.Sprintf("INCREMENT BY %d\n", sequence.IncrementBy)
	}
	if sequence.MinValue > 0 {
		sql += fmt.Sprintf("MINVALUE %d\n", sequence.MinValue)
	}
	if sequence.MaxValue > 0 {
		sql += fmt.Sprintf("MAXVALUE %d\n", sequence.MaxValue)
	}
	if sequence.Cache > 0 {
		sql += fmt.Sprintf("CACHE %d\n", sequence.Cache)
	}
	if sequence.Cycle {
		sql += "CYCLE\n"
	}
	return sql
}

// generateTypeSQL generates SQL for a type
func (o *Oracle) generateTypeSQL(typ sqlmapper.Type) string {
	return fmt.Sprintf("CREATE TYPE %s AS %s", typ.Name, typ.Definition)
}

// generateTableSQL generates SQL for a table
// toOracleType maps the shared type vocabulary onto Oracle's own types. Oracle
// has no boolean, no native JSON column and no TEXT, so those fold onto the
// nearest thing it does have.
var toOracleType = map[string]string{
	"varchar": "VARCHAR2", "character varying": "VARCHAR2", "char": "CHAR",
	"text": "CLOB", "tinytext": "CLOB", "mediumtext": "CLOB", "longtext": "CLOB",
	"smallint": "NUMBER(5)", "int": "NUMBER(10)", "integer": "NUMBER(10)",
	"mediumint": "NUMBER(8)", "bigint": "NUMBER(19)", "tinyint": "NUMBER(3)",
	"decimal": "NUMBER", "numeric": "NUMBER",
	"real": "BINARY_FLOAT", "float": "BINARY_FLOAT", "double": "BINARY_DOUBLE",
	"double precision": "BINARY_DOUBLE",
	"boolean":          "NUMBER(1)", "bool": "NUMBER(1)", "bit": "NUMBER(1)",
	"date": "DATE", "time": "TIMESTAMP", "timestamp": "TIMESTAMP",
	"datetime": "TIMESTAMP", "timestamptz": "TIMESTAMP WITH TIME ZONE",
	"timestamp with time zone": "TIMESTAMP WITH TIME ZONE",
	"json":                     "CLOB", "jsonb": "CLOB", "xml": "XMLTYPE",
	"blob": "BLOB", "bytea": "BLOB", "tinyblob": "BLOB", "mediumblob": "BLOB",
	"longblob": "BLOB", "binary": "BLOB", "varbinary": "BLOB",
	"uuid": "VARCHAR2(36)", "inet": "VARCHAR2(45)", "cidr": "VARCHAR2(45)",
	"macaddr": "VARCHAR2(17)", "interval": "INTERVAL DAY TO SECOND",
	"enum": "VARCHAR2(255)", "set": "VARCHAR2(255)",
	"serial": "NUMBER(10)", "bigserial": "NUMBER(19)", "smallserial": "NUMBER(5)",
}

// oracleNoLengthTypes never take a length, either because Oracle forbids it or
// because the mapping already carries one.
var oracleNoLengthTypes = map[string]bool{
	"CLOB": true, "BLOB": true, "DATE": true, "TIMESTAMP": true,
	"TIMESTAMP WITH TIME ZONE": true, "XMLTYPE": true, "BINARY_FLOAT": true,
	"BINARY_DOUBLE": true, "INTERVAL DAY TO SECOND": true,
}

// resolveType maps a column onto the Oracle type it should be declared as.
func (o *Oracle) resolveType(col sqlmapper.Column) string {
	lower := strings.ToLower(strings.TrimSpace(col.DataType))

	// Oracle has no array type; a serialised value in a CLOB is the closest it
	// gets without inventing a nested table.
	if col.IsArray {
		return "CLOB"
	}

	mapped, ok := toOracleType[lower]
	if !ok {
		// Already an Oracle type, or something we do not know: pass it through.
		mapped = strings.ToUpper(col.DataType)
	}

	if col.Length > 0 && !oracleNoLengthTypes[mapped] && !strings.Contains(mapped, "(") {
		if col.Scale > 0 {
			return fmt.Sprintf("%s(%d,%d)", mapped, col.Length, col.Scale)
		}
		return fmt.Sprintf("%s(%d)", mapped, col.Length)
	}
	return mapped
}

// generateColumnSQL renders one column. inlinePK reports whether this column
// carries the PRIMARY KEY marker itself.
func (o *Oracle) generateColumnSQL(col sqlmapper.Column, inlinePK bool) string {
	oracleType := o.resolveType(col)
	sql := col.Name + " " + oracleType

	if col.DefaultValue != "" && !col.AutoIncrement {
		sql += " DEFAULT " + o.defaultLiteral(col, oracleType)
	}

	// Oracle wants the identity clause after the type and before the
	// constraints; it also implies NOT NULL.
	if col.AutoIncrement {
		sql += " GENERATED BY DEFAULT AS IDENTITY"
	}

	if inlinePK {
		sql += " PRIMARY KEY"
	} else if !col.IsNullable && !col.AutoIncrement {
		sql += " NOT NULL"
	}
	if col.IsUnique && !inlinePK {
		sql += " UNIQUE"
	}

	return sql
}

// defaultLiteral renders a column default the way Oracle expects it.
func (o *Oracle) defaultLiteral(col sqlmapper.Column, oracleType string) string {
	dv := strings.TrimSpace(col.DefaultValue)

	// Oracle has no boolean, so a boolean default has to become the number the
	// column is now declared as.
	if strings.HasPrefix(oracleType, "NUMBER(1)") {
		switch strings.ToLower(strings.Trim(dv, "'")) {
		case "true", "t", "yes", "1":
			return "1"
		case "false", "f", "no", "0":
			return "0"
		}
	}

	if strings.EqualFold(dv, "CURRENT_TIMESTAMP") {
		return "SYSTIMESTAMP"
	}
	if isNumericLiteral(dv) {
		return dv
	}
	if strings.ContainsAny(dv, "()") {
		return expr.Value(dv, expr.Oracle)
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
func (o *Oracle) generateConstraintSQL(c sqlmapper.Constraint) string {
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
		// Oracle supports ON DELETE CASCADE and SET NULL only; it has no
		// ON UPDATE action at all, so those are dropped rather than emitted
		// as syntax it would reject.
		switch strings.ToUpper(c.DeleteRule) {
		case "CASCADE":
			sb.WriteString(" ON DELETE CASCADE")
		case "SET NULL":
			sb.WriteString(" ON DELETE SET NULL")
		}
	case "CHECK":
		sb.WriteString(fmt.Sprintf("CHECK (%s)", expr.Condition(c.CheckExpression, expr.Oracle)))
	}
	return sb.String()
}

// generateTableSQL creates a CREATE TABLE statement with its columns and
// constraints. deferred lists the foreign keys that must be added afterwards.
func (o *Oracle) generateTableSQL(table sqlmapper.Table, deferred []sqlmapper.Constraint) string {
	var result strings.Builder
	result.WriteString("CREATE TABLE " + table.Name + " (\n")

	// A single-column PK on an identity column reads better inline; every other
	// PK is emitted as a table-level constraint. Exactly one of the two must
	// fire, or Oracle rejects the table with two primary keys.
	inlinePKCols := map[string]bool{}
	var tableConstraints []sqlmapper.Constraint
	for _, c := range table.Constraints {
		switch c.Type {
		case "PRIMARY KEY":
			if len(c.Columns) == 1 && columnIsAutoIncrement(table, c.Columns[0]) && c.Name == "" {
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
			// guard. Oracle has no such function, and the column maps to a CLOB
			// here, so the check would only break the load.
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

	parts := make([]string, 0, len(table.Columns)+len(tableConstraints))
	for _, col := range table.Columns {
		parts = append(parts, "    "+o.generateColumnSQL(col, inlinePKCols[col.Name]))
	}
	for _, c := range tableConstraints {
		if sql := o.generateConstraintSQL(c); sql != "" {
			parts = append(parts, "    "+sql)
		}
	}

	result.WriteString(strings.Join(parts, ",\n"))
	result.WriteString("\n)")
	if table.TableSpace != "" {
		result.WriteString(" TABLESPACE " + table.TableSpace)
	}
	result.WriteString(";\n")
	return result.String()
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

// generateIndexSQL generates SQL for an index
func (o *Oracle) generateIndexSQL(tableName string, index sqlmapper.Index) string {
	var sql string
	if index.IsBitmap {
		sql = "CREATE BITMAP INDEX "
	} else if index.IsUnique {
		sql = "CREATE UNIQUE INDEX "
	} else {
		sql = "CREATE INDEX "
	}

	sql += index.Name + " ON " + tableName + " (" + strings.Join(index.Columns, ", ") + ")"

	// Add index options
	if index.TableSpace != "" {
		sql += " TABLESPACE " + index.TableSpace
	}

	return sql
}

// splitTopLevelCommas splits a CREATE TABLE body on the commas that separate
// definitions, ignoring the ones nested inside parentheses or string literals.
func splitTopLevelCommas(body string) []string {
	var parts []string
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
