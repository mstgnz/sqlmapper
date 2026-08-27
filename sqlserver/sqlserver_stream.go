package sqlserver

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/mstgnz/sqlmapper"
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
		dispatch := dispatchKey(statement)

		// Parse CREATE TABLE statements
		if strings.HasPrefix(dispatch, "CREATE TABLE") {
			table, err := p.parseTableStatement(statement)
			if err != nil {
				return err
			}

			err = callback(stream.SchemaObject{
				Type: stream.TableObject,
				Data: table,
			})
			if err != nil {
				return err
			}
			continue
		}

		// Parse CREATE VIEW statements
		if strings.HasPrefix(dispatch, "CREATE VIEW") {
			view, err := p.parseViewStatement(statement)
			if err != nil {
				return err
			}

			err = callback(stream.SchemaObject{
				Type: stream.ViewObject,
				Data: view,
			})
			if err != nil {
				return err
			}
			continue
		}

		// Parse CREATE FUNCTION statements
		if strings.HasPrefix(dispatch, "CREATE FUNCTION") {
			function, err := p.parseFunctionStatement(statement)
			if err != nil {
				return err
			}

			err = callback(stream.SchemaObject{
				Type: stream.FunctionObject,
				Data: function,
			})
			if err != nil {
				return err
			}
			continue
		}

		// Parse CREATE PROCEDURE statements
		if strings.HasPrefix(dispatch, "CREATE PROCEDURE") ||
			strings.HasPrefix(dispatch, "CREATE PROC") {
			procedure, err := p.parseProcedureStatement(statement)
			if err != nil {
				return err
			}

			err = callback(stream.SchemaObject{
				Type: stream.ProcedureObject,
				Data: procedure,
			})
			if err != nil {
				return err
			}
			continue
		}

		// Parse CREATE TRIGGER statements
		if strings.HasPrefix(dispatch, "CREATE TRIGGER") {
			trigger, err := p.parseTriggerStatement(statement)
			if err != nil {
				return err
			}

			err = callback(stream.SchemaObject{
				Type: stream.TriggerObject,
				Data: trigger,
			})
			if err != nil {
				return err
			}
			continue
		}

		// Parse CREATE INDEX statements
		if strings.HasPrefix(dispatch, "CREATE INDEX") ||
			strings.HasPrefix(dispatch, "CREATE UNIQUE INDEX") ||
			strings.HasPrefix(dispatch, "CREATE CLUSTERED INDEX") ||
			strings.HasPrefix(dispatch, "CREATE NONCLUSTERED INDEX") {
			index, err := p.parseIndexStatement(statement)
			if err != nil {
				return err
			}

			err = callback(stream.SchemaObject{
				Type: stream.IndexObject,
				Data: index,
			})
			if err != nil {
				return err
			}
			continue
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
	case strings.HasPrefix(upperStatement, "CREATE TABLE"):
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

	case strings.HasPrefix(upperStatement, "CREATE INDEX") ||
		strings.HasPrefix(upperStatement, "CREATE UNIQUE INDEX") ||
		strings.HasPrefix(upperStatement, "CREATE CLUSTERED INDEX") ||
		strings.HasPrefix(upperStatement, "CREATE NONCLUSTERED INDEX"):
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
			if _, err := writer.Write([]byte(stmt + "\nGO\n")); err != nil {
				return err
			}
		}
	}

	// Write views
	for _, view := range schema.Views {
		stmt := fmt.Sprintf("CREATE VIEW %s AS\n%s", view.Name, view.Definition)
		if _, err := writer.Write([]byte(stmt + "\nGO\n\n")); err != nil {
			return err
		}
	}

	// Write functions
	for _, function := range schema.Functions {
		if !function.IsProc {
			stmt := fmt.Sprintf("CREATE FUNCTION %s(", function.Name)
			for i, param := range function.Parameters {
				if i > 0 {
					stmt += ", "
				}
				stmt += fmt.Sprintf("@%s %s", param.Name, param.DataType)
			}
			stmt += fmt.Sprintf(")\nRETURNS %s\nAS\nBEGIN\n%s\nEND",
				function.Returns, function.Body)
			if _, err := writer.Write([]byte(stmt + "\nGO\n\n")); err != nil {
				return err
			}
		}
	}

	// Write procedures
	for _, function := range schema.Functions {
		if function.IsProc {
			stmt := fmt.Sprintf("CREATE PROCEDURE %s", function.Name)
			if len(function.Parameters) > 0 {
				stmt += "("
				for i, param := range function.Parameters {
					if i > 0 {
						stmt += ", "
					}
					stmt += fmt.Sprintf("@%s %s", param.Name, param.DataType)
				}
				stmt += ")"
			}
			stmt += fmt.Sprintf("\nAS\nBEGIN\n%s\nEND", function.Body)
			if _, err := writer.Write([]byte(stmt + "\nGO\n\n")); err != nil {
				return err
			}
		}
	}

	// Write triggers
	for _, trigger := range schema.Triggers {
		stmt := fmt.Sprintf("CREATE TRIGGER %s ON %s\n%s %s\nAS\nBEGIN\n%s\nEND",
			trigger.Name, trigger.Table, trigger.Timing, trigger.Event, trigger.Body)
		if _, err := writer.Write([]byte(stmt + "\nGO\n\n")); err != nil {
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

	if err := localParser.parseTables(ensureTerminated(statement)); err != nil {
		return nil, err
	}

	if len(tempSchema.Tables) == 0 {
		return nil, fmt.Errorf("no table found in statement")
	}

	return &tempSchema.Tables[0], nil
}

// parseViewStatement parses a CREATE VIEW statement
func (p *SQLServerStreamParser) parseViewStatement(statement string) (*sqlmapper.View, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &SQLServer{schema: tempSchema}

	if err := localParser.parseViews(ensureTerminated(statement)); err != nil {
		return nil, err
	}

	if len(tempSchema.Views) == 0 {
		return nil, fmt.Errorf("no view found in statement")
	}

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
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &SQLServer{schema: tempSchema}

	if err := localParser.parseTriggers(ensureTerminated(statement)); err != nil {
		return nil, err
	}

	if len(tempSchema.Triggers) == 0 {
		return nil, fmt.Errorf("no trigger found in statement")
	}

	return &tempSchema.Triggers[0], nil
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
var orReplaceRe = regexp.MustCompile(`(?i)^\s*CREATE\s+OR\s+REPLACE\s+`)

// dispatchKey normalises a statement for prefix matching. "CREATE OR REPLACE
// FUNCTION" shifts every fixed prefix, so the optional keywords are folded away
// once here rather than doubling every case in the dispatch.
func dispatchKey(statement string) string {
	return strings.ToUpper(orReplaceRe.ReplaceAllString(strings.TrimSpace(statement), "CREATE "))
}
