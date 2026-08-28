package sqlserver

import (
	"bytes"
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

// SQLServerStreamParser implements the StreamParser interface for SQL Server
type SQLServerStreamParser struct {
	sqlserver *SQLServer
}

// NewSQLServerStreamParser creates a new SQL Server stream parser
// sqlserverStreamIndexRe reads a standalone CREATE INDEX statement, which the whole-file
// parser cannot do because it resolves the target table first.
var sqlserverStreamIndexRe = regexp.MustCompile(`(?i)CREATE\s+(?:(UNIQUE)\s+)?(?:(CLUSTERED|NONCLUSTERED)\s+)?INDEX\s+([.\w\[\]]+)\s+ON\s+([.\w\[\]]+)\s*\((.*?)\)`)

func NewSQLServerStreamParser() *SQLServerStreamParser {
	return &SQLServerStreamParser{
		sqlserver: NewSQLServer().(*SQLServer),
	}
}

// ParseStream implements the StreamParser interface
func (p *SQLServerStreamParser) ParseStream(reader io.Reader, callback func(stream.SchemaObject) error) error {
	streamReader := stream.NewStreamReader(reader, "GO")

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

// ParseStreamParallel implements parallel processing for SQL Server stream parsing
func (p *SQLServerStreamParser) ParseStreamParallel(reader io.Reader, callback func(stream.SchemaObject) error, workers int) error {
	streamReader := stream.NewStreamReader(reader, "GO")
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
func (p *SQLServerStreamParser) parseStatement(statement string) (*stream.SchemaObject, error) {
	upperStatement := dispatchKey(statement)

	switch {
	case keyword.HasPrefix(upperStatement, "CREATE TABLE"):
		table, err := p.parseTableStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.TableObject,
			Data: table,
		}, nil

	case strings.HasPrefix(upperStatement, "CREATE VIEW"):
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

	case strings.HasPrefix(upperStatement, "CREATE PROCEDURE") || strings.HasPrefix(upperStatement, "CREATE PROC"):
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

	case mssIndexHeaderRe.MatchString(upperStatement):
		index, err := p.parseIndexStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.IndexObject,
			Data: index,
		}, nil
	}

	return nil, nil
}

// GenerateStream implements the StreamParser interface
func (p *SQLServerStreamParser) GenerateStream(schema *sqlmapper.Schema, writer io.Writer) error {
	if schema == nil {
		return fmt.Errorf("schema cannot be nil")
	}

	// Write tables
	// Order the tables the same way Generate does, so a streamed schema loads
	// even when the source listed a child table before its parent.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)
	for _, table := range tables {
		stmt := p.sqlserver.generateTableSQL(table, deferredFKs[table.Name])
		if _, err := writer.Write([]byte(stmt + "\nGO\n\n")); err != nil {
			return err
		}

		// Generate indexes for this table
		for _, index := range table.Indexes {
			stmt := p.sqlserver.generateIndexSQL(table.Name, index)
			if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\nGO\n")); err != nil {
				return err
			}
		}
	}

	// Views are rendered by the same code the file generator uses. SQL Server
	// has no boolean, and the stream used to write a body saying WHERE is_active
	// straight through, which does not load there.
	for _, view := range schema.Views {
		stmt := p.sqlserver.generateViewSQL(view)
		if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\nGO\n\n")); err != nil {
			return err
		}
	}

	// Routines are rendered by the same code the file generator uses, so the
	// streamed output and the file output agree.
	if routines := p.sqlserver.generateRoutinesSQL(schema); routines != "" {
		if _, err := writer.Write([]byte(routines)); err != nil {
			return err
		}
	}

	return nil
}

// parseTableStatement parses a CREATE TABLE statement
func (p *SQLServerStreamParser) parseTableStatement(statement string) (*sqlmapper.Table, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &SQLServer{schema: tempSchema}

	if err := localParser.parseTablesFromStatement([]byte(statement)); err != nil {
		return nil, err
	}

	if len(tempSchema.Tables) == 0 {
		return nil, fmt.Errorf("no table found in statement")
	}

	return &tempSchema.Tables[0], nil
}

