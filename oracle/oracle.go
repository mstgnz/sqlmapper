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
	"github.com/mstgnz/sqlmapper/internal/routine"
	"github.com/mstgnz/sqlmapper/internal/sqlfmt"
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
	oracleEnableRe = regexp.MustCompile(`(?i)\s+(?:ENABLE|DISABLE)\b`)
	// DBMS_METADATA writes USING INDEX with the whole storage clause behind it:
	// USING INDEX PCTFREE 10 INITRANS 2 MAXTRANS 255 TABLESPACE "USERS". Only
	// the keywords were removed before, so the storage words stayed and were
	// read as part of the key.
	oracleUsingIndexRe = regexp.MustCompile(`(?i)\s+USING\s+INDEX\b` +
		`(?:\s+(?:PCTFREE|PCTUSED|INITRANS|MAXTRANS|COMPUTE\s+STATISTICS|NOCOMPRESS|` +
		`LOGGING|NOLOGGING|STORAGE\s*\([^)]*\)|TABLESPACE\s+"?[\w$#]+"?)\s*\d*)*`)
	oracleIdentityRe = regexp.MustCompile(`(?i)\s*GENERATED\s+(?:ALWAYS|BY\s+DEFAULT(?:\s+ON\s+NULL)?)\s+AS\s+IDENTITY` +
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

// oracleAnonymousConstraintRe matches a table constraint written without a name.
var oracleAnonymousConstraintRe = regexp.MustCompile(`(?i)^(?:PRIMARY\s+KEY|UNIQUE|FOREIGN\s+KEY|CHECK)\b`)

// oracleConstraintNameRe reads the name off a named constraint.
var oracleConstraintNameRe = regexp.MustCompile(`(?i)^CONSTRAINT\s+("?[\w$#]+"?)`)

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
		schema: &sqlmapper.Schema{SourceDialect: sqlmapper.Oracle},
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
	// Start from an empty schema. A parser used a second time used to add
	// to what it read the first, so a caller reusing one silently got two
	// schemas merged into one.
	o.schema = &sqlmapper.Schema{SourceDialect: sqlmapper.Oracle}

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

		// CREATE FUNCTION and CREATE PROCEDURE. These were read by the stream
		// parser only, so a dump converted through this path lost them.
		if oracleRoutineRe.MatchString(stmt) {
			if err := o.parseFunctions(stmt); err != nil {
				return nil, fmt.Errorf("error parsing routine: %v", err)
			}
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
		// DBMS_METADATA names a constraint only when the schema did. An unnamed
		// PRIMARY KEY (...) at table level used to fall through to the column
		// parser, which read the word PRIMARY as a column name.
		named := strings.HasPrefix(strings.ToUpper(colDef), "CONSTRAINT")
		if named || oracleAnonymousConstraintRe.MatchString(colDef) {
			constraint := sqlmapper.Constraint{}

			// Read the constraint name, which an anonymous one does not have.
			if named {
				matches := oracleConstraintNameRe.FindStringSubmatch(colDef)
				if len(matches) > 1 {
					constraint.Name = unquoteOracleIdent(matches[1])
				}
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

		// An identity column carries a long option list that would otherwise be
		// read as part of the type and the default. It is stripped first so the
		// fields below are the fields of what is left: taking them from the
		// original text and then slicing the shortened one ran off the end.
		autoIncrement := oracleIdentityRe.MatchString(colDef)
		if autoIncrement {
			colDef = oracleIdentityRe.ReplaceAllString(colDef, "")
		}

		parts := strings.Fields(colDef)
		if len(parts) < 2 {
			continue
		}

		col := sqlmapper.Column{
			Name: unquoteOracleIdent(parts[0]),
			// Oracle columns are nullable unless the definition says otherwise.
			// Leaving this at the zero value marked every column NOT NULL.
			IsNullable:    true,
			AutoIncrement: autoIncrement,
		}

		rest := strings.TrimSpace(colDef[len(parts[0]):])
		applyOracleType(&col, takeOracleType(rest))

		if strings.Contains(strings.ToUpper(colDef), "NOT NULL") {
			col.IsNullable = false
		}

		if strings.Contains(strings.ToUpper(colDef), "DEFAULT") {
			defaultIdx := strings.Index(keyword.UpperASCII(colDef), "DEFAULT")
			// TrimSpace before looking for the terminator: the character right
			// after DEFAULT is a space, so searching the untrimmed remainder
			// found index 0 and every default came out empty.
			restStr := strings.TrimSpace(colDef[defaultIdx+len("DEFAULT"):])
			col.DefaultValue = oracleDefaultValue(restStr)

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

	// NOCACHE is a cache of none, which is what a cache of one means in the
	// schema. Without this the clause was dropped and a sequence written with
	// it came back out caching by default.
	if strings.Contains(upper, "NOCACHE") || strings.Contains(upper, "NO CACHE") {
		seq.Cache = 1
	}

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
	asIndex := strings.Index(keyword.UpperASCII(stmt), " AS ")
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
//
// oracleTriggerNameRe reads the trigger name, which DBMS_METADATA writes quoted
// and schema-qualified, with EDITIONABLE in front of the keyword.
var oracleTriggerNameRe = regexp.MustCompile(`(?i)CREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:NON)?EDITIONABLE\s+)?TRIGGER\s+([."\w$#]+)`)

func (o *Oracle) parseCreateTrigger(stmt string) (sqlmapper.Trigger, error) {
	trigger := sqlmapper.Trigger{}

	// Read the trigger name, which may be schema-qualified. Matching only \w
	// truncated "app.users_bi" to "app".
	matches := oracleTriggerNameRe.FindStringSubmatch(stmt)
	if len(matches) > 1 {
		trigger.Name = matches[1]
		if parts := strings.Split(trigger.Name, "."); len(parts) > 1 {
			trigger.Schema = unquoteOracleIdent(parts[0])
			trigger.Name = unquoteOracleIdent(parts[1])
		} else {
			trigger.Name = unquoteOracleIdent(trigger.Name)
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
	tableRegex := regexp.MustCompile(`(?i)\sON\s+([."\w$#]+)`)
	matches = tableRegex.FindStringSubmatch(stmt)
	if len(matches) > 1 {
		trigger.Table = matches[1]
		if parts := strings.Split(trigger.Table, "."); len(parts) > 1 {
			trigger.Table = parts[1]
		}
		trigger.Table = unquoteOracleIdent(trigger.Table)
	}

	// Check for FOR EACH ROW
	trigger.ForEachRow = strings.Contains(strings.ToUpper(stmt), "FOR EACH ROW")

	// Read the trigger body
	beginIndex := strings.Index(keyword.UpperASCII(stmt), "BEGIN")
	endIndex := strings.LastIndex(keyword.UpperASCII(stmt), "END")
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

	// Create sequences. The stream generator's renderer is used, which carries
	// the bounds and the cache the shorter one here used to drop.
	for _, seq := range schema.Sequences {
		result.WriteString(strings.TrimRight(o.generateSequenceSQL(seq), "\n"))
		result.WriteString(";\n\n")
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
		result.WriteString(o.generateViewSQL(view))
		result.WriteString(";\n\n")
	}

	// Routines come after everything they can refer to. This used to write
	// triggers only, and wrote a foreign body as if it were PL/SQL.
	if routines := o.generateRoutinesSQL(schema); routines != "" {
		result.WriteString(routines)
	}

	return result.String(), nil
}

// parseTablesFromStatement reads one CREATE TABLE with the same code the file
// parser uses. The stream path had a second implementation of its own, and on
// real DBMS_METADATA output it failed outright.
//
// Statements are collapsed onto one line first, because the patterns below the
// file parser were written against that form.
func (o *Oracle) parseTablesFromStatement(statement string) error {
	table, err := o.parseCreateTable(strings.Join(strings.Fields(statement), " "))
	if err != nil {
		return err
	}
	o.schema.Tables = append(o.schema.Tables, table)
	return nil
}

// oracleRoutineRe reads a CREATE FUNCTION or CREATE PROCEDURE as DBMS_METADATA
// writes it: EDITIONABLE in front of the keyword, a quoted schema-qualified
// name, and the body after IS or AS running to the end of the block.
//
// The previous pattern had an unparenthesised alternation, so "IS" and
// "AS ... $" were the two branches and the whole left-hand side was optional.
var oracleRoutineRe = regexp.MustCompile(`(?is)CREATE(?:\s+OR\s+REPLACE)?` +
	`(?:\s+(?:NON)?EDITIONABLE)?\s+(FUNCTION|PROCEDURE)\s+([."\w$#]+)` +
	`\s*(?:\((.*?)\))?\s*(?:RETURN\s+([\w$#]+(?:\s*\([^)]*\))?))?\s+(?:IS|AS)\s+(.*)$`)

func (o *Oracle) parseFunctions(statement string) error {
	matches := oracleRoutineRe.FindStringSubmatch(statement)

	if len(matches) > 5 {
		isProc := strings.EqualFold(matches[1], "PROCEDURE")
		functionName := matches[2]
		function := sqlmapper.Function{
			IsProc: isProc,
			Body:   strings.TrimSpace(matches[5]),
		}

		if !isProc {
			function.Returns = matches[4]
		}

		// Parse schema if exists
		parts := strings.Split(functionName, ".")
		if len(parts) > 1 {
			function.Schema = unquoteOracleIdent(parts[0])
			function.Name = unquoteOracleIdent(parts[1])
		} else {
			function.Name = unquoteOracleIdent(functionName)
		}

		// Parse parameters. Oracle writes the direction after the name, as
		// "v IN NUMBER", so reading the second word as the type gave IN.
		if matches[3] != "" {
			params := strings.Split(matches[3], ",")
			for _, param := range params {
				parts := oracleParamFields(param)
				if len(parts) >= 2 {
					parameter := sqlmapper.Parameter{
						Name:      parts[0],
						Direction: parts[1],
						DataType:  parts[2],
					}
					function.Parameters = append(function.Parameters, parameter)
				}
			}
		}

		o.schema.Functions = append(o.schema.Functions, function)
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
	// Oracle rejects a cache of one: ORA-04013 says the number has to be
	// greater than 1. PostgreSQL writes CACHE 1 for a sequence with no cache at
	// all, which is what NOCACHE means here.
	switch {
	case sequence.Cache > 1:
		sql += fmt.Sprintf("CACHE %d\n", sequence.Cache)
	case sequence.Cache == 1:
		sql += "NOCACHE\n"
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
// oracleKeyTextType is what an unbounded text column becomes when it has to be
// indexed. Oracle cannot index a CLOB at all, and the length has to leave room
// for a multi-byte character set inside the index key limit.
const oracleKeyTextType = "VARCHAR2(1000)"

func (o *Oracle) resolveType(col sqlmapper.Column) string {
	return o.resolveTypeForKey(col, false)
}

// resolveTypeForKey renders a column's type, bounding an unbounded text column
// when the table indexes it.
func (o *Oracle) resolveTypeForKey(col sqlmapper.Column, isKey bool) string {
	rendered := o.resolveTypeName(col)
	if isKey && col.Length == 0 && oracleUnboundedText[rendered] {
		return oracleKeyTextType
	}
	return rendered
}

// oracleUnboundedText are the Oracle types that carry no length and so cannot
// take part in an index.
var oracleUnboundedText = map[string]bool{
	"CLOB": true, "NCLOB": true, "BLOB": true, "LONG": true,
}

func (o *Oracle) resolveTypeName(col sqlmapper.Column) string {
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
func (o *Oracle) generateColumnSQL(col sqlmapper.Column, inlinePK, isKey bool) string {
	oracleType := o.resolveTypeForKey(col, isKey)
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
		// A character column cannot be assigned a timestamp: Oracle answers
		// ORA-00932. SQLite stores the current time as text and means exactly
		// this conversion, so it is written out rather than dropped.
		if oracleIsCharacterType(oracleType) {
			return "TO_CHAR(SYSTIMESTAMP)"
		}
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
	result.WriteString("CREATE TABLE ")
	result.WriteString(table.Name)
	result.WriteString(" (\n")

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

	// A column the table indexes cannot be an unbounded CLOB, so the type
	// resolution needs to know which ones those are.
	keyCols := sqlmapper.KeyColumns(table)

	parts := make([]string, 0, len(table.Columns)+len(tableConstraints))
	for _, col := range table.Columns {
		parts = append(parts, "    "+o.generateColumnSQL(col, inlinePKCols[col.Name], keyCols[col.Name]))
	}
	for _, c := range tableConstraints {
		if sql := o.generateConstraintSQL(c); sql != "" {
			parts = append(parts, "    "+sql)
		}
	}

	result.WriteString(strings.Join(parts, ",\n"))
	result.WriteString("\n)")
	if table.TableSpace != "" {
		result.WriteString(" TABLESPACE ")
		result.WriteString(table.TableSpace)
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

// generateRoutinesSQL renders the routine section of a dump.
//
// A PL/SQL block ends at a slash on its own line, not at the semicolon that
// closes its last statement, which is why every routine here is followed by
// one.
func (o *Oracle) generateRoutinesSQL(schema *sqlmapper.Schema) string {
	if routine.Count(schema) == 0 {
		return ""
	}
	if !schema.RoutinesAreNativeTo(sqlmapper.Oracle) {
		return routine.ForeignSQL(schema)
	}

	var stmts []string

	for _, fn := range schema.Functions {
		if fn.IsProc {
			stmts = append(stmts, fmt.Sprintf("CREATE OR REPLACE PROCEDURE %s(%s)\n%s",
				fn.Name, oracleParams(fn.Parameters), oracleRoutineBody(fn.Body)))
			continue
		}
		stmts = append(stmts, fmt.Sprintf("CREATE OR REPLACE FUNCTION %s(%s) RETURN %s\n%s",
			fn.Name, oracleParams(fn.Parameters), fn.Returns, oracleRoutineBody(fn.Body)))
	}

	for _, pr := range schema.Procedures {
		stmts = append(stmts, fmt.Sprintf("CREATE OR REPLACE PROCEDURE %s(%s)\n%s",
			pr.Name, oracleParams(pr.Parameters), oracleRoutineBody(pr.Body)))
	}

	for _, tr := range schema.Triggers {
		header := "CREATE OR REPLACE " + strings.TrimPrefix(
			sqlfmt.TriggerHeader(tr.Name, tr.Timing, tr.Event, tr.Table, tr.ForEachRow), "CREATE ")
		stmts = append(stmts, sqlfmt.SourceRoutineSQL(header, tr.Body))
	}

	var sb strings.Builder
	for _, stmt := range stmts {
		sb.WriteString(sqlfmt.Terminate(stmt, ";"))
		sb.WriteString("\n/\n\n")
	}
	return sb.String()
}

// oracleIsCharacterType reports whether a rendered type holds text.
func oracleIsCharacterType(rendered string) bool {
	name := rendered
	if i := strings.IndexByte(name, '('); i != -1 {
		name = name[:i]
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "CLOB", "NCLOB", "VARCHAR2", "NVARCHAR2", "CHAR", "NCHAR", "LONG":
		return true
	}
	return false
}

// oracleParamFields splits a parameter into name, direction and type. Oracle
// writes the direction between them and leaves it out for the common IN case,
// so the returned slice always has all three, with an empty direction when none
// was written.
func oracleParamFields(param string) []string {
	fields := strings.Fields(strings.TrimSpace(param))
	if len(fields) < 2 {
		return nil
	}

	name, rest := fields[0], fields[1:]

	direction := ""
	switch {
	case len(rest) >= 2 && strings.EqualFold(rest[0], "IN") && strings.EqualFold(rest[1], "OUT"):
		direction, rest = "IN OUT", rest[2:]
	case strings.EqualFold(rest[0], "IN"), strings.EqualFold(rest[0], "OUT"):
		direction, rest = strings.ToUpper(rest[0]), rest[1:]
	}
	if len(rest) == 0 {
		return nil
	}

	return []string{name, direction, strings.Join(rest, " ")}
}

// oracleParams renders a parameter list the way Oracle writes one, with the
// direction after the name rather than before it.
func oracleParams(params []sqlmapper.Parameter) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		part := p.Name
		if p.Direction != "" {
			part += " " + p.Direction
		}
		parts = append(parts, part+" "+p.DataType)
	}
	return strings.Join(parts, ", ")
}

// oracleRoutineBody puts the IS a PL/SQL block needs in front of it. Without it
// the server sees a function header running straight into BEGIN and rejects the
// statement.
func oracleRoutineBody(body string) string {
	b := strings.TrimSpace(body)
	upper := strings.ToUpper(b)
	if strings.HasPrefix(upper, "IS") || strings.HasPrefix(upper, "AS") {
		return b
	}
	return "IS\n" + b
}

// generateViewSQL renders a view definition, without its terminator.
//
// Both Generate and GenerateStream call it. The stream used to write the
// definition as it stood, and Oracle has no boolean, so a view saying
// WHERE is_active did not load there.
func (o *Oracle) generateViewSQL(view sqlmapper.View) string {
	body := expr.TranslateViewBody(strings.TrimSuffix(strings.TrimSpace(view.Definition), ";"), expr.Oracle)
	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS\n%s", view.Name, body)
}

// oracleDefaultValue reads what follows DEFAULT: a quoted literal, a call with
// its own parentheses, or a bare token.
//
// Reading up to the first space cut a literal in half at "two words", and
// keeping the quotes on the value corrupted the default in every conversion,
// because the schema holds the value and each generator quotes it again.
func oracleDefaultValue(rest string) string {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}

	if rest[0] == '\'' {
		for i := 1; i < len(rest); i++ {
			if rest[i] != '\'' {
				continue
			}
			if i+1 < len(rest) && rest[i+1] == '\'' {
				i++
				continue
			}
			return sqlfmt.UnquoteLiteral(rest[:i+1])
		}
		return sqlfmt.UnquoteLiteral(rest)
	}

	if open := strings.IndexByte(rest, '('); open != -1 && open < strings.IndexByte(rest+" ", ' ') {
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
	}

	return strings.Fields(rest)[0]
}
