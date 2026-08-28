package postgres

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/internal/keyword"
	"github.com/mstgnz/sqlmapper/internal/sqlfmt"
	"github.com/mstgnz/sqlmapper/stream"
)

// PostgreSQLStreamParser implements the StreamParser interface for PostgreSQL
type PostgreSQLStreamParser struct {
	postgres *PostgreSQL
}

// NewPostgreSQLStreamParser creates a new PostgreSQL stream parser
func NewPostgreSQLStreamParser() *PostgreSQLStreamParser {
	return &PostgreSQLStreamParser{
		postgres: NewPostgreSQL().(*PostgreSQL),
	}
}

// ParseStream implements the StreamParser interface
func (p *PostgreSQLStreamParser) ParseStream(reader io.Reader, callback func(stream.SchemaObject) error) error {
	streamReader := stream.NewStreamReader(reader, ";")

	for {
		statement, err := streamReader.ReadStatement()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading statement: %v", err)
		}

		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}

		// One dispatch, shared with ParseStreamParallel. There used to be two
		// of them, an if chain here and the switch there, and they drifted:
		// constraints reported by one were dropped by the other.
		obj, err := p.parseStatement(statement)
		if err != nil {
			return err
		}
		if obj == nil {
			continue
		}
		if err := callback(*obj); err != nil {
			return err
		}
	}

	return nil
}

// ParseStreamParallel implements parallel processing for PostgreSQL stream parsing
func (p *PostgreSQLStreamParser) ParseStreamParallel(reader io.Reader, callback func(stream.SchemaObject) error, workers int) error {
	streamReader := stream.NewStreamReader(reader, ";")
	statements := make(chan string, workers)
	results := make(chan stream.SchemaObject, workers)
	errors := make(chan error, workers)
	var wg sync.WaitGroup

	// Start worker goroutines
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for statement := range statements {
				obj, err := p.parseStatement(statement)
				if err != nil {
					errors <- err
					return
				}
				if obj != nil {
					results <- *obj
				}
			}
		}()
	}

	// Start a goroutine to close results channel after all workers are done
	go func() {
		wg.Wait()
		close(results)
	}()

	// Start a goroutine to read statements and send them to workers
	go func() {
		for {
			statement, err := streamReader.ReadStatement()
			if err == io.EOF {
				break
			}
			if err != nil {
				errors <- fmt.Errorf("error reading statement: %v", err)
				break
			}

			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			statements <- statement
		}
		close(statements)
	}()

	// Process results and handle errors
	for obj := range results {
		if err := callback(obj); err != nil {
			return err
		}
	}

	// Check for any errors from workers
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}

// parseStatement parses a single SQL statement and returns a SchemaObject
func (p *PostgreSQLStreamParser) parseStatement(statement string) (*stream.SchemaObject, error) {
	upperStatement := dispatchKey(statement)

	switch {
	case strings.HasPrefix(upperStatement, "CREATE TYPE"):
		typ, err := p.parseTypeStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.TypeObject,
			Data: typ,
		}, nil

	case keyword.HasPrefix(upperStatement, "CREATE TABLE"):
		table, err := p.parseTableStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.TableObject,
			Data: table,
		}, nil

	case strings.HasPrefix(upperStatement, "CREATE VIEW"),
		strings.HasPrefix(upperStatement, "CREATE MATERIALIZED VIEW"):
		view, err := p.parseViewStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.ViewObject,
			Data: view,
		}, nil

	case strings.HasPrefix(upperStatement, "CREATE FUNCTION"):
		function, err := p.parseFunctionStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.FunctionObject,
			Data: function,
		}, nil

	case strings.HasPrefix(upperStatement, "CREATE PROCEDURE"):
		procedure, err := p.parseProcedureStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.ProcedureObject,
			Data: procedure,
		}, nil

	case strings.HasPrefix(upperStatement, "CREATE TRIGGER"):
		trigger, err := p.parseTriggerStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.TriggerObject,
			Data: trigger,
		}, nil

	case strings.HasPrefix(upperStatement, "CREATE INDEX"),
		strings.HasPrefix(upperStatement, "CREATE UNIQUE INDEX"):
		index, err := p.parseIndexStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.IndexObject,
			Data: index,
		}, nil

	case strings.HasPrefix(upperStatement, "ALTER TABLE"):
		constraint, err := p.parseConstraintStatement(statement)
		if err != nil {
			// An ALTER TABLE that adds no constraint is not this parser's
			// business, and is no reason to end the stream.
			return nil, nil
		}
		return &stream.SchemaObject{
			Type: stream.ConstraintObject,
			Data: constraint,
		}, nil

	case strings.HasPrefix(upperStatement, "GRANT"),
		strings.HasPrefix(upperStatement, "REVOKE"):
		permission, err := p.parsePermissionStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.PermissionObject,
			Data: permission,
		}, nil
	}

	return nil, nil
}

