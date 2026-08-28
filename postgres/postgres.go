// Package postgres provides functionality for parsing and generating PostgreSQL database schemas.
// It implements the Parser interface for handling PostgreSQL specific SQL syntax and schema structures.
package postgres

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

// toPostgresType maps any source database type (MySQL or PostgreSQL) to the PostgreSQL equivalent.
// This is used during Generate to convert parsed schemas to valid PostgreSQL SQL.
var toPostgresType = map[string]string{
	// MySQL-only types
	"tinyint":    "smallint",
	"mediumint":  "integer",
	"int":        "integer",
	"float":      "real",
	"double":     "double precision",
	"tinytext":   "text",
	"mediumtext": "text",
	"longtext":   "text",
	"json":       "jsonb",
	"datetime":   "timestamp",
	"blob":       "bytea",
	"tinyblob":   "bytea",
	"mediumblob": "bytea",
	"longblob":   "bytea",
	"enum":       "text",
	"set":        "text",
	"bool":       "boolean",
	"bit":        "bit",
	"year":       "smallint",
	// Types shared by MySQL and PostgreSQL (or PostgreSQL-only)
	"smallint":  "smallint",
	"bigint":    "bigint",
	"decimal":   "decimal",
	"numeric":   "numeric",
	"char":      "char",
	"varchar":   "varchar",
	"text":      "text",
	"boolean":   "boolean",
	"date":      "date",
	"time":      "time",
	"timestamp": "timestamp",
	// PostgreSQL aliases
	"integer":                     "integer",
	"real":                        "real",
	"double precision":            "double precision",
	"character varying":           "varchar",
	"character":                   "char",
	"bytea":                       "bytea",
	"jsonb":                       "jsonb",
	"uuid":                        "uuid",
	"inet":                        "inet",
	"cidr":                        "cidr",
	"macaddr":                     "macaddr",
	"serial":                      "serial",
	"bigserial":                   "bigserial",
	"smallserial":                 "smallserial",
	"interval":                    "interval",
	"point":                       "point",
	"line":                        "line",
	"lseg":                        "lseg",
	"box":                         "box",
	"path":                        "path",
	"polygon":                     "polygon",
	"circle":                      "circle",
	"timestamptz":                 "timestamp with time zone",
	"timestamp with time zone":    "timestamp with time zone",
	"timestamp without time zone": "timestamp",
}

// Package-level compiled regexes for performance.
var (
	pgCommentRe      = regexp.MustCompile(`(?m)--.*$`)
	pgBlockCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	pgCopyBlockRe    = regexp.MustCompile(`(?s)COPY\s+\S[^\n]*\n.*?^\\\.\n`) // pg_dump COPY ... FROM stdin blocks
	pgWhitespaceRe   = regexp.MustCompile(`\s+`)
	// pg_dump emits FKs/PKs/UNIQUEs as ALTER TABLE after all CREATE TABLEs.
	pgAlterTableRe = regexp.MustCompile(`(?im)ALTER\s+TABLE\s+(?:ONLY\s+)?([\w."]+)\s+ADD\s+CONSTRAINT\s+(\w+)\s+(.+?)\s*;`)
	pgDBRe         = regexp.MustCompile(`(?i)CREATE\s+DATABASE\s+(\w+)`)
	pgSchemaRe     = regexp.MustCompile(`(?i)CREATE\s+SCHEMA\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)
	pgEnumRe       = regexp.MustCompile(`(?i)CREATE\s+TYPE\s+([\w.]+)\s+AS\s+ENUM\s*\(([^)]+)\);`)
	pgCompositeRe  = regexp.MustCompile(`(?i)CREATE\s+TYPE\s+([\w.]+)\s+AS\s*\(([^)]+)\);`)
	pgExtRe        = regexp.MustCompile(`(?i)CREATE\s+EXTENSION(?:\s+IF\s+NOT\s+EXISTS)?\s+(?:"([^"]+)"|(\w[\w-]*))\s*(?:WITH\s+SCHEMA\s+(\w+))?\s*;`)
	pgSeqRe        = regexp.MustCompile(`(?i)CREATE\s+(?:TEMP(?:ORARY)?\s+)?SEQUENCE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w."]+)([^;]*);`)
	// pg_dump writes sequence options in its own order and uses NO MINVALUE /
	// NO MAXVALUE, so each option is matched on its own rather than in sequence.
	pgSeqIncrementRe = regexp.MustCompile(`(?i)INCREMENT(?:\s+BY)?\s+(-?\d+)`)
	pgSeqMinRe       = regexp.MustCompile(`(?i)(?:^|\s)MINVALUE\s+(-?\d+)`)
	pgSeqMaxRe       = regexp.MustCompile(`(?i)(?:^|\s)MAXVALUE\s+(-?\d+)`)
	pgSeqStartRe     = regexp.MustCompile(`(?i)START(?:\s+WITH)?\s+(-?\d+)`)
	pgSeqCacheRe     = regexp.MustCompile(`(?i)CACHE\s+(-?\d+)`)
	pgSeqCycleRe     = regexp.MustCompile(`(?i)(?:^|\s)CYCLE`)
	pgSeqNoCycleRe   = regexp.MustCompile(`(?i)NO\s+CYCLE`)
	// pg_dump attaches sequences to columns after the fact:
	//   ALTER TABLE ONLY t ALTER COLUMN id SET DEFAULT nextval('t_id_seq'::regclass);
	//   ALTER TABLE t ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (...);
	pgAlterColDefaultRe  = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:ONLY\s+)?([\w."]+)\s+ALTER\s+(?:COLUMN\s+)?([\w."]+)\s+SET\s+DEFAULT\s+([^;]+);`)
	pgAlterColIdentityRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+(?:ONLY\s+)?([\w."]+)\s+ALTER\s+(?:COLUMN\s+)?([\w."]+)\s+ADD\s+GENERATED\s+(?:ALWAYS|BY\s+DEFAULT)\s+AS\s+IDENTITY`)
	pgCreateTableRe      = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([\w.]+)\s*\(`)
	pgTableSpaceRe       = regexp.MustCompile(`(?i)TABLESPACE\s+(\w+)\s*$`)
	pgIndexRe            = regexp.MustCompile(`(?i)CREATE\s+(UNIQUE\s+)?INDEX\s+(\w+)\s+ON\s+([\w.]+)\s*(?:USING\s+(\w+)\s*)?\(([^)]+)\)(?:\s+WHERE\s+(.+?))?(?:\s+TABLESPACE\s+(\w+))?\s*;`)
	pgViewRe             = regexp.MustCompile(`(?i)CREATE(?:\s+OR\s+REPLACE)?\s+VIEW\s+([\w.]+)\s+AS\s+(.*?);`)
	pgMatViewRe          = regexp.MustCompile(`(?i)CREATE\s+MATERIALIZED\s+VIEW\s+([\w.]+)(?:\s+WITH\s*\([^)]*\))?\s+AS\s+(.*?)\s+WITH\s+(?:NO\s+)?DATA\s*;`)
	// pg_dump writes the attributes between RETURNS and the body:
	//
	//	CREATE FUNCTION public.touch() RETURNS trigger
	//	    LANGUAGE plpgsql
	//	    AS $$ ... $$;
	//
	// while hand-written SQL usually puts LANGUAGE after it. Insisting on one
	// order meant no function in a real dump was found at all, so what is
	// between them is captured and read afterwards.
	pgFuncRe = regexp.MustCompile(`(?is)CREATE(?:\s+OR\s+REPLACE)?\s+FUNCTION\s+([\w."]+)\s*\((.*?)\)\s+RETURNS\s+`)

	// pgFuncAttrRe finds an attribute wherever the writer put it.
	pgLanguageRe = regexp.MustCompile(`(?i)\bLANGUAGE\s+(\w+)`)

	// pgReturnStopRe marks where the return type ends and the function's
	// attributes begin.
	pgReturnStopRe = regexp.MustCompile(`(?is)\s+(?:LANGUAGE|AS|SET|SECURITY|STABLE|IMMUTABLE|VOLATILE|STRICT|PARALLEL|COST|ROWS|WINDOW|LEAKPROOF|CALLED|RETURNS)\b`)
	pgProcRe       = regexp.MustCompile(`(?i)CREATE(?:\s+OR\s+REPLACE)?\s+PROCEDURE\s+([\w.]+)\s*\((.*?)\)\s+LANGUAGE\s+(\w+)\s+AS\s+\$\$(.*?)\$\$`)
	pgTriggerRe    = regexp.MustCompile(`(?i)CREATE\s+TRIGGER\s+(\w+)\s+(BEFORE|AFTER|INSTEAD\s+OF)\s+(INSERT|UPDATE|DELETE)\s+ON\s+([\w.]+)\s+(?:FOR\s+EACH\s+ROW\s+)?EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+([\w.]+)`)
	pgCondTrigRe   = regexp.MustCompile(`(?i)CREATE\s+TRIGGER\s+(\w+)\s+(BEFORE|AFTER|INSTEAD\s+OF)\s+(?:UPDATE\s+OF\s+[\w.]+\s+)?ON\s+([\w.]+)\s+(?:FOR\s+EACH\s+ROW\s+)?WHEN\s+\((.+?)\)\s+EXECUTE\s+(?:FUNCTION|PROCEDURE)\s+([\w.]+)`)
	pgGrantRe      = regexp.MustCompile(`(?i)GRANT\s+(.*?)\s+ON\s+(?:TABLE\s+)?([\w.]+)\s+TO\s+(\w+)(?:\s+WITH\s+GRANT\s+OPTION)?\s*;`)
	pgGrantAllRe   = regexp.MustCompile(`(?i)GRANT\s+ALL\s+PRIVILEGES\s+ON\s+(?:ALL\s+TABLES\s+IN\s+SCHEMA\s+)?([\w.]+)\s+TO\s+(\w+)(?:\s+WITH\s+GRANT\s+OPTION)?\s*;`)
	pgGrantExecRe  = regexp.MustCompile(`(?i)GRANT\s+EXECUTE\s+ON\s+(?:FUNCTION|PROCEDURE)\s+([\w.]+)\s*\([^)]*\)\s+TO\s+(\w+)(?:\s+WITH\s+GRANT\s+OPTION)?\s*;`)
	pgRevokeRe     = regexp.MustCompile(`(?i)REVOKE\s+(.*?)\s+ON\s+(?:TABLE\s+)?([\w.]+)\s+FROM\s+(\w+)\s*;`)

	pgTableCommentRe = regexp.MustCompile(`(?i)COMMENT\s+ON\s+TABLE\s+([\w.]+)\s+IS\s+'([^']+)'\s*;`)
	pgColCommentRe   = regexp.MustCompile(`(?i)COMMENT\s+ON\s+COLUMN\s+([\w.]+)\.(\w+)\s+IS\s+'([^']+)'\s*;`)

	pgTypeWithLenRe = regexp.MustCompile(`^(\w[\w\s]*?)\s*\((\d+)(?:\s*,\s*(\d+))?\)$`)
	pgCheckRe       = regexp.MustCompile(`(?i)CHECK\s*\((.+)\)`)
	pgConstraintRe  = regexp.MustCompile(`(?i)CONSTRAINT\s+(\w+)\s+(.*)`)
	pgPKRe          = regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(([^)]+)\)`)
	pgFKRe          = regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(([^)]+)\)\s*REFERENCES\s+([\w.]+)\s*\(([^)]+)\)`)
	pgUniqueRe      = regexp.MustCompile(`(?i)UNIQUE\s*\(([^)]+)\)`)
)

