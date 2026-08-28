package oracle

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
    id NUMBER PRIMARY KEY,
    email VARCHAR2(255) NOT NULL
)
/
CREATE TABLE orders (
    id NUMBER PRIMARY KEY,
    user_id NUMBER NOT NULL
)
/
CREATE VIEW active_users AS SELECT id, email FROM users
/
CREATE INDEX idx_orders_user ON orders (user_id)
/
`

func TestOracleStreamParser_ParseStream(t *testing.T) {
	parser := NewOracleStreamParser()

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

func TestOracleStreamParser_ParseStreamParallel(t *testing.T) {
	parser := NewOracleStreamParser()

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

// TestOracleStreamParser_ParallelIsRaceFree exists to be run under -race. The
// dialect parser embedded in the stream parser used to be shared by every
// worker, so concurrent statements raced on its schema pointer.
func TestOracleStreamParser_ParallelIsRaceFree(t *testing.T) {
	var big strings.Builder
	for i := range 30 {
		big.WriteString("CREATE TABLE t")
		big.WriteString(string(rune('a' + i%26)))
		big.WriteString(" (id NUMBER PRIMARY KEY, name VARCHAR2(50) NOT NULL)\n/\n")
	}

	parser := NewOracleStreamParser()
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

func TestOracleStreamParser_CallbackErrorAborts(t *testing.T) {
	sentinel := errors.New("stop here")

	err := NewOracleStreamParser().ParseStream(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func TestOracleStreamParser_GenerateStreamNilSchema(t *testing.T) {
	var out strings.Builder
	assert.Error(t, NewOracleStreamParser().GenerateStream(nil, &out))
}

func TestOracleStreamParser_GenerateStream(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name: "users",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "NUMBER", IsPrimaryKey: true},
					{Name: "email", DataType: "VARCHAR2", Length: 255, IsNullable: false},
				},
			},
		},
	}

	var out strings.Builder
	require.NoError(t, NewOracleStreamParser().GenerateStream(schema, &out))
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

// streamObjects drives the object kinds only the stream parser handles.
const streamObjects = `CREATE OR REPLACE FUNCTION app.add_one(n IN NUMBER) RETURN NUMBER IS BEGIN RETURN n + 1
/
CREATE OR REPLACE PROCEDURE app.touch_user(uid IN NUMBER) IS BEGIN NULL
/
CREATE OR REPLACE TRIGGER app.users_bi BEFORE INSERT ON app.users FOR EACH ROW BEGIN NULL
/
CREATE SEQUENCE app.order_seq START WITH 10 INCREMENT BY 2
/
CREATE OR REPLACE TYPE app.addr_t AS OBJECT (street VARCHAR2(100))
/
CREATE UNIQUE INDEX idx_users_email ON app.users (email)
/
`

func TestOracleStreamParser_ParseObjects(t *testing.T) {
	counts := map[stream.SchemaObjectType]int{}

	err := NewOracleStreamParser().ParseStream(strings.NewReader(streamObjects), func(obj stream.SchemaObject) error {
		counts[obj.Type]++
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, 1, counts[stream.FunctionObject])
	assert.Equal(t, 1, counts[stream.ProcedureObject])
	assert.Equal(t, 1, counts[stream.TriggerObject])
	assert.Equal(t, 1, counts[stream.SequenceObject])
	assert.Equal(t, 1, counts[stream.TypeObject])
	assert.Equal(t, 1, counts[stream.IndexObject])
}

func TestOracleStreamParser_ParseObjectsParallel(t *testing.T) {
	var mu sync.Mutex
	counts := map[stream.SchemaObjectType]int{}

	err := NewOracleStreamParser().ParseStreamParallel(strings.NewReader(streamObjects), func(obj stream.SchemaObject) error {
		mu.Lock()
		counts[obj.Type]++
		mu.Unlock()
		return nil
	}, 4)
	require.NoError(t, err)

	assert.Equal(t, 1, counts[stream.SequenceObject])
	assert.Equal(t, 1, counts[stream.TypeObject])
}

func TestOracleStreamParser_IndexDetail(t *testing.T) {
	var idx *sqlmapper.Index
	err := NewOracleStreamParser().ParseStream(strings.NewReader(streamObjects), func(obj stream.SchemaObject) error {
		if obj.Type == stream.IndexObject {
			idx = obj.Data.(*sqlmapper.Index)
		}
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, idx)

	assert.Equal(t, "idx_users_email", idx.Name)
	assert.Equal(t, []string{"email"}, idx.Columns)
	assert.True(t, idx.IsUnique)
}

func TestOracleGenerateFullSchema(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "NUMBER", IsPrimaryKey: true, IsNullable: false},
				{Name: "email", DataType: "VARCHAR2", Length: 255, IsNullable: false},
				{Name: "note", DataType: "VARCHAR2", Length: 100, DefaultValue: "none"},
			},
			Constraints: []sqlmapper.Constraint{
				{Name: "uq_email", Type: "UNIQUE", Columns: []string{"email"}},
			},
			Indexes: []sqlmapper.Index{
				// Oracle indexes a unique key of its own, so one over the same
				// column is a duplicate it refuses to create: ORA-01408.
				{Name: "idx_email", Columns: []string{"email"}, IsUnique: true},
				{Name: "idx_note", Columns: []string{"note"}},
			},
		}},
	}

	out, err := NewOracle().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "CREATE TABLE users")
	assert.Contains(t, out, "VARCHAR2(255)")
	assert.Contains(t, out, "idx_note")
	assert.NotContains(t, out, "idx_email",
		"an index over a unique key's own columns is one Oracle will not create")
}

// TestOracleStreamParser_SerialAndParallelAgree pins the two dispatchers together.
// They are separate switch statements, and they had drifted: one of them was
// missing a branch, so an object kind survived a serial parse and vanished from
// a parallel one.
func TestOracleStreamParser_SerialAndParallelAgree(t *testing.T) {
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

	for name, dump := range map[string]string{"tables": streamDump, "objects": streamObjects} {
		t.Run(name, func(t *testing.T) {
			serial := count(func(cb func(stream.SchemaObject) error) error {
				return NewOracleStreamParser().ParseStream(strings.NewReader(dump), cb)
			})
			parallel := count(func(cb func(stream.SchemaObject) error) error {
				return NewOracleStreamParser().ParseStreamParallel(strings.NewReader(dump), cb, 4)
			})
			assert.Equal(t, serial, parallel)
		})
	}
}

// TestOracleStreamParser_ParseErrorsPropagate drives the error branches of the
// per-statement helpers: a statement whose keyword matches but whose body does
// not parse must surface an error rather than being silently skipped.
func TestOracleStreamParser_ParseErrorsPropagate(t *testing.T) {
	malformed := []string{
		"CREATE TABLE",
		"CREATE VIEW",
		"CREATE FUNCTION",
		"CREATE PROCEDURE",
		"CREATE TRIGGER",
		"CREATE SEQUENCE",
		"CREATE TYPE",
		"CREATE INDEX",
	}

	for _, stmt := range malformed {
		t.Run(stmt, func(t *testing.T) {
			err := NewOracleStreamParser().ParseStream(strings.NewReader(stmt+"\n/\n"), func(obj stream.SchemaObject) error {
				return nil
			})
			assert.Error(t, err, "a keyword with no body must not parse cleanly")
		})
	}
}

func TestOracleStreamParser_ParallelSurfacesParseErrors(t *testing.T) {
	err := NewOracleStreamParser().ParseStreamParallel(
		strings.NewReader("CREATE TABLE\n/\n"),
		func(obj stream.SchemaObject) error { return nil },
		2,
	)
	assert.Error(t, err)
}
