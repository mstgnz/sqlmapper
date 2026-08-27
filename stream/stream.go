package stream

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/mstgnz/sqlmapper"
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

// Process processes a stream of SQL statements in parallel
func (wp *WorkerPool) Process(reader io.Reader, callback func(SchemaObject) error) error {
	// Start the worker pool
	wp.Start()

	// Start a goroutine to read statements and submit them to workers
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

	// Process results and handle errors
	for {
		select {
		case obj, ok := <-wp.Results():
			if !ok {
				return nil
			}
			if err := callback(obj); err != nil {
				return err
			}
		case err := <-wp.Errors():
			return err
		}
	}
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

// StreamReader provides buffered reading of SQL statements
type StreamReader struct {
	reader    *bufio.Reader
	delimiter string
	buffer    []byte
}

// NewStreamReader creates a new StreamReader with the given reader and delimiter
func NewStreamReader(reader io.Reader, delimiter string) *StreamReader {
	return &StreamReader{
		reader:    bufio.NewReader(reader),
		delimiter: delimiter,
		buffer:    make([]byte, 0, 4096),
	}
}

// ReadStatement reads the next SQL statement from the reader
func (sr *StreamReader) ReadStatement() (string, error) {
	var statement []byte
	inString := false
	inComment := false
	lineComment := false
	escaped := false

	for {
		b, err := sr.reader.ReadByte()
		if err != nil {
			if err == io.EOF && len(statement) > 0 {
				return string(statement), nil
			}
			return "", err
		}

		// Handle string literals
		if b == '\'' && !inComment && !escaped {
			inString = !inString
		}

		// Handle escape characters
		if b == '\\' && !inComment {
			escaped = !escaped
		} else {
			escaped = false
		}

		// Handle comments
		if !inString && !inComment && b == '-' {
			nextByte, err := sr.reader.ReadByte()
			if err == nil {
				if nextByte == '-' {
					lineComment = true
					inComment = true
					continue
				}
				// Only a byte that was actually read can be pushed back.
				if err := sr.reader.UnreadByte(); err != nil {
					return "", err
				}
			}
		}

		if !inString && !inComment && b == '/' {
			nextByte, err := sr.reader.ReadByte()
			if err == nil {
				if nextByte == '*' {
					inComment = true
					continue
				}
				if err := sr.reader.UnreadByte(); err != nil {
					return "", err
				}
			}
		}

		if inComment && !lineComment && b == '*' {
			nextByte, err := sr.reader.ReadByte()
			if err == nil {
				if nextByte == '/' {
					inComment = false
					continue
				}
				if err := sr.reader.UnreadByte(); err != nil {
					return "", err
				}
			}
		}

		if lineComment && b == '\n' {
			inComment = false
			lineComment = false
			continue
		}

		// Skip comments
		if inComment {
			continue
		}

		// Add character to statement
		statement = append(statement, b)

		// Check for delimiter
		if !inString && len(statement) >= len(sr.delimiter) {
			lastIdx := len(statement) - len(sr.delimiter)
			if string(statement[lastIdx:]) == sr.delimiter {
				return string(statement[:lastIdx]), nil
			}
		}
	}
}