// PostgreSQL represents a PostgreSQL parser implementation.
type PostgreSQL struct {
	schema *sqlmapper.Schema
}

// NewPostgreSQL creates and initializes a new PostgreSQL parser instance.
func NewPostgreSQL() sqlmapper.Database {
	return &PostgreSQL{
		schema: &sqlmapper.Schema{SourceDialect: sqlmapper.PostgreSQL},
	}
}

// Parse takes a PostgreSQL SQL dump and parses it into a common schema structure.
func (p *PostgreSQL) Parse(content string) (*sqlmapper.Schema, error) {
	// Start from an empty schema. A parser used a second time used to add
	// to what it read the first, so a caller reusing one silently got two
	// schemas merged into one.
	p.schema = &sqlmapper.Schema{SourceDialect: sqlmapper.PostgreSQL}

	if content == "" {
		return nil, errors.New("empty content")
	}

	content = p.normalizeContent(content)

	if err := p.parseSchemas(content); err != nil {
		return nil, fmt.Errorf("error parsing schemas: %v", err)
	}
	if err := p.parseTypes(content); err != nil {
		return nil, fmt.Errorf("error parsing types: %v", err)
	}
	if err := p.parseExtensions(content); err != nil {
		return nil, fmt.Errorf("error parsing extensions: %v", err)
	}
	if err := p.parseSequences(content); err != nil {
		return nil, fmt.Errorf("error parsing sequences: %v", err)
	}
	if err := p.parseTables(content); err != nil {
		return nil, fmt.Errorf("error parsing tables: %v", err)
	}
	if err := p.parseAlterTableConstraints(content); err != nil {
		return nil, fmt.Errorf("error parsing alter table constraints: %v", err)
	}
	if err := p.parseAlterColumnDefaults(content); err != nil {
		return nil, fmt.Errorf("error parsing alter column defaults: %v", err)
	}
	if err := p.parseIndexes(content); err != nil {
		return nil, fmt.Errorf("error parsing indexes: %v", err)
	}
	if err := p.parseViews(content); err != nil {
		return nil, fmt.Errorf("error parsing views: %v", err)
	}
	if err := p.parseFunctions(content); err != nil {
		return nil, fmt.Errorf("error parsing functions: %v", err)
	}
	if err := p.parseTriggers(content); err != nil {
		return nil, fmt.Errorf("error parsing triggers: %v", err)
	}
	if err := p.parsePermissions(content); err != nil {
		return nil, fmt.Errorf("error parsing permissions: %v", err)
	}

	return p.schema, nil
}

// Generate creates PostgreSQL SQL from a schema structure.
// It applies type mapping so it correctly handles schemas parsed from MySQL.
func (p *PostgreSQL) Generate(schema *sqlmapper.Schema) (string, error) {
	if schema == nil {
		return "", errors.New("empty schema")
	}

	var result strings.Builder

	// Dump tools do not order tables by dependency, so a child table can precede
	// its parent and the foreign key would fail to resolve.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)

	// Types the source declared come first, because a column may be one. The
	// file generator used to emit none of them, so a schema it had just parsed
	// came back out with tables referring to a type nothing had created.
	for _, typ := range schema.Types {
		result.WriteString(sqlfmt.Terminate(p.generateTypeSQL(typ), ";"))
		result.WriteString("\n")
	}

	for _, stmt := range p.enumTypesSQL(tables) {
		result.WriteString(stmt)
		result.WriteString("\n")
	}

	for _, table := range tables {
		result.WriteString(p.generateTableSQL(table, deferredFKs[table.Name]))
		result.WriteString("\n")

		for _, idx := range table.Indexes {
			result.WriteString(p.generateIndexSQL(table.Name, idx))
			result.WriteString("\n")
		}
	}

	// Foreign keys that close a cycle cannot be satisfied by ordering, so they
	// are added once every table exists.
	for _, table := range tables {
		for _, c := range deferredFKs[table.Name] {
			result.WriteString(fmt.Sprintf("ALTER TABLE %s ADD %s;\n", table.Name, p.generateConstraintSQL(c)))
		}
	}

	// Views are emitted last so the tables they select from already exist. The
	// body is carried over verbatim: this package converts DDL structure, not
	// query syntax, so a view written in another dialect's SQL may need editing.
	booleans := p.booleanColumns(schema)
	for _, view := range schema.Views {
		result.WriteString(p.generateViewSQL(view, booleans))
		result.WriteString("\n")
	}

	// Routines come after everything they can refer to.
	if routines := p.generateRoutinesSQL(schema); routines != "" {
		result.WriteString("\n")
		result.WriteString(routines)
	}

	return result.String(), nil
}

