package mysql

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

// MySQLStreamParser implements the StreamParser interface for MySQL
type MySQLStreamParser struct {
	mysql *MySQL
}

// NewMySQLStreamParser creates a new MySQL stream parser
func NewMySQLStreamParser() *MySQLStreamParser {
	return &MySQLStreamParser{
		mysql: NewMySQL().(*MySQL),
	}
}

// ParseStream implements the StreamParser interface
func (p *MySQLStreamParser) ParseStream(reader io.Reader, callback func(stream.SchemaObject) error) error {
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

// ParseStreamParallel implements parallel processing for MySQL stream parsing
func (p *MySQLStreamParser) ParseStreamParallel(reader io.Reader, callback func(stream.SchemaObject) error, workers int) error {
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
func (p *MySQLStreamParser) parseStatement(statement string) (*stream.SchemaObject, error) {
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

	case strings.HasPrefix(upperStatement, "CREATE PROCEDURE"):
		procedure, err := p.parseProcedureStatement(statement)
		if err != nil {
			return nil, err
		}
		return &stream.SchemaObject{
			Type: stream.ProcedureObject,
			Data: procedure,
		}, nil

	case strings.HasPrefix(upperStatement, "CREATE INDEX"),
		strings.HasPrefix(upperStatement, "CREATE UNIQUE INDEX"),
		strings.HasPrefix(upperStatement, "CREATE FULLTEXT INDEX"):
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
func (p *MySQLStreamParser) GenerateStream(schema *sqlmapper.Schema, writer io.Writer) error {
	if schema == nil {
		return fmt.Errorf("schema cannot be nil")
	}

	// Write tables, ordered the same way Generate orders them so a streamed
	// schema loads even when the source listed a child table before its parent.
	tables, deferredFKs := sqlmapper.OrderTablesByDependency(schema.Tables)
	for _, table := range tables {
		stmt := p.mysql.generateTableSQL(table, deferredFKs[table.Name])
		if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n\n")); err != nil {
			return err
		}

		// Generate indexes for this table
		for _, index := range table.Indexes {
			stmt := p.mysql.generateIndexSQL(table.Name, index, columnsByName(table))
			if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n")); err != nil {
				return err
			}
		}
	}

	// Foreign keys that close a cycle are added once every table exists.
	for _, table := range tables {
		for _, c := range deferredFKs[table.Name] {
			stmt := fmt.Sprintf("ALTER TABLE %s ADD %s;\n", table.Name, p.mysql.generateConstraintSQL(c, columnsByName(table)))
			if _, err := writer.Write([]byte(stmt)); err != nil {
				return err
			}
		}
	}

	// Views are rendered by the same code the file generator uses, so a body
	// carrying another dialect's schema qualifier is translated here too.
	for _, view := range schema.Views {
		stmt := p.mysql.generateViewSQL(view)
		if _, err := writer.Write([]byte(sqlfmt.Terminate(stmt, ";") + "\n\n")); err != nil {
			return err
		}
	}

	// Routines are rendered by the same code the file generator uses, because
	// the two used to disagree: this path wrote a trigger MySQL could not load
	// and the other dropped it entirely.
	if routines := p.mysql.generateRoutinesSQL(schema); routines != "" {
		if _, err := writer.Write([]byte(routines)); err != nil {
			return err
		}
	}

	// Grants are rendered by the same code the file generator uses, and come
	// last for the same reason: they name objects that have to exist first.
	if perms := p.mysql.generatePermissionsSQL(schema); perms != "" {
		if _, err := writer.Write([]byte("\n" + perms)); err != nil {
			return err
		}
	}

	return nil
}

// parseTableStatement parses a CREATE TABLE statement
func (p *MySQLStreamParser) parseTableStatement(statement string) (*sqlmapper.Table, error) {
	// Create a temporary schema for parsing
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &MySQL{schema: tempSchema}

	// Parse the table using the existing MySQL parser
	if err := localParser.parseTables(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	// Check if any table was parsed
	if len(tempSchema.Tables) == 0 {
		return nil, fmt.Errorf("no table found in statement")
	}

	// Return the first table
	return &tempSchema.Tables[0], nil
}

// parseIndexStatement reads a standalone CREATE INDEX.
//
// parseIndexes attaches the index to a table already present in the schema,
// which never holds for a single statement read off the stream, so the header is
// read directly here. Nothing read one at all on this path: a dump whose indexes
// are written as statements of their own lost every one of them, while the same
// file read whole kept them.
func (p *MySQLStreamParser) parseIndexStatement(statement string) (*sqlmapper.Index, error) {
	m := mysqlIndexRe.FindStringSubmatch(statement)
	if len(m) < 5 {
		return nil, fmt.Errorf("no index found in statement")
	}
	return &sqlmapper.Index{
		Name:     m[2],
		Columns:  splitAndTrim(m[4]),
		IsUnique: strings.TrimSpace(m[1]) != "",
	}, nil
}

// parseViewStatement parses a CREATE VIEW statement
func (p *MySQLStreamParser) parseViewStatement(statement string) (*sqlmapper.View, error) {
	// Create a temporary schema for parsing
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &MySQL{schema: tempSchema}

	// Parse the view using the existing MySQL parser
	if err := localParser.parseViews(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	// Check if any view was parsed
	if len(tempSchema.Views) == 0 {
		return nil, fmt.Errorf("no view found in statement")
	}

	// Return the first view
	return &tempSchema.Views[0], nil
}

// parseFunctionStatement parses a CREATE FUNCTION statement
func (p *MySQLStreamParser) parseFunctionStatement(statement string) (*sqlmapper.Function, error) {
	// Create a temporary schema for parsing
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &MySQL{schema: tempSchema}

	// Parse the function using the existing MySQL parser
	if err := localParser.parseFunctions(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	// Check if any function was parsed
	if len(tempSchema.Functions) == 0 {
		return nil, fmt.Errorf("no function found in statement")
	}

	// Return the first function
	return &tempSchema.Functions[0], nil
}

// parseProcedureStatement parses a CREATE PROCEDURE statement
func (p *MySQLStreamParser) parseProcedureStatement(statement string) (*sqlmapper.Procedure, error) {
	// Create a temporary schema for parsing
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &MySQL{schema: tempSchema}

	// Parse the procedure using the existing MySQL parser
	if err := localParser.parseFunctions(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	// Check if any function was parsed
	if len(tempSchema.Functions) == 0 {
		return nil, fmt.Errorf("no procedure found in statement")
	}

	// Find the first procedure (function with IsProc=true)
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
func (p *MySQLStreamParser) parseTriggerStatement(statement string) (*sqlmapper.Trigger, error) {
	// Create a temporary schema for parsing
	tempSchema := &sqlmapper.Schema{}
	// A parser per statement: stream parsers are used concurrently and
	// must not share mutable state through the embedded dialect parser.
	localParser := &MySQL{schema: tempSchema}

	// Parse the trigger using the existing MySQL parser
	if err := localParser.parseTriggers(localParser.normalizeContent(ensureTerminated(statement))); err != nil {
		return nil, err
	}

	// Check if any trigger was parsed
	if len(tempSchema.Triggers) == 0 {
		return nil, fmt.Errorf("no trigger found in statement")
	}

	// Return the first trigger
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
// mysqldump writes a view with the attributes it was created with, each in its
// own version block:
//
//	/*!50001 CREATE ALGORITHM=UNDEFINED */
//	/*!50013 DEFINER=`root`@`localhost` SQL SECURITY DEFINER */
//	/*!50001 VIEW `v` AS select ... */;
//
// Folding only OR REPLACE meant the real definition was not recognised as a
// view at all, and the reader kept the SELECT 1 AS col stand-in mysqldump
// writes earlier in the file instead.
var orReplaceRe = regexp.MustCompile(`(?i)^\s*CREATE(?:\s+OR\s+REPLACE)?` +
	`(?:\s+ALGORITHM\s*=\s*\S+)?(?:\s+DEFINER\s*=\s*\S+)?` +
	`(?:\s+SQL\s+SECURITY\s+\w+)?\s+`)

// dispatchKey normalises a statement for prefix matching. "CREATE OR REPLACE
// FUNCTION" shifts every fixed prefix, so the optional keywords are folded away
// once here rather than doubling every case in the dispatch.
func dispatchKey(statement string) string {
	return strings.ToUpper(orReplaceRe.ReplaceAllString(strings.TrimSpace(statement), "CREATE "))
}