// parseTypeStatement parses a CREATE TYPE statement
func (p *PostgreSQLStreamParser) parseTypeStatement(statement string) (*sqlmapper.Type, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &PostgreSQL{schema: tempSchema}

	if err := localParser.parseTypes(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	if len(tempSchema.Types) == 0 {
		return nil, fmt.Errorf("no type found in statement")
	}

	return &tempSchema.Types[0], nil
}

// parseTableStatement parses a CREATE TABLE statement
func (p *PostgreSQLStreamParser) parseTableStatement(statement string) (*sqlmapper.Table, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &PostgreSQL{schema: tempSchema}

	if err := localParser.parseTables(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	if len(tempSchema.Tables) == 0 {
		return nil, fmt.Errorf("no table found in statement")
	}

	return &tempSchema.Tables[0], nil
}

// parseViewStatement parses a CREATE VIEW statement
func (p *PostgreSQLStreamParser) parseViewStatement(statement string) (*sqlmapper.View, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &PostgreSQL{schema: tempSchema}

	if err := localParser.parseViews(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	if len(tempSchema.Views) == 0 {
		return nil, fmt.Errorf("no view found in statement")
	}

	return &tempSchema.Views[0], nil
}

// parseFunctionStatement parses a CREATE FUNCTION statement
func (p *PostgreSQLStreamParser) parseFunctionStatement(statement string) (*sqlmapper.Function, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &PostgreSQL{schema: tempSchema}

	if err := localParser.parseFunctions(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	if len(tempSchema.Functions) == 0 {
		return nil, fmt.Errorf("no function found in statement")
	}

	for _, fn := range tempSchema.Functions {
		if !fn.IsProc {
			return &fn, nil
		}
	}

	return nil, fmt.Errorf("no function found in statement")
}

// parseProcedureStatement parses a CREATE PROCEDURE statement
func (p *PostgreSQLStreamParser) parseProcedureStatement(statement string) (*sqlmapper.Procedure, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &PostgreSQL{schema: tempSchema}

	if err := localParser.parseFunctions(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	if len(tempSchema.Functions) == 0 {
		return nil, fmt.Errorf("no procedure found in statement")
	}

	for _, fn := range tempSchema.Functions {
		if fn.IsProc {
			proc := &sqlmapper.Procedure{
				Name:       fn.Name,
				Parameters: fn.Parameters,
				Body:       fn.Body,
				Schema:     fn.Schema,
			}
			return proc, nil
		}
	}

	return nil, fmt.Errorf("no procedure found in statement")
}

// parseTriggerStatement parses a CREATE TRIGGER statement
func (p *PostgreSQLStreamParser) parseTriggerStatement(statement string) (*sqlmapper.Trigger, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &PostgreSQL{schema: tempSchema}

	if err := localParser.parseTriggers(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	if len(tempSchema.Triggers) == 0 {
		return nil, fmt.Errorf("no trigger found in statement")
	}

	return &tempSchema.Triggers[0], nil
}

// parseIndexStatement parses a CREATE INDEX statement
func (p *PostgreSQLStreamParser) parseIndexStatement(statement string) (*sqlmapper.Index, error) {
	// parseIndexes attaches the index to a table that is already present in the
	// schema, which never holds for a single statement read off the stream, so
	// the statement is read directly here.
	localParser := &PostgreSQL{schema: &sqlmapper.Schema{}}

	m := pgIndexRe.FindStringSubmatch(localParser.normalizeContent(ensureTerminated(statement)))
	if len(m) < 6 {
		return nil, fmt.Errorf("no index found in statement")
	}

	index := &sqlmapper.Index{
		Name:     m[2],
		Columns:  splitAndTrim(m[5]),
		IsUnique: strings.TrimSpace(m[1]) != "",
		Type:     m[4],
	}
	if len(m) > 6 {
		index.Condition = m[6]
	}
	if len(m) > 7 {
		index.TableSpace = m[7]
	}
	return index, nil
}

// parsePermissionStatement parses a GRANT/REVOKE statement
func (p *PostgreSQLStreamParser) parsePermissionStatement(statement string) (*sqlmapper.Permission, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &PostgreSQL{schema: tempSchema}

	if err := localParser.parsePermissions(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	if len(tempSchema.Permissions) == 0 {
		return nil, fmt.Errorf("no permission found in statement")
	}

	return &tempSchema.Permissions[0], nil
}

// GenerateStream implements the StreamParser interface
func (p *PostgreSQLStreamParser) GenerateStream(schema *sqlmapper.Schema, writer io.Writer) error {
	if schema == nil {
		return fmt.Errorf("schema cannot be nil")
	}

	// Write types
	// A definition that came from another dialect is stated rather than emitted,
	// the same way the file generator states it.
	for _, typ := range schema.Types {
		stmt := sqlfmt.Terminate(p.postgres.generateTypeSQL(typ), ";")
		if !schema.TypeIsPortable(typ, sqlmapper.PostgreSQL) {
			stmt = sqlfmt.ForeignType(string(schema.SourceDialect), typ.Name, typ.Kind, typ.Definition)
		}
		if _, err := writer.Write([]byte(stmt + "\n\n")); err != nil {
			return err
		}
	}

	// A MySQL ENUM column needs a type of its own here, and the stream used to
	// write none, so its tables referred to a type that was never created.
	for _, stmt := range p.postgres.enumTypesSQL(schema.Tables) {
		if _, err := writer.Write([]byte(stmt + "\n\n")); err != nil {
			return err
		}
	}

	// Write tables
	// Order the tables the same way Generate does, so a streamed schema loads
	// even when the source listed a child table before its parent.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)

	// The boolean columns are known before anything that filters on them is
	// written: both an index condition and a view body need them.
	booleans := p.postgres.booleanColumns(schema)

	for _, table := range tables {
		stmt := p.postgres.generateTableSQL(table, deferredFKs[table.Name])
		if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n\n")); err != nil {
			return err
		}

		// Generate indexes for this table
		for _, index := range table.Indexes {
			stmt := p.postgres.generateIndexSQL(table.Name, index, booleans)
			if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n")); err != nil {
				return err
			}
		}
	}

	// Foreign keys that close a cycle are added once every table exists.
	for _, table := range tables {
		for _, c := range deferredFKs[table.Name] {
			stmt := fmt.Sprintf("ALTER TABLE %s ADD %s;\n", table.Name, p.postgres.generateConstraintSQL(c))
			if _, err := writer.Write([]byte(stmt)); err != nil {
				return err
			}
		}
	}

	// Views are rendered by the same code the file generator uses, so a body
	// carrying another dialect's schema qualifier is translated here too.
	for _, view := range schema.Views {
		stmt := p.postgres.generateViewSQL(view, booleans)
		if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n\n")); err != nil {
			return err
		}
	}

	// Routines are rendered by the same code the file generator uses. The two
	// used to disagree: this path spliced a trigger body where a function name
	// belongs, and the other wrote no routines at all.
	if routines := p.postgres.generateRoutinesSQL(schema); routines != "" {
		if _, err := writer.Write([]byte(routines)); err != nil {
			return err
		}
	}

	// Grants are rendered by the same code the file generator uses, and come
	// last for the same reason: they name objects that have to exist first.
	if perms := p.postgres.generatePermissionsSQL(schema); perms != "" {
		if _, err := writer.Write([]byte("\n" + perms)); err != nil {
			return err
		}
	}

	return nil
}

// ensureTerminated prepares a single statement handed over by the stream reader
// for the whole-file parsers. Those parsers run after a normalisation pass that
// collapses whitespace and they anchor several patterns on the trailing
// delimiter, both of which a raw statement is missing.
func ensureTerminated(statement string) string {
	s := strings.Join(strings.Fields(statement), " ")
	if s == "" {
		return s
	}
	if strings.HasSuffix(s, ";") {
		return s
	}
	return s + ";"
}

// orReplaceRe matches the optional OR REPLACE that sits between CREATE and the
// object keyword.
var orReplaceRe = regexp.MustCompile(`(?i)^\s*CREATE\s+OR\s+REPLACE\s+`)

// dispatchKey normalises a statement for prefix matching. "CREATE OR REPLACE
// FUNCTION" shifts every fixed prefix, so the optional keywords are folded away
// once here rather than doubling every case in the dispatch.
func dispatchKey(statement string) string {
	return strings.ToUpper(orReplaceRe.ReplaceAllString(strings.TrimSpace(statement), "CREATE "))
}

// parseConstraintStatement reads one ALTER TABLE ... ADD CONSTRAINT.
//
// The whole-file parser attaches the constraint to a table it has already read,
// which a stream cannot do: the table was handed to the caller and forgotten.
// The constraint is reported on its own instead, the way an index is.
func (p *PostgreSQLStreamParser) parseConstraintStatement(statement string) (*sqlmapper.Constraint, error) {
	stmt := ensureTerminated(statement)

	m := pgAlterTableRe.FindStringSubmatch(stmt)
	if m == nil {
		return nil, fmt.Errorf("no constraint found in statement")
	}

	// The parser looks the table up by name, so it has to find one.
	name := strings.Trim(m[1], `"`)
	if parts := strings.Split(name, "."); len(parts) > 1 {
		name = strings.Trim(parts[1], `"`)
	}

	localParser := &PostgreSQL{schema: &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{Name: name}},
	}}
	if err := localParser.parseAlterTableConstraints(stmt); err != nil {
		return nil, err
	}

	constraints := localParser.schema.Tables[0].Constraints
	if len(constraints) == 0 {
		return nil, fmt.Errorf("no constraint found in statement")
	}
	return &constraints[0], nil
}