// generateViewSQL renders a view definition. The SELECT body is passed through
// unchanged; only the wrapper is dialect-specific.
func (p *PostgreSQL) generateViewSQL(view sqlmapper.View, booleans map[string]bool) string {
	body := expr.TranslateViewBodyWithBooleans(strings.TrimSpace(view.Definition), expr.PostgreSQL, booleans)
	body = strings.TrimSuffix(body, ";")
	keyword := "CREATE VIEW"
	if view.IsMaterialized {
		keyword = "CREATE MATERIALIZED VIEW"
	}
	return fmt.Sprintf("%s %s AS %s;", keyword, view.Name, body)
}

func (p *PostgreSQL) normalizeContent(content string) string {
	// Strip COPY ... FROM stdin data blocks first: they require raw newlines.
	content = pgCopyBlockRe.ReplaceAllString(content, "")
	// Strip block comments (/* ... */)
	content = pgBlockCommentRe.ReplaceAllString(content, "")
	// Strip line comments (-- ...)
	content = pgCommentRe.ReplaceAllString(content, "")
	content = strings.TrimSpace(content)
	// Collapse all whitespace so that multi-line patterns match uniformly.
	content = pgWhitespaceRe.ReplaceAllString(content, " ")
	return content
}

func (p *PostgreSQL) parseSchemas(content string) error {
	if matches := pgDBRe.FindStringSubmatch(content); len(matches) > 1 {
		p.schema.Name = matches[1]
		return nil
	}
	matches := pgSchemaRe.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		if len(m) > 1 {
			p.schema.Name = m[1]
		}
	}
	return nil
}

func (p *PostgreSQL) parseTypes(content string) error {
	for _, m := range pgEnumRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		typ := sqlmapper.Type{Name: m[1], Kind: "ENUM", Definition: m[2]}
		if parts := strings.Split(m[1], "."); len(parts) > 1 {
			typ.Schema = parts[0]
			typ.Name = parts[1]
		}
		p.schema.Types = append(p.schema.Types, typ)
	}
	for _, m := range pgCompositeRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		typ := sqlmapper.Type{Name: m[1], Kind: "COMPOSITE", Definition: m[2]}
		if parts := strings.Split(m[1], "."); len(parts) > 1 {
			typ.Schema = parts[0]
			typ.Name = parts[1]
		}
		p.schema.Types = append(p.schema.Types, typ)
	}
	return nil
}

func (p *PostgreSQL) parseExtensions(content string) error {
	for _, m := range pgExtRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		ext := sqlmapper.Extension{Name: m[1]}
		if ext.Name == "" {
			ext.Name = m[2]
		}
		if len(m) > 3 && m[3] != "" {
			ext.Schema = m[3]
		}
		p.schema.Extensions = append(p.schema.Extensions, ext)
	}
	return nil
}

func (p *PostgreSQL) parseSequences(content string) error {
	for _, m := range pgSeqRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		name := strings.Trim(m[1], `"`)
		seq := sqlmapper.Sequence{Name: name, IncrementBy: 1}
		if parts := strings.Split(name, "."); len(parts) > 1 {
			seq.Schema = strings.Trim(parts[0], `"`)
			seq.Name = strings.Trim(parts[1], `"`)
		}

		opts := m[2]
		if o := pgSeqIncrementRe.FindStringSubmatch(opts); len(o) > 1 {
			seq.IncrementBy = atoi(o[1])
		}
		if o := pgSeqMinRe.FindStringSubmatch(opts); len(o) > 1 {
			seq.MinValue = atoi(o[1])
		}
		if o := pgSeqMaxRe.FindStringSubmatch(opts); len(o) > 1 {
			seq.MaxValue = atoi(o[1])
		}
		if o := pgSeqStartRe.FindStringSubmatch(opts); len(o) > 1 {
			seq.StartValue = atoi(o[1])
		}
		if o := pgSeqCacheRe.FindStringSubmatch(opts); len(o) > 1 {
			seq.Cache = atoi(o[1])
		}
		if pgSeqCycleRe.MatchString(opts) && !pgSeqNoCycleRe.MatchString(opts) {
			seq.Cycle = true
		}

		p.schema.Sequences = append(p.schema.Sequences, seq)
	}
	return nil
}

// parseAlterColumnDefaults resolves the pg_dump idiom of declaring a plain
// integer column and wiring it to a sequence in a later ALTER TABLE. Without
// this the column loses its auto-increment behaviour on the way out.
func (p *PostgreSQL) parseAlterColumnDefaults(content string) error {
	apply := func(rawTable, rawColumn string, fn func(*sqlmapper.Column)) {
		table := strings.Trim(rawTable, `"`)
		if parts := strings.Split(table, "."); len(parts) > 1 {
			table = strings.Trim(parts[1], `"`)
		}
		column := strings.Trim(rawColumn, `"`)
		for ti := range p.schema.Tables {
			if p.schema.Tables[ti].Name != table {
				continue
			}
			for ci := range p.schema.Tables[ti].Columns {
				if p.schema.Tables[ti].Columns[ci].Name == column {
					fn(&p.schema.Tables[ti].Columns[ci])
					return
				}
			}
		}
	}

	for _, m := range pgAlterColDefaultRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 4 {
			continue
		}
		value := strings.TrimSpace(m[3])
		upper := strings.ToUpper(value)
		apply(m[1], m[2], func(col *sqlmapper.Column) {
			switch {
			case strings.HasPrefix(upper, "NEXTVAL("):
				col.AutoIncrement = true
				col.DefaultValue = ""
			case strings.Contains(upper, "CURRENT_TIMESTAMP") || strings.Contains(upper, "NOW()"):
				col.DefaultValue = "CURRENT_TIMESTAMP"
			case strings.HasPrefix(value, "'"):
				if q := regexp.MustCompile(`'([^']*)'`).FindStringSubmatch(value); len(q) > 1 {
					col.DefaultValue = q[1]
				}
			case upper == "NULL":
				col.DefaultValue = ""
			default:
				col.DefaultValue = strings.TrimSpace(pgCastRe.ReplaceAllString(value, ""))
			}
		})
	}

	for _, m := range pgAlterColIdentityRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		apply(m[1], m[2], func(col *sqlmapper.Column) {
			col.AutoIncrement = true
			col.DefaultValue = ""
		})
	}

	return nil
}