// parseViewStatement parses a CREATE VIEW statement
func (p *SQLServerStreamParser) parseViewStatement(statement string) (*sqlmapper.View, error) {
	// The file parser's reader is used, not a second one of the stream's own.
	// The stream had its own and it left the brackets on the name, so a real
	// SSMS script produced a view called [active_customers.
	localParser := &SQLServer{schema: &sqlmapper.Schema{}}

	view, err := localParser.parseCreateView([]byte(statement))
	if err != nil {
		return nil, err
	}
	// The whole-file parser records whatever it can and moves on. A stream
	// hands back one object per statement, so a statement it could not read has
	// to be reported rather than returned empty.
	if view.Name == "" || view.Definition == "" {
		return nil, fmt.Errorf("no view found in statement")
	}
	tempSchema := &sqlmapper.Schema{Views: []sqlmapper.View{view}}

	return &tempSchema.Views[0], nil
}

// parseFunctionStatement parses a CREATE FUNCTION statement
func (p *SQLServerStreamParser) parseFunctionStatement(statement string) (*sqlmapper.Function, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &SQLServer{schema: tempSchema}

	if err := localParser.parseFunctions(ensureTerminated(statement)); err != nil {
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
func (p *SQLServerStreamParser) parseProcedureStatement(statement string) (*sqlmapper.Procedure, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &SQLServer{schema: tempSchema}

	if err := localParser.parseFunctions(ensureTerminated(statement)); err != nil {
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
func (p *SQLServerStreamParser) parseTriggerStatement(statement string) (*sqlmapper.Trigger, error) {
	// The file parser's reader is used, not a second one of the stream's own.
	localParser := &SQLServer{schema: &sqlmapper.Schema{}}

	trigger, err := localParser.parseCreateTrigger([]byte(statement))
	if err != nil {
		return nil, err
	}
	if trigger.Name == "" {
		return nil, fmt.Errorf("no trigger found in statement")
	}

	return &trigger, nil
}

// parseIndexStatement parses a CREATE INDEX statement
func (p *SQLServerStreamParser) parseIndexStatement(statement string) (*sqlmapper.Index, error) {
	// parseIndexes attaches the index to a table that is already present in the
	// schema, which never holds for a single statement read off the stream, so
	// the statement is read directly here.
	m := sqlserverStreamIndexRe.FindStringSubmatch(ensureTerminated(statement))
	if len(m) < 6 {
		return nil, fmt.Errorf("no index found in statement")
	}

	index := &sqlmapper.Index{
		Name:        strings.Trim(m[3], "[]"),
		Columns:     (&SQLServer{}).splitAndTrim(m[5]),
		IsUnique:    strings.TrimSpace(m[1]) != "",
		IsClustered: strings.EqualFold(strings.TrimSpace(m[2]), "CLUSTERED"),
	}
	return index, nil
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
// SQL Server writes CREATE OR ALTER, not CREATE OR REPLACE, and has done since
// 2016 SP1. Folding the form it does not use left a CREATE OR ALTER VIEW
// unrecognised.
var orReplaceRe = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+(?:ALTER|REPLACE))?\s+`)

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
func (p *SQLServerStreamParser) parseConstraintStatement(statement string) (*sqlmapper.Constraint, error) {
	// The whole-file parser normalises every statement as it splits them, and
	// the stream did not. SSMS writes "ADD  CONSTRAINT" with two spaces, which
	// the branch looking for "ADD CONSTRAINT" does not match, so the statement
	// was read as a column definition instead.
	stmt := normalizeSQLServerDDL(statement)

	// "ALTER TABLE x CHECK CONSTRAINT y" only re-enables a constraint that was
	// already declared; there is nothing in it to parse.
	if mssCheckOnlyRe.MatchString(stmt) {
		return nil, fmt.Errorf("no constraint found in statement")
	}

	localParser := &SQLServer{schema: &sqlmapper.Schema{}, buf: bytes.NewBuffer(nil)}

	if err := localParser.parseAlterTable([]byte(stmt)); err != nil {
		return nil, err
	}

	if len(localParser.schema.Tables) == 0 {
		return nil, fmt.Errorf("no constraint found in statement")
	}
	constraints := localParser.schema.Tables[0].Constraints
	if len(constraints) == 0 {
		return nil, fmt.Errorf("no constraint found in statement")
	}
	return &constraints[0], nil
}
