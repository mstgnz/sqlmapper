package stream

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
)

// MockStreamParser is a mock implementation of StreamParser for testing
type MockStreamParser struct {
	parseStreamFunc         func(reader io.Reader, callback func(SchemaObject) error) error
	parseStreamParallelFunc func(reader io.Reader, callback func(SchemaObject) error, workers int) error
	generateStreamFunc      func(schema *sqlmapper.Schema, writer io.Writer) error
}

func (m *MockStreamParser) ParseStream(reader io.Reader, callback func(SchemaObject) error) error {
	if m.parseStreamFunc != nil {
		return m.parseStreamFunc(reader, callback)
	}
	return nil
}

func (m *MockStreamParser) ParseStreamParallel(reader io.Reader, callback func(SchemaObject) error, workers int) error {
	if m.parseStreamParallelFunc != nil {
		return m.parseStreamParallelFunc(reader, callback, workers)
	}
	return nil
}

func (m *MockStreamParser) GenerateStream(schema *sqlmapper.Schema, writer io.Writer) error {
	if m.generateStreamFunc != nil {
		return m.generateStreamFunc(schema, writer)
	}
	return nil
}

func TestStreamReader_ReadStatement(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		delimiter string
		want      []string
		wantErr   bool
	}{
		{
			name: "Basic SQL statements",
			input: `
				CREATE TABLE users (id INT);
				CREATE TABLE posts (id INT);
			`,
			delimiter: ";",
			want: []string{
				"CREATE TABLE users (id INT)",
				"CREATE TABLE posts (id INT)",
			},
			wantErr: false,
		},
		{
			name: "Statements with comments",
			input: `
				-- Create users table
				CREATE TABLE users (id INT); /* with id */
				/* Multi-line
				   comment */
				CREATE TABLE posts (id INT); -- end
			`,
			delimiter: ";",
			want: []string{
				"CREATE TABLE users (id INT)",
				"CREATE TABLE posts (id INT)",
			},
			wantErr: false,
		},
		{
			name: "Statements with string literals",
			input: `
				CREATE TABLE users (name VARCHAR(50) DEFAULT 'John;Smith');
				INSERT INTO users VALUES ('user;with;semicolons');
			`,
			delimiter: ";",
			want: []string{
				"CREATE TABLE users (name VARCHAR(50) DEFAULT 'John;Smith')",
				"INSERT INTO users VALUES ('user;with;semicolons')",
			},
			wantErr: false,
		},
		{
			name: "Custom delimiter",
			input: `
				CREATE TABLE users (id INT)$$
				CREATE TABLE posts (id INT)$$
			`,
			delimiter: "$$",
			want: []string{
				"CREATE TABLE users (id INT)",
				"CREATE TABLE posts (id INT)",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewStreamReader(strings.NewReader(tt.input), tt.delimiter)
			var got []string

			for {
				stmt, err := reader.ReadStatement()
				if err == io.EOF {
					break
				}
				if err != nil {
					if !tt.wantErr {
						t.Errorf("ReadStatement() error = %v", err)
					}
					return
				}

				stmt = strings.TrimSpace(stmt)
				if stmt != "" {
					got = append(got, stmt)
				}
			}

			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWorkerPool(t *testing.T) {
	tests := []struct {
		name      string
		workers   int
		input     string
		mockSetup func() *MockStreamParser
		validate  func(t *testing.T, objects []SchemaObject, err error)
	}{
		{
			name:    "Process multiple statements",
			workers: 2,
			input: `
				CREATE TABLE users (id INT);
				CREATE TABLE posts (id INT);
			`,
			mockSetup: func() *MockStreamParser {
				return &MockStreamParser{
					parseStreamFunc: func(reader io.Reader, callback func(SchemaObject) error) error {
						// Simulate parsing by creating mock objects
						buf := new(bytes.Buffer)
						if _, err := buf.ReadFrom(reader); err != nil {
							return err
						}
						stmt := strings.TrimSpace(buf.String())

						if strings.Contains(stmt, "users") {
							return callback(SchemaObject{Type: TableObject, Data: "users"})
						}
						if strings.Contains(stmt, "posts") {
							return callback(SchemaObject{Type: TableObject, Data: "posts"})
						}
						return nil
					},
				}
			},
			validate: func(t *testing.T, objects []SchemaObject, err error) {
				assert.NoError(t, err)
				assert.Len(t, objects, 2)

				// Create a map to check both objects exist
				objectMap := make(map[string]bool)
				for _, obj := range objects {
					assert.Equal(t, TableObject, obj.Type)
					objectMap[obj.Data.(string)] = true
				}

				assert.True(t, objectMap["users"], "users table should exist")
				assert.True(t, objectMap["posts"], "posts table should exist")
			},
		},
		{
			name:    "Handle parser error",
			workers: 2,
			input:   "INVALID SQL;",
			mockSetup: func() *MockStreamParser {
				return &MockStreamParser{
					parseStreamFunc: func(reader io.Reader, callback func(SchemaObject) error) error {
						return fmt.Errorf("invalid SQL syntax")
					},
				}
			},
			validate: func(t *testing.T, objects []SchemaObject, err error) {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "invalid SQL syntax")
				assert.Len(t, objects, 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := tt.mockSetup()
			pool := NewWorkerPool(tt.workers, parser)

			var objects []SchemaObject
			err := pool.Process(strings.NewReader(tt.input), func(obj SchemaObject) error {
				objects = append(objects, obj)
				return nil
			})

			tt.validate(t, objects, err)
		})
	}
}

// TestWorkerPoolDrainsEveryResult pins the fix for a pool that returned early.
//
// Wait closes the error channel as well as the result channel, and a receive
// from a closed channel is always ready, so the select in Process took that
// case at random and returned nil while results were still buffered. It lost an
// object in roughly one run in thirteen, silently.
func TestWorkerPoolDrainsEveryResult(t *testing.T) {
	const runs = 300

	for i := 0; i < runs; i++ {
		parser := &MockStreamParser{
			parseStreamFunc: func(reader io.Reader, callback func(SchemaObject) error) error {
				buf := new(bytes.Buffer)
				if _, err := buf.ReadFrom(reader); err != nil {
					return err
				}
				stmt := strings.TrimSpace(buf.String())
				for _, name := range []string{"users", "posts", "orders"} {
					if strings.Contains(stmt, name) {
						return callback(SchemaObject{Type: TableObject, Data: name})
					}
				}
				return nil
			},
		}

		var got []SchemaObject
		err := NewWorkerPool(2, parser).Process(
			strings.NewReader("CREATE TABLE users (id INT);CREATE TABLE posts (id INT);CREATE TABLE orders (id INT);"),
			func(obj SchemaObject) error {
				got = append(got, obj)
				return nil
			})

		require.NoError(t, err)
		require.Len(t, got, 3, "run %d dropped an object", i)
	}
}

// TestWorkerPoolReportsParserErrors checks the other side of the same select:
// an error still has to reach the caller rather than being drowned out by the
// results arriving alongside it.
func TestWorkerPoolReportsParserErrors(t *testing.T) {
	parser := &MockStreamParser{
		parseStreamFunc: func(reader io.Reader, callback func(SchemaObject) error) error {
			return errors.New("invalid SQL syntax")
		},
	}

	err := NewWorkerPool(2, parser).Process(
		strings.NewReader("BROKEN;"),
		func(SchemaObject) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid SQL syntax")
}