func (p *PostgreSQL) parseTables(content string) error {
	// Comments arrive in COMMENT ON statements of their own, anywhere in the
	// file, so they are collected once here rather than looked for again for
	// every table. Scanning the whole dump inside the loop made the cost grow
	// with the square of the table count.
	tableComments, columnComments := pgComments(content)

	// Split into statements to avoid greedy matching across multiple CREATE TABLE blocks.
	statements := strings.Split(content, ";")
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		loc := pgCreateTableRe.FindStringSubmatchIndex(stmt)
		if loc == nil {
			continue
		}
		tableName := stmt[loc[2]:loc[3]]
		openParen := loc[1] - 1

		body, closeIdx := extractBalancedParens(stmt, openParen)
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

		// Check for TABLESPACE after the closing paren
		remainder := strings.TrimSpace(stmt[closeIdx+1:])
		if m := pgTableSpaceRe.FindStringSubmatch(remainder); len(m) > 1 {
			table.TableSpace = m[1]
		}

		if err := p.parseColumnsAndConstraints(body, &table); err != nil {
			return err
		}

		// Comments stated by a later COMMENT ON, collected once before the loop.
		table.Comment = tableComments[tableName]
		if byColumn := columnComments[tableName]; byColumn != nil {
			for i := range table.Columns {
				if c, ok := byColumn[table.Columns[i].Name]; ok {
					table.Columns[i].Comment = c
				}
			}
		}

		for i := range table.Columns {
			table.Columns[i].Order = i + 1
		}
		p.schema.Tables = append(p.schema.Tables, table)
	}
	return nil
}

// parseAlterTableConstraints handles the pg_dump pattern:
//
//	ALTER TABLE [ONLY] schema.table ADD CONSTRAINT name FOREIGN KEY (...) REFERENCES ...;
//	ALTER TABLE [ONLY] schema.table ADD CONSTRAINT name PRIMARY KEY (...);
//	ALTER TABLE [ONLY] schema.table ADD CONSTRAINT name UNIQUE (...);
//
// These are emitted after CREATE TABLE in a real pg_dump file.
func (p *PostgreSQL) parseAlterTableConstraints(content string) error {
	for _, m := range pgAlterTableRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 4 {
			continue
		}
		rawTable := strings.Trim(m[1], `"`)
		constraintName := m[2]
		body := strings.TrimSpace(m[3])

		// Resolve schema.table → just table name for lookup
		tableLookup := rawTable
		schemaPrefix := ""
		if parts := strings.Split(rawTable, "."); len(parts) > 1 {
			schemaPrefix = strings.Trim(parts[0], `"`)
			tableLookup = strings.Trim(parts[1], `"`)
		}

		// Find the target table in the schema
		tableIdx := -1
		for i, t := range p.schema.Tables {
			if t.Name == tableLookup || (t.Schema == schemaPrefix && t.Name == tableLookup) {
				tableIdx = i
				break
			}
		}
		if tableIdx == -1 {
			continue
		}

		c, err := p.parseConstraint(body)
		if err != nil {
			continue
		}
		if constraintName != "" {
			c.Name = constraintName
		}
		if c.Type == "" {
			continue
		}

		// If this is a PK, also mark the relevant columns
		if c.Type == "PRIMARY KEY" {
			for _, pkCol := range c.Columns {
				for ci := range p.schema.Tables[tableIdx].Columns {
					if p.schema.Tables[tableIdx].Columns[ci].Name == pkCol {
						p.schema.Tables[tableIdx].Columns[ci].IsPrimaryKey = true
						p.schema.Tables[tableIdx].Columns[ci].IsNullable = false
					}
				}
			}
		}

		// Avoid duplicates (constraint with same name already parsed inline)
		for _, existing := range p.schema.Tables[tableIdx].Constraints {
			if existing.Name == constraintName && existing.Type == c.Type {
				goto nextConstraint
			}
		}
		p.schema.Tables[tableIdx].Constraints = append(p.schema.Tables[tableIdx].Constraints, c)
	nextConstraint:
	}
	return nil
}

