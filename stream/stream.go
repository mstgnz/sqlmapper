package stream

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/internal/sqlsplit"
)

// StreamParser represents an interface for streaming database dump operations
type StreamParser interface {
	// ParseStream parses a SQL dump from a reader and calls the callback for each parsed object
	ParseStream(reader io.Reader, callback func(SchemaObject) error) error

	// ParseStreamParallel parses a SQL dump from a reader in parallel using worker pools
	ParseStreamParallel(reader io.Reader, callback func(SchemaObject) error, workers int) error

	// GenerateStream generates SQL statements for schema objects and writes them to the writer
	GenerateStream(schema *sqlmapper.Schema, writer io.Writer) error
}

// WorkerPool represents a pool of workers for parallel processing
type WorkerPool struct {
	workers int
	jobs    chan string
	results chan SchemaObject
	errors  chan error
	wg      sync.WaitGroup
	parser  StreamParser
}

// NewWorkerPool creates a new worker pool with the specified number of workers
func NewWorkerPool(workers int, parser StreamParser) *WorkerPool {
	return &WorkerPool{
		workers: workers,
		jobs:    make(chan string, workers),
		results: make(chan SchemaObject, workers),
		errors:  make(chan error, workers),
		parser:  parser,
	}
}

// Start starts the worker pool
func (wp *WorkerPool) Start() {
	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
}

// worker processes jobs from the jobs channel
func (wp *WorkerPool) worker() {
	defer wp.wg.Done()
	for statement := range wp.jobs {
		// Parse the SQL statement using a temporary reader
		err := wp.parser.ParseStream(strings.NewReader(statement), func(obj SchemaObject) error {
			wp.results <- obj
			return nil
		})

		if err != nil {
			wp.errors <- fmt.Errorf("error processing statement: %v", err)
			return
		}
	}
}

// Submit submits a new SQL statement to be processed
func (wp *WorkerPool) Submit(statement string) {
	wp.jobs <- statement
}

// Results returns the channel for receiving processed schema objects
func (wp *WorkerPool) Results() <-chan SchemaObject {
	return wp.results
}

// Errors returns the channel for receiving processing errors
func (wp *WorkerPool) Errors() <-chan error {
	return wp.errors
}

// Wait waits for all workers to finish processing
func (wp *WorkerPool) Wait() {
	close(wp.jobs)
	wp.wg.Wait()
	close(wp.results)
	close(wp.errors)
}

// Process reads statements from reader and runs them through the pool.
//
// Both channels are drained to completion. Returning as soon as one of them
// closed lost whatever was still buffered in the other: Wait closes the error
// channel too, and a receive from a closed channel is always ready, so the
// select would take that case at random and return nil with results still
// waiting. A nil channel is never selected, which is how each one is retired
// once it is drained.
func (wp *WorkerPool) Process(reader io.Reader, callback func(SchemaObject) error) error {
	wp.Start()

	go func() {
		streamReader := NewStreamReader(reader, ";")
		for {
			statement, err := streamReader.ReadStatement()
			if err == io.EOF {
				break
			}
			if err != nil {
				wp.errors <- fmt.Errorf("error reading statement: %v", err)
				break
			}

			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			wp.Submit(statement)
		}
		wp.Wait()
	}()

	results, errs := wp.Results(), wp.Errors()
	for results != nil || errs != nil {
		select {
		case obj, ok := <-results:
			if !ok {
				results = nil
				continue
			}
			if err := callback(obj); err != nil {
				return err
			}

		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			return err
		}
	}
	return nil
}

// SchemaObjectType represents the type of schema object
type SchemaObjectType int

const (
	TableObject SchemaObjectType = iota
	ViewObject
	FunctionObject
	ProcedureObject
	TriggerObject
	IndexObject
	ConstraintObject
	SequenceObject
	TypeObject
	PermissionObject
)

// SchemaObject represents a parsed database object
type SchemaObject struct {
	Type SchemaObjectType
	Data interface{} // Table, View, Function, etc.
}

// StreamReader reads a SQL dump one statement at a time.
//
// Splitting is delegated to internal/sqlsplit, which knows that a delimiter
// inside a string, a comment, a quoted identifier, a PostgreSQL $$ body or a
// MySQL DELIMITER block does not end a statement. Scanning for the delimiter
// character alone cut every routine in half, and with it everything that
// followed in the file.
type StreamReader struct {
	splitter *sqlsplit.Splitter
}

// NewStreamReader creates a new StreamReader with the given reader and
// delimiter. The delimiter selects the dialect's splitting rules: ";" for
// MySQL, PostgreSQL and SQLite, "GO" for SQL Server, "/" for Oracle.
func NewStreamReader(reader io.Reader, delimiter string) *StreamReader {
	return &StreamReader{splitter: sqlsplit.New(reader, delimiter)}
}

// ReadStatement returns the next statement, or io.EOF at the end of the input.
func (sr *StreamReader) ReadStatement() (string, error) {
	return sr.splitter.Next()
}
