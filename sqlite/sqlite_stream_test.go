package sqlite

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const streamDump = `CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL
);

CREATE TABLE orders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL
);

CREATE VIEW active_users AS SELECT id, email FROM users;

CREATE INDEX idx_orders_user ON orders (user_id);
`

func TestSQLiteStreamParser_ParseStream(t *testing.T) {
	parser := NewSQLiteStreamParser()

	var tables, views, indexes []string
	err := parser.ParseStream(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		switch v := obj.Data.(type) {
		case *sqlmapper.Table:
			tables = append(tables, v.Name)
		case *sqlmapper.View:
			views = append(views, v.Name)
		case *sqlmapper.Index:
			indexes = append(indexes, v.Name)
		}
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"users", "orders"}, tables)
	assert.Equal(t, []string{"active_users"}, views)
	// A standalone CREATE INDEX cannot resolve its table, which used to abort
	// the whole stream with "no index found in statement".
	assert.Equal(t, []string{"idx_orders_user"}, indexes)
}

func TestSQLiteStreamParser_ParseStreamParallel(t *testing.T) {
	parser := NewSQLiteStreamParser()

	var mu sync.Mutex
	var names []string
	err := parser.ParseStreamParallel(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		mu.Lock()
		defer mu.Unlock()
		if table, ok := obj.Data.(*sqlmapper.Table); ok {
			names = append(names, table.Name)
		}
		return nil
	}, 4)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"users", "orders"}, names)
}

// TestSQLiteStreamParser_ParallelIsRaceFree exists to be run under -race. The
// dialect parser embedded in the stream parser used to be shared by every
// worker, so concurrent statements raced on its schema pointer.
func TestSQLiteStreamParser_ParallelIsRaceFree(t *testing.T) {
	var big strings.Builder
	for i := range 30 {
		big.WriteString("CREATE TABLE t")
		big.WriteString(string(rune('a' + i%26)))
		big.WriteString(" (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL);\n")
	}

	parser := NewSQLiteStreamParser()
	var count int
	var mu sync.Mutex
	err := parser.ParseStreamParallel(strings.NewReader(big.String()), func(obj stream.SchemaObject) error {
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	}, 8)
	require.NoError(t, err)
	assert.Equal(t, 30, count)
}

func TestSQLiteStreamParser_CallbackErrorAborts(t *testing.T) {
	sentinel := errors.New("stop here")

	err := NewSQLiteStreamParser().ParseStream(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func TestSQLiteStreamParser_GenerateStreamNilSchema(t *testing.T) {
	var out strings.Builder
	assert.Error(t, NewSQLiteStreamParser().GenerateStream(nil, &out))
}

func TestSQLiteStreamParser_GenerateStream(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name: "users",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "INTEGER", IsPrimaryKey: true},
					{Name: "email", DataType: "TEXT", Length: 255, IsNullable: false},
				},
			},
		},
	}

	var out strings.Builder
	require.NoError(t, NewSQLiteStreamParser().GenerateStream(schema, &out))
	assert.Contains(t, out.String(), "users")
}

func TestEnsureTerminated(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"appends the missing delimiter", "CREATE TABLE t (id INT)", "CREATE TABLE t (id INT);"},
		{"keeps an existing delimiter", "CREATE TABLE t (id INT);", "CREATE TABLE t (id INT);"},
		{"collapses newlines", "CREATE VIEW v AS\n  SELECT 1", "CREATE VIEW v AS SELECT 1;"},
		{"empty input stays empty", "   \n  ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ensureTerminated(tt.input))
		})
	}
}

// TestSQLiteStreamParser_SerialAndParallelAgree pins the two dispatchers together.
// They are separate switch statements, and they had drifted: one of them was
// missing a branch, so an object kind survived a serial parse and vanished from
// a parallel one.
func TestSQLiteStreamParser_SerialAndParallelAgree(t *testing.T) {
	count := func(run func(cb func(stream.SchemaObject) error) error) map[stream.SchemaObjectType]int {
		t.Helper()
		var mu sync.Mutex
		seen := map[stream.SchemaObjectType]int{}
		require.NoError(t, run(func(obj stream.SchemaObject) error {
			mu.Lock()
			seen[obj.Type]++
			mu.Unlock()
			return nil
		}))
		return seen
	}

	for name, dump := range map[string]string{"objects": streamDump} {
		t.Run(name, func(t *testing.T) {
			serial := count(func(cb func(stream.SchemaObject) error) error {
				return NewSQLiteStreamParser().ParseStream(strings.NewReader(dump), cb)
			})
			parallel := count(func(cb func(stream.SchemaObject) error) error {
				return NewSQLiteStreamParser().ParseStreamParallel(strings.NewReader(dump), cb, 4)
			})
			assert.Equal(t, serial, parallel)
		})
	}
}