// extractBalancedParens returns the content between the matching parentheses
// starting at openIdx, and the index of the closing paren.
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
func (p *PostgreSQL) parseColumnsAndConstraints(columnDefs string, table *sqlmapper.Table) error {
	defs := strings.Split(columnDefs, ",")
	var finalDefs []string
	var current strings.Builder
	parenCount := 0

	for _, def := range defs {
		parenCount += strings.Count(def, "(") - strings.Count(def, ")")
		if parenCount > 0 {
			current.WriteString(def)
			current.WriteString(",")
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

		// Detect table-level constraints by leading keyword
		isConstraint := strings.HasPrefix(defUpper, "CONSTRAINT") ||
			strings.HasPrefix(defUpper, "PRIMARY KEY") ||
			strings.HasPrefix(defUpper, "FOREIGN KEY") ||
			strings.HasPrefix(defUpper, "UNIQUE") ||
			strings.HasPrefix(defUpper, "CHECK")

		if isConstraint {
			c, err := p.parseConstraint(def)
			if err != nil {
				return err
			}
			table.Constraints = append(table.Constraints, c)
			continue
		}

		// Column definition
		if strings.Contains(def, " ") {
			col, err := p.parseColumn(def)
			if err != nil {
				return err
			}

			if strings.Contains(defUpper, "PRIMARY KEY") {
				col.IsPrimaryKey = true
				col.IsNullable = false
				table.Constraints = append(table.Constraints, sqlmapper.Constraint{
					Type:    "PRIMARY KEY",
					Columns: []string{col.Name},
				})
			}
			if strings.Contains(defUpper, "UNIQUE") {
				col.IsUnique = true
			}
			if strings.Contains(defUpper, "CHECK") {
				if m := pgCheckRe.FindStringSubmatch(def); len(m) > 1 {
					col.CheckExpression = m[1]
					table.Constraints = append(table.Constraints, sqlmapper.Constraint{
						Type:            "CHECK",
						Columns:         []string{col.Name},
						CheckExpression: m[1],
					})
				}
			}

			table.Columns = append(table.Columns, col)
		}
	}
	return nil
}

// pgTypeStopWords marks the tokens that end a type expression and begin the
// column's attributes. Anything before the first stop word is part of the type,
// which lets multi-word types like "character varying(255)" survive intact.
var pgTypeStopWords = map[string]bool{
	"NOT": true, "NULL": true, "DEFAULT": true, "PRIMARY": true,
	"UNIQUE": true, "CHECK": true, "REFERENCES": true, "GENERATED": true,
	"COLLATE": true, "CONSTRAINT": true,
}

// pgDefaultStopWords marks the tokens that end a DEFAULT clause. NULL is absent
// on purpose: "DEFAULT NULL" is a value, not an attribute.
var pgDefaultStopWords = map[string]bool{
	"NOT": true, "PRIMARY": true, "UNIQUE": true, "CHECK": true,
	"REFERENCES": true, "GENERATED": true, "COLLATE": true, "CONSTRAINT": true,
}

// pgCastRe matches PostgreSQL's ::type cast suffix, including multi-word target
// types such as ::character varying.
var pgCastRe = regexp.MustCompile(`::\s*[a-zA-Z_][\w]*(\s+[a-zA-Z_][\w]*)*(\s*\(\s*\d+(\s*,\s*\d+)?\s*\))?(\s*\[\s*\])?`)

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

// normalizePGTypeName folds PostgreSQL's spelled-out type names onto the short
// names the schema model uses.
func normalizePGTypeName(name string) string {
	n := strings.ToLower(strings.Join(strings.Fields(name), " "))
	switch n {
	case "character varying":
		return "varchar"
	case "character":
		return "char"
	case "timestamp without time zone":
		return "timestamp"
	case "time without time zone":
		return "time"
	case "time with time zone":
		return "timetz"
	case "bit varying":
		return "varbit"
	}
	return n
}

// applyPGType fills DataType, Length, Scale and IsArray from a raw type
// expression such as "character varying(255)", "numeric(10,2)" or "text[]".
func applyPGType(column *sqlmapper.Column, typeExpr string) {
	typeExpr = strings.TrimSpace(typeExpr)

	// Array suffix, possibly repeated or spaced: text[], integer [], text[][]
	for {
		trimmed := strings.TrimSpace(typeExpr)
		if !strings.HasSuffix(trimmed, "]") {
			break
		}
		open := strings.LastIndex(trimmed, "[")
		if open == -1 {
			break
		}
		column.IsArray = true
		typeExpr = strings.TrimSpace(trimmed[:open])
	}

	if m := pgTypeWithLenRe.FindStringSubmatch(typeExpr); len(m) > 2 {
		column.DataType = normalizePGTypeName(m[1])
		column.Length = atoi(m[2])
		if len(m) > 3 && m[3] != "" {
			column.Scale = atoi(m[3])
		}
		return
	}

	column.DataType = normalizePGTypeName(typeExpr)
}

// parseColumn processes a single PostgreSQL column definition.
func (p *PostgreSQL) parseColumn(def string) (sqlmapper.Column, error) {
	parts := strings.Fields(def)
	if len(parts) < 2 {
		return sqlmapper.Column{}, fmt.Errorf("invalid column definition: %s", def)
	}

	column := sqlmapper.Column{
		Name:       strings.Trim(parts[0], `"`),
		IsNullable: true,
	}

	defUpper := keyword.UpperASCII(def)

	rest := strings.TrimSpace(def[len(parts[0]):])
	applyPGType(&column, takeUntilStopWord(rest, pgTypeStopWords))

	// A serial is an integer column with a sequence attached. Recording it as
	// "bigserial" would leave the shared model holding PostgreSQL's own spelling
	// for the thing MySQL records as bigint plus AutoIncrement, so the two sides
	// are folded onto the same representation here.
	switch column.DataType {
	case "serial":
		column.DataType, column.AutoIncrement = "int", true
	case "bigserial":
		column.DataType, column.AutoIncrement = "bigint", true
	case "smallserial":
		column.DataType, column.AutoIncrement = "smallint", true
	}

	// NOT NULL
	if strings.Contains(defUpper, "NOT NULL") {
		column.IsNullable = false
	}

	// PRIMARY KEY (inline)
	if strings.Contains(defUpper, "PRIMARY KEY") {
		column.IsPrimaryKey = true
		column.IsNullable = false
	}

	// UNIQUE (inline)
	if strings.Contains(defUpper, "UNIQUE") {
		column.IsUnique = true
	}

	// GENERATED ... AS IDENTITY behaves like AUTO_INCREMENT elsewhere.
	if strings.Contains(defUpper, "AS IDENTITY") {
		column.AutoIncrement = true
	}

	// CHECK (inline)
	if strings.Contains(defUpper, "CHECK") {
		if m := pgCheckRe.FindStringSubmatch(def); len(m) > 1 {
			column.CheckExpression = m[1]
		}
	}

	// DEFAULT value
	if idx := strings.Index(defUpper, "DEFAULT"); idx >= 0 {
		defaultPart := takeUntilStopWord(strings.TrimSpace(def[idx+len("DEFAULT"):]), pgDefaultStopWords)
		defaultPart = strings.TrimSuffix(strings.TrimSpace(defaultPart), ",")
		up := strings.ToUpper(defaultPart)

		switch {
		case strings.HasPrefix(up, "NEXTVAL("):
			// Column is driven by a sequence; downstream dialects express this
			// as SERIAL / AUTO_INCREMENT rather than a literal default.
			column.AutoIncrement = true
		case up == "NULL" || defaultPart == "":
			// Implicit default, nothing to carry over.
		case strings.Contains(up, "CURRENT_TIMESTAMP") || strings.Contains(up, "NOW()"):
			column.DefaultValue = "CURRENT_TIMESTAMP"
		case strings.HasPrefix(defaultPart, "'"):
			if m := regexp.MustCompile(`'([^']*)'`).FindStringSubmatch(defaultPart); len(m) > 1 {
				column.DefaultValue = m[1]
			}
		default:
			column.DefaultValue = strings.TrimSpace(pgCastRe.ReplaceAllString(defaultPart, ""))
		}
	}

	return column, nil
}

func (p *PostgreSQL) parseConstraint(def string) (sqlmapper.Constraint, error) {
	c := sqlmapper.Constraint{}

	if strings.HasPrefix(strings.ToUpper(def), "CONSTRAINT") {
		if m := pgConstraintRe.FindStringSubmatch(def); len(m) > 2 {
			c.Name = m[1]
			def = m[2]
		}
	}

	defUpper := strings.ToUpper(def)

	switch {
	case strings.Contains(defUpper, "PRIMARY KEY"):
		c.Type = "PRIMARY KEY"
		if m := pgPKRe.FindStringSubmatch(def); len(m) > 1 {
			c.Columns = splitAndTrim(m[1])
		}

	case strings.Contains(defUpper, "FOREIGN KEY"):
		c.Type = "FOREIGN KEY"
		if m := pgFKRe.FindStringSubmatch(def); len(m) > 3 {
			c.Columns = splitAndTrim(m[1])
			c.RefTable = stripSchemaPrefix(m[2])
			c.RefColumns = splitAndTrim(m[3])
		}
		if strings.Contains(defUpper, "ON DELETE") {
			switch {
			case strings.Contains(defUpper, "ON DELETE CASCADE"):
				c.DeleteRule = "CASCADE"
			case strings.Contains(defUpper, "ON DELETE SET NULL"):
				c.DeleteRule = "SET NULL"
			case strings.Contains(defUpper, "ON DELETE RESTRICT"):
				c.DeleteRule = "RESTRICT"
			case strings.Contains(defUpper, "ON DELETE NO ACTION"):
				c.DeleteRule = "NO ACTION"
			case strings.Contains(defUpper, "ON DELETE SET DEFAULT"):
				c.DeleteRule = "SET DEFAULT"
			}
		}
		if strings.Contains(defUpper, "ON UPDATE") {
			switch {
			case strings.Contains(defUpper, "ON UPDATE CASCADE"):
				c.UpdateRule = "CASCADE"
			case strings.Contains(defUpper, "ON UPDATE SET NULL"):
				c.UpdateRule = "SET NULL"
			case strings.Contains(defUpper, "ON UPDATE RESTRICT"):
				c.UpdateRule = "RESTRICT"
			case strings.Contains(defUpper, "ON UPDATE NO ACTION"):
				c.UpdateRule = "NO ACTION"
			}
		}

	case strings.HasPrefix(defUpper, "UNIQUE"):
		c.Type = "UNIQUE"
		if m := pgUniqueRe.FindStringSubmatch(def); len(m) > 1 {
			c.Columns = splitAndTrim(m[1])
		}

	case strings.Contains(defUpper, "CHECK"):
		c.Type = "CHECK"
		if m := pgCheckRe.FindStringSubmatch(def); len(m) > 1 {
			c.CheckExpression = m[1]
		}
	}

	return c, nil
}

func (p *PostgreSQL) parseIndexes(content string) error {
	for _, m := range pgIndexRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 6 {
			continue
		}
		isUnique := strings.TrimSpace(m[1]) != ""
		indexName := m[2]
		tableName := m[3]
		indexType := m[4]
		columns := splitAndTrim(m[5])
		condition := ""
		if len(m) > 6 {
			condition = m[6]
		}

		for i, table := range p.schema.Tables {
			if table.Name == tableName || fmt.Sprintf("%s.%s", table.Schema, table.Name) == tableName {
				p.schema.Tables[i].Indexes = append(p.schema.Tables[i].Indexes, sqlmapper.Index{
					Name:      indexName,
					Columns:   columns,
					IsUnique:  isUnique,
					Type:      indexType,
					Condition: condition,
				})
				break
			}
		}
	}
	return nil
}

