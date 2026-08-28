package sqlite

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

// SQLiteStreamParser implements the StreamParser interface for SQLite
type SQLiteStreamParser struct {
	sqlite *SQLite
}

// NewSQLiteStreamParser creates a new SQLite stream parser
// sqliteStreamIndexRe reads a standalone CREATE INDEX statement, which the whole-file
// parser cannot do because it resolves the target table first.
var sqliteStreamIndexRe = regexp.MustCompile(`(?i)CREATE\s+(UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([.\w]+)\s+ON\s+([.\w]+)\s*\((.*?)\)`)

func NewSQLiteStreamParser() *SQLiteStreamParser {
	return &SQLiteStreamParser{
		sqlite: NewSQLite().(*SQLite),
	}
}

// ParseStream implements the StreamParser interface
func (p *SQLiteStreamParser) ParseStream(reader io.Reader, callback func(stream.SchemaObject) error) error {
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
		dispatch := dispatchKey(statement)

		// Parse CREATE TABLE statements
		if keyword.HasPrefix(dispatch, "CREATE TABLE") {
			table, err := p.parseTableStatement(statement)
			if err != nil {
				return err
			}
			// sqlite_sequence belongs to SQLite rather than to the schema, and
			// the reader reports nothing for it.
			if table == nil {
				continue
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

		// Parse CREATE INDEX statements
		if strings.HasPrefix(dispatch, "CREATE INDEX") ||
			strings.HasPrefix(dispatch, "CREATE UNIQUE INDEX") {
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
	}

	return nil
}

// ParseStreamParallel implements parallel processing for SQLite stream parsing
func (p *SQLiteStreamParser) ParseStreamParallel(reader io.Reader, callback func(stream.SchemaObject) error, workers int) error {
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
func (p *SQLiteStreamParser) parseStatement(statement string) (*stream.SchemaObject, error) {
	upperStatement := dispatchKey(statement)

	switch {
	case keyword.HasPrefix(upperStatement, "CREATE TABLE"):
		table, err := p.parseTableStatement(statement)
		if err != nil {
			return nil, err
		}
		if table == nil {
			return nil, nil // sqlite_sequence belongs to SQLite, not the schema
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

	case strings.HasPrefix(upperStatement, "CREATE TRIGGER"):
		trigger, err := p.parseTriggerStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.TriggerObject,
			Data: trigger,
		}, nil
	}

	return nil, nil
}

// GenerateStream implements the StreamParser interface
func (p *SQLiteStreamParser) GenerateStream(schema *sqlmapper.Schema, writer io.Writer) error {
	if schema == nil {
		return fmt.Errorf("schema cannot be nil")
	}

	// Write tables in the order Generate uses, so a streamed schema reads the
	// same way and a child table does not precede its parent.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)
	for _, table := range tables {
		stmt := p.sqlite.generateTableSQL(table, deferredFKs[table.Name])
		if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n\n")); err != nil {
			return err
		}

		// Generate indexes for this table
		for _, index := range table.Indexes {
			stmt := p.sqlite.generateIndexSQL(table.Name, index)
			if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n")); err != nil {
				return err
			}
		}
	}

	// Write views
	for _, view := range schema.Views {
		stmt := fmt.Sprintf("CREATE VIEW %s AS %s", view.Name, view.Definition)
		if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n\n")); err != nil {
			return err
		}
	}

	// Routines are rendered by the same code the file generator uses, so the
	// streamed output and the file output agree.
	if routines := p.sqlite.generateRoutinesSQL(schema); routines != "" {
		if _, err := writer.Write([]byte(routines)); err != nil {
			return err
		}
	}

	return nil
}

// parseTableStatement parses a CREATE TABLE statement
func (p *SQLiteStreamParser) parseTableStatement(statement string) (*sqlmapper.Table, error) {
	// The file parser's reader is used, not a second one of the stream's own.
	// There used to be two implementations of this in the package and they did
	// not agree: on a real sqlite3 .schema the stream path dropped columns,
	// split NUMERIC(10,2) at its comma, kept sqlite_sequence and lost every
	// constraint.
	localParser := &SQLite{schema: &sqlmapper.Schema{}}

	table, err := localParser.parseCreateTable([]byte(statement))
	if err != nil {
		return nil, err
	}
	if isSQLiteInternalTable(table.Name) {
		return nil, nil
	}

	return &table, nil
}

// parseViewStatement parses a CREATE VIEW statement
func (p *SQLiteStreamParser) parseViewStatement(statement string) (*sqlmapper.View, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &SQLite{schema: tempSchema}

	if err := localParser.parseViews(ensureTerminated(statement)); err != nil {
		return nil, err
	}

	if len(tempSchema.Views) == 0 {
		return nil, fmt.Errorf("no view found in statement")
	}

	return &tempSchema.Views[0], nil
}

// parseIndexStatement parses a CREATE INDEX statement
func (p *SQLiteStreamParser) parseIndexStatement(statement string) (*sqlmapper.Index, error) {
	// parseIndexes attaches the index to a table that is already present in the
	// schema, which never holds for a single statement read off the stream, so
	// the statement is read directly here.
	m := sqliteStreamIndexRe.FindStringSubmatch(ensureTerminated(statement))
	if len(m) < 5 {
		return nil, fmt.Errorf("no index found in statement")
	}

	index := &sqlmapper.Index{
		Name:     m[2],
		Columns:  (&SQLite{}).splitAndTrim(m[4]),
		IsUnique: strings.TrimSpace(m[1]) != "",
	}
	return index, nil
}

// parseTriggerStatement parses a CREATE TRIGGER statement
func (p *SQLiteStreamParser) parseTriggerStatement(statement string) (*sqlmapper.Trigger, error) {
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &SQLite{schema: tempSchema}

	if err := localParser.parseTriggers(ensureTerminated(statement)); err != nil {
		return nil, err
	}

	if len(tempSchema.Triggers) == 0 {
		return nil, fmt.Errorf("no trigger found in statement")
	}

	return &tempSchema.Triggers[0], nil
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