func (p *PostgreSQL) parseViews(content string) error {
	for _, m := range pgViewRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		view := sqlmapper.View{Definition: m[2]}
		if parts := strings.Split(m[1], "."); len(parts) > 1 {
			view.Schema = parts[0]
			view.Name = parts[1]
		} else {
			view.Name = m[1]
		}
		p.schema.Views = append(p.schema.Views, view)
	}

	for _, m := range pgMatViewRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		view := sqlmapper.View{Definition: m[2], IsMaterialized: true}
		if parts := strings.Split(m[1], "."); len(parts) > 1 {
			view.Schema = parts[0]
			view.Name = parts[1]
		} else {
			view.Name = m[1]
		}
		p.schema.Views = append(p.schema.Views, view)
	}
	return nil
}

// pgDollarBody splits what follows RETURNS into the attributes before the body,
// the body itself, and the attributes after it.
//
// The tag is matched by hand rather than by regexp: Go's engine has no
// backreferences, so $function$ ... $function$ cannot be expressed as a
// pattern, and pg_dump reaches for a tag whenever the body contains $$.
func pgDollarBody(rest string) (body, before, after string, ok bool) {
	open := strings.Index(rest, "$")
	if open == -1 {
		return "", "", "", false
	}
	close := strings.Index(rest[open+1:], "$")
	if close == -1 {
		return "", "", "", false
	}
	tag := rest[open : open+close+2]
	if strings.ContainsAny(tag[1:len(tag)-1], " \t\n") {
		return "", "", "", false
	}

	bodyStart := open + len(tag)
	end := strings.Index(rest[bodyStart:], tag)
	if end == -1 {
		return "", "", "", false
	}

	before = rest[:open]
	body = rest[bodyStart : bodyStart+end]
	after = rest[bodyStart+end+len(tag):]
	if i := strings.Index(after, ";"); i != -1 {
		after = after[:i]
	}
	return body, before, after, true
}

// pgReturnType takes the declared type off the front of what follows RETURNS,
// leaving the attributes behind.
func pgReturnType(rest string) string {
	if loc := pgReturnStopRe.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	return strings.Join(strings.Fields(rest), " ")
}

// pgLanguageOf reads the language out of a function's attributes, defaulting to
// the one PostgreSQL assumes for a body it cannot otherwise place.
func pgLanguageOf(attrs string) string {
	if m := pgLanguageRe.FindStringSubmatch(attrs); len(m) > 1 {
		return m[1]
	}
	return "plpgsql"
}

func (p *PostgreSQL) parseFunctions(content string) error {
	for _, loc := range pgFuncRe.FindAllStringSubmatchIndex(content, -1) {
		name := content[loc[2]:loc[3]]
		params := content[loc[4]:loc[5]]

		body, before, after, ok := pgDollarBody(content[loc[1]:])
		if !ok {
			// A function with no dollar-quoted body is one this parser cannot
			// read; skipping it is better than recording an empty one.
			continue
		}

		fn := sqlmapper.Function{
			Returns:  pgReturnType(before),
			Body:     body,
			Language: pgLanguageOf(before + " " + after),
		}
		if parts := strings.Split(strings.Trim(name, `"`), "."); len(parts) > 1 {
			fn.Schema = parts[0]
			fn.Name = parts[1]
		} else {
			fn.Name = name
		}
		if params != "" {
			for _, param := range strings.Split(params, ",") {
				pp := strings.Fields(strings.TrimSpace(param))
				if len(pp) >= 2 {
					fn.Parameters = append(fn.Parameters, sqlmapper.Parameter{Name: pp[0], DataType: pp[1]})
				}
			}
		}
		p.schema.Functions = append(p.schema.Functions, fn)
	}

	for _, m := range pgProcRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 5 {
			continue
		}
		fn := sqlmapper.Function{Language: m[3], Body: m[4], IsProc: true}
		if parts := strings.Split(m[1], "."); len(parts) > 1 {
			fn.Schema = parts[0]
			fn.Name = parts[1]
		} else {
			fn.Name = m[1]
		}
		if m[2] != "" {
			for _, param := range strings.Split(m[2], ",") {
				pp := strings.Fields(strings.TrimSpace(param))
				if len(pp) >= 2 {
					fn.Parameters = append(fn.Parameters, sqlmapper.Parameter{Name: pp[0], DataType: pp[1]})
				}
			}
		}
		p.schema.Functions = append(p.schema.Functions, fn)
	}
	return nil
}

func (p *PostgreSQL) parseTriggers(content string) error {
	for _, m := range pgTriggerRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 6 {
			continue
		}
		trig := sqlmapper.Trigger{
			Name: m[1], Timing: m[2], Event: m[3],
			Body: m[5], ForEachRow: strings.Contains(m[0], "FOR EACH ROW"),
		}
		if parts := strings.Split(m[4], "."); len(parts) > 1 {
			trig.Schema = parts[0]
			trig.Table = parts[1]
		} else {
			trig.Table = m[4]
		}
		p.schema.Triggers = append(p.schema.Triggers, trig)
	}

	for _, m := range pgCondTrigRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 6 {
			continue
		}
		trig := sqlmapper.Trigger{
			Name: m[1], Timing: m[2], Condition: m[4],
			Body: m[5], ForEachRow: strings.Contains(m[0], "FOR EACH ROW"),
		}
		if parts := strings.Split(m[3], "."); len(parts) > 1 {
			trig.Schema = parts[0]
			trig.Table = parts[1]
		} else {
			trig.Table = m[3]
		}
		p.schema.Triggers = append(p.schema.Triggers, trig)
	}
	return nil
}

func (p *PostgreSQL) parsePermissions(content string) error {
	for _, m := range pgGrantRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 4 {
			continue
		}
		privs := splitAndTrim(m[1])
		p.schema.Permissions = append(p.schema.Permissions, sqlmapper.Permission{
			Type: "GRANT", Privileges: privs, Object: m[2], Grantee: m[3],
			WithGrant: strings.Contains(m[0], "WITH GRANT OPTION"),
		})
	}
	for _, m := range pgGrantAllRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		p.schema.Permissions = append(p.schema.Permissions, sqlmapper.Permission{
			Type: "GRANT", Privileges: []string{"ALL PRIVILEGES"}, Object: m[1], Grantee: m[2],
			WithGrant: strings.Contains(m[0], "WITH GRANT OPTION"),
		})
	}
	for _, m := range pgGrantExecRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 3 {
			continue
		}
		p.schema.Permissions = append(p.schema.Permissions, sqlmapper.Permission{
			Type: "GRANT", Privileges: []string{"EXECUTE"}, Object: m[1], Grantee: m[2],
			WithGrant: strings.Contains(m[0], "WITH GRANT OPTION"),
		})
	}
	for _, m := range pgRevokeRe.FindAllStringSubmatch(content, -1) {
		if len(m) < 4 {
			continue
		}
		privs := splitAndTrim(m[1])
		p.schema.Permissions = append(p.schema.Permissions, sqlmapper.Permission{
			Type: "REVOKE", Privileges: privs, Object: m[2], Grantee: m[3],
		})
	}
	return nil
}

// generateTableSQL creates a CREATE TABLE statement, applying type mapping and
// outputting all constraints (PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK).
func (p *PostgreSQL) generateTableSQL(table sqlmapper.Table, deferred []sqlmapper.Constraint) string {
	var result strings.Builder

	result.WriteString(fmt.Sprintf("CREATE TABLE %s (\n", table.Name))

	// A single-column PK on an auto-increment column reads better inline; every
	// other PK is emitted as a table-level constraint. Exactly one of the two
	// must fire, otherwise PostgreSQL rejects the table with two primary keys.
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
		case "CHECK":
			// MariaDB stores JSON as LONGTEXT and guards it with a
			// CHECK (json_valid(col)). The column maps to a native jsonb here,
			// which validates itself, and PostgreSQL has no json_valid function,
			// so carrying the check over would only break the load.
			if expr.IsJSONGuardSQL(c.CheckExpression) {
				continue
			}
			tableConstraints = append(tableConstraints, c)
		case "UNIQUE":
			tableConstraints = append(tableConstraints, c)
		}
	}

	// Columns flagged as PK without a matching constraint (inline "id serial
	// PRIMARY KEY" in the source) still need the marker.
	if len(inlinePKCols) == 0 && len(tableConstraints) == len(table.Constraints) {
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
		result.WriteString("    ")
		result.WriteString(p.generateColumnSQL(col, table.Name, inlinePKCols[col.Name]))
		if i < totalItems-1 {
			result.WriteString(",")
		}
		result.WriteString("\n")
	}

	for i, c := range tableConstraints {
		result.WriteString("    ")
		result.WriteString(p.generateConstraintSQL(c))
		if len(table.Columns)+i < totalItems-1 {
			result.WriteString(",")
		}
		result.WriteString("\n")
	}

	result.WriteString(")")
	if table.TableSpace != "" {
		result.WriteString(" TABLESPACE ")
		result.WriteString(table.TableSpace)
	}
	result.WriteString(";")
	return result.String()
}

// generateColumnSQL creates the SQL for a single PostgreSQL column, applying type mapping.
// inlinePK reports whether this column should carry the PRIMARY KEY marker itself;
// when the table emits an explicit PK constraint it must not, or PostgreSQL sees two.
func (p *PostgreSQL) generateColumnSQL(col sqlmapper.Column, tableName string, inlinePK bool) string {
	var parts []string
	parts = append(parts, col.Name)

	pgType := p.resolveType(col, tableName)
	if col.IsArray {
		pgType += "[]"
	}
	parts = append(parts, pgType)

	isSerialType := strings.HasSuffix(strings.ToLower(pgType), "serial")

	if inlinePK {
		parts = append(parts, "PRIMARY KEY")
	} else {
		if !col.IsNullable && !isSerialType && !col.AutoIncrement {
			parts = append(parts, "NOT NULL")
		}
		if col.IsUnique {
			parts = append(parts, "UNIQUE")
		}
	}

	if col.DefaultValue != "" && !isSerialType && !col.AutoIncrement {
		dv := col.DefaultValue
		// MySQL and SQL Server spell a boolean default as 1 or 0. PostgreSQL
		// rejects that outright: "column is of type boolean but default
		// expression is of type integer".
		if strings.HasPrefix(strings.ToUpper(pgType), "BOOLEAN") {
			switch strings.ToLower(strings.Trim(dv, "'")) {
			case "1", "true", "t", "y", "yes":
				dv = "true"
			case "0", "false", "f", "n", "no":
				dv = "false"
			}
		}
		if strings.ContainsAny(dv, "()") {
			parts = append(parts, "DEFAULT", expr.Value(dv, expr.PostgreSQL))
		} else if strings.ToUpper(dv) == "CURRENT_TIMESTAMP" || dv == "true" || dv == "false" {
			parts = append(parts, "DEFAULT", dv)
		} else if isNumeric(dv) {
			parts = append(parts, "DEFAULT", dv)
		} else {
			parts = append(parts, "DEFAULT", fmt.Sprintf("'%s'", dv))
		}
	}

	return strings.Join(parts, " ")
}

// resolveType maps a column's DataType to the PostgreSQL equivalent.
// booleanColumns names the columns this generator will declare as boolean.
//
// PostgreSQL is the only target with a strict boolean, so it is the only one
// that rejects "WHERE is_active" when the column is an integer. A source with
// no boolean type of its own, SQLite for instance, gives it exactly that.
func (p *PostgreSQL) booleanColumns(schema *sqlmapper.Schema) map[string]bool {
	out := make(map[string]bool)
	for _, table := range schema.Tables {
		for _, col := range table.Columns {
			name := strings.ToLower(strings.TrimSpace(p.resolveType(col, table.Name)))
			if name == "boolean" || name == "bool" {
				out[strings.ToLower(col.Name)] = true
			}
		}
	}
	return out
}

func (p *PostgreSQL) resolveType(col sqlmapper.Column, tableName string) string {
	lower := strings.ToLower(col.DataType)

	// ENUM with values → use the custom type we declared
	if lower == "enum" && len(col.EnumValues) > 0 {
		return fmt.Sprintf("%s_%s_enum", tableName, col.Name)
	}

	// AUTO_INCREMENT columns use SERIAL variants
	if col.AutoIncrement {
		switch lower {
		case "bigint", "bigserial":
			return "BIGSERIAL"
		case "smallint", "smallserial":
			return "SMALLSERIAL"
		default:
			return "SERIAL"
		}
	}

	// serial/bigserial/smallserial without AutoIncrement flag (e.g. from test schemas)
	switch lower {
	case "serial":
		return "SERIAL"
	case "bigserial":
		return "BIGSERIAL"
	case "smallserial":
		return "SMALLSERIAL"
	}

	// Types that embed their own length (from MySQL type map like "varchar(36)")
	if strings.Contains(lower, "(") {
		return strings.ToUpper(lower)
	}

	// Fixed-length types that never take parentheses in PostgreSQL
	noLength := map[string]bool{
		"text": true, "bytea": true, "jsonb": true, "json": true,
		"boolean": true, "smallint": true, "integer": true,
		"bigint": true, "real": true, "double precision": true,
		"timestamp": true, "timestamp with time zone": true,
		"date": true, "time": true, "uuid": true, "inet": true,
		"cidr": true, "macaddr": true, "interval": true,
	}

	if mapped, ok := toPostgresType[lower]; ok {
		t := mapped
		if col.Length > 0 && !noLength[t] {
			if col.Scale > 0 {
				t = fmt.Sprintf("%s(%d,%d)", t, col.Length, col.Scale)
			} else {
				t = fmt.Sprintf("%s(%d)", t, col.Length)
			}
		}
		return strings.ToUpper(t)
	}

	// Fallback
	if col.Length > 0 {
		if col.Scale > 0 {
			return fmt.Sprintf("%s(%d,%d)", col.DataType, col.Length, col.Scale)
		}
		return fmt.Sprintf("%s(%d)", col.DataType, col.Length)
	}
	return col.DataType
}

func (p *PostgreSQL) generateConstraintSQL(c sqlmapper.Constraint) string {
	var sb strings.Builder
	if c.Name != "" {
		sb.WriteString(fmt.Sprintf("CONSTRAINT %s ", c.Name))
	}
	switch c.Type {
	case "PRIMARY KEY":
		sb.WriteString(fmt.Sprintf("PRIMARY KEY (%s)", strings.Join(c.Columns, ", ")))
	case "FOREIGN KEY":
		sb.WriteString(fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s (%s)",
			strings.Join(c.Columns, ", "), c.RefTable, strings.Join(c.RefColumns, ", ")))
		if c.DeleteRule != "" {
			sb.WriteString(" ON DELETE ")
			sb.WriteString(c.DeleteRule)
		}
		if c.UpdateRule != "" {
			sb.WriteString(" ON UPDATE ")
			sb.WriteString(c.UpdateRule)
		}
	case "UNIQUE":
		sb.WriteString(fmt.Sprintf("UNIQUE (%s)", strings.Join(c.Columns, ", ")))
	case "CHECK":
		sb.WriteString(fmt.Sprintf("CHECK (%s)", expr.Condition(c.CheckExpression, expr.PostgreSQL)))
	}
	return sb.String()
}

func (p *PostgreSQL) generateIndexSQL(tableName string, index sqlmapper.Index) string {
	var sb strings.Builder
	if index.IsUnique {
		sb.WriteString("CREATE UNIQUE INDEX ")
	} else {
		sb.WriteString("CREATE INDEX ")
	}
	sb.WriteString(index.Name)
	sb.WriteString(" ON ")
	sb.WriteString(tableName)
	if index.Type != "" {
		sb.WriteString(" USING ")
		sb.WriteString(index.Type)
	}
	sb.WriteString("(")
	sb.WriteString(strings.Join(index.Columns, ", "))
	sb.WriteString(")")
	if index.Condition != "" {
		sb.WriteString(" WHERE ")
		sb.WriteString(index.Condition)
	}
	if index.TableSpace != "" {
		sb.WriteString(" TABLESPACE ")
		sb.WriteString(index.TableSpace)
	}
	sb.WriteString(";")
	return sb.String()
}

// generateTypeSQL generates SQL for a PostgreSQL type definition.
func (p *PostgreSQL) generateTypeSQL(typ sqlmapper.Type) string {
	if typ.Kind == "ENUM" {
		return fmt.Sprintf("CREATE TYPE %s AS ENUM (%s);", typ.Name, typ.Definition)
	}
	return fmt.Sprintf("CREATE TYPE %s AS (%s);", typ.Name, typ.Definition)
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

// stripSchemaPrefix reduces "schema.table" to "table". Schema qualification does
// not survive a hop to MySQL or SQLite, and the bare name is what every dialect
// resolves through its own default search path.
func stripSchemaPrefix(name string) string {
	name = strings.TrimSpace(strings.Trim(name, `"`))
	if parts := strings.Split(name, "."); len(parts) > 1 {
		return strings.Trim(parts[len(parts)-1], `"`)
	}
	return name
}

// columnIsAutoIncrement reports whether the named column of table is an
// auto-increment / serial column.
func columnIsAutoIncrement(table sqlmapper.Table, name string) bool {
	for _, col := range table.Columns {
		if col.Name == name {
			return col.AutoIncrement
		}
	}
	return false
}

// splitAndTrim splits a comma-separated string and trims whitespace from each element.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// isNumeric reports whether s looks like a bare number.
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
// PostgreSQL statements.
func (p *PostgreSQL) routines(schema *sqlmapper.Schema) []string {
	var out []string

	for _, fn := range schema.Functions {
		if fn.IsProc {
			out = append(out, fmt.Sprintf("CREATE PROCEDURE %s(%s) LANGUAGE %s AS $$%s$$",
				fn.Name, routine.Params(fn.Parameters), pgLanguage(fn.Language), pgBody(fn.Body)))
			continue
		}
		out = append(out, fmt.Sprintf("CREATE FUNCTION %s(%s) RETURNS %s AS $$%s$$ LANGUAGE %s",
			fn.Name, routine.Params(fn.Parameters), fn.Returns, pgBody(fn.Body), pgLanguage(fn.Language)))
	}

	for _, pr := range schema.Procedures {
		out = append(out, fmt.Sprintf("CREATE PROCEDURE %s(%s) LANGUAGE %s AS $$%s$$",
			pr.Name, routine.Params(pr.Parameters), pgLanguage(pr.Language), pgBody(pr.Body)))
	}

	for _, tr := range schema.Triggers {
		// A PostgreSQL trigger runs a function rather than carrying a body, and
		// the parser records that function's name in the body field. Rendering
		// it as anything else produced EXECUTE FUNCTION BEGIN.
		stmt := sqlfmt.TriggerHeader(tr.Name, tr.Timing, tr.Event, tr.Table, tr.ForEachRow)
		if tr.Condition != "" {
			stmt += "\nWHEN " + tr.Condition
		}
		out = append(out, fmt.Sprintf("%s\nEXECUTE FUNCTION %s()", stmt, pgTriggerFunction(tr.Body)))
	}

	return out
}

// pgLanguage supplies the language a function is written in. Emitting the empty
// string produced "LANGUAGE ;", which no server accepts.
func pgLanguage(lang string) string {
	if strings.TrimSpace(lang) == "" {
		return "plpgsql"
	}
	return lang
}

// pgBody pads the body so the dollar quotes do not run into it.
func pgBody(body string) string {
	return "\n" + strings.TrimSpace(body) + "\n"
}

// pgTriggerFunction takes the function name out of the body field, tolerating a
// value that already carries its parentheses.
//
// The schema qualifier is dropped because the function itself is written out
// unqualified. Keeping it worked only while that schema happened to be public;
// a trigger from any other schema called a function that was never created
// there.
func pgTriggerFunction(body string) string {
	name := strings.TrimSpace(body)
	if i := strings.Index(name, "("); i > 0 {
		name = name[:i]
	}
	if i := strings.LastIndex(name, "."); i != -1 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name)
}

// generateRoutinesSQL renders the routine section of a dump. Generate and
// GenerateStream both call it, because they used to disagree about whether
// routines were written at all.
func (p *PostgreSQL) generateRoutinesSQL(schema *sqlmapper.Schema) string {
	if routine.Count(schema) == 0 {
		return ""
	}
	if !schema.RoutinesAreNativeTo(sqlmapper.PostgreSQL) {
		return routine.ForeignSQL(schema)
	}

	var sb strings.Builder
	for _, stmt := range p.routines(schema) {
		sb.WriteString(sqlfmt.Terminate(stmt, ";"))
		sb.WriteString("\n\n")
	}
	return sb.String()
}

// enumTypesSQL renders the CREATE TYPE a MySQL ENUM column needs, since
// PostgreSQL has no inline enum.
//
// Both Generate and GenerateStream call it. The stream used to write none of
// them, so a streamed MySQL schema produced tables referring to a type that was
// never created.
func (p *PostgreSQL) enumTypesSQL(tables []sqlmapper.Table) []string {
	var out []string
	for _, table := range tables {
		for _, col := range table.Columns {
			if !strings.EqualFold(col.DataType, "enum") || len(col.EnumValues) == 0 {
				continue
			}
			quoted := make([]string, len(col.EnumValues))
			for i, v := range col.EnumValues {
				quoted[i] = fmt.Sprintf("'%s'", v)
			}
			out = append(out, fmt.Sprintf("CREATE TYPE %s_%s_enum AS ENUM (%s);",
				table.Name, col.Name, strings.Join(quoted, ", ")))
		}
	}
	return out
}

// pgComments collects the comments a COMMENT ON states, by table and by column.
func pgComments(content string) (tables map[string]string, columns map[string]map[string]string) {
	tables = make(map[string]string)
	columns = make(map[string]map[string]string)

	for _, m := range pgTableCommentRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 2 {
			tables[m[1]] = m[2]
		}
	}
	for _, m := range pgColCommentRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 3 {
			if columns[m[1]] == nil {
				columns[m[1]] = make(map[string]string)
			}
			columns[m[1]][m[2]] = m[3]
		}
	}
	return tables, columns
}
