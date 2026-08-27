package sqlite

import (
	"strings"
	"sync"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTrigger(t *testing.T) {
	const dump = `CREATE TABLE users (id INTEGER PRIMARY KEY, updated_at TEXT);
CREATE TRIGGER users_touch AFTER UPDATE ON users BEGIN UPDATE users SET updated_at = 1 END;
`
	schema, err := NewSQLite().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Triggers, 1)

	trg := schema.Triggers[0]
	assert.Equal(t, "users_touch", trg.Name)
	assert.Equal(t, "users", trg.Table)
	assert.Equal(t, "AFTER", trg.Timing)
	assert.Equal(t, "UPDATE", trg.Event)
}

func TestParseUniqueIndex(t *testing.T) {
	const dump = `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT);
CREATE UNIQUE INDEX idx_users_email ON users (email);
`
	schema, err := NewSQLite().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)
	require.Len(t, schema.Tables[0].Indexes, 1)

	idx := schema.Tables[0].Indexes[0]
	assert.Equal(t, "idx_users_email", idx.Name)
	assert.Equal(t, []string{"email"}, idx.Columns)
	assert.True(t, idx.IsUnique)
}

func TestParseIndexWithIfNotExists(t *testing.T) {
	const dump = `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT);
CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);
`
	schema, err := NewSQLite().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables[0].Indexes, 1)
	assert.Equal(t, "idx_users_email", schema.Tables[0].Indexes[0].Name)
}

func TestGenerateIndexSQL(t *testing.T) {
	s := &SQLite{}

	plain := s.generateIndexSQL("users", sqlmapper.Index{Name: "idx_email", Columns: []string{"email"}})
	assert.Contains(t, plain, "INDEX idx_email")
	assert.Contains(t, plain, "users")

	unique := s.generateIndexSQL("users", sqlmapper.Index{Name: "uq_email", Columns: []string{"email"}, IsUnique: true})
	assert.Contains(t, unique, "UNIQUE")
}

func TestSQLiteGenerateNilSchema(t *testing.T) {
	_, err := NewSQLite().Generate(nil)
	assert.Error(t, err)
}

func TestSQLiteParseEmptyContent(t *testing.T) {
	_, err := NewSQLite().Parse("")
	assert.Error(t, err)
}

func TestSQLiteStreamParser_ParseTrigger(t *testing.T) {
	const dump = `CREATE TRIGGER users_touch AFTER UPDATE ON users BEGIN UPDATE users SET updated_at = 1 END;
`
	var triggers int
	err := NewSQLiteStreamParser().ParseStream(strings.NewReader(dump), func(obj stream.SchemaObject) error {
		if obj.Type == stream.TriggerObject {
			triggers++
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, triggers)
}

func TestSQLiteStreamParser_GenerateStreamObjects(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "INTEGER", IsPrimaryKey: true},
				{Name: "email", DataType: "TEXT"},
			},
			Indexes: []sqlmapper.Index{{Name: "idx_email", Columns: []string{"email"}}},
		}},
		Views: []sqlmapper.View{{Name: "v", Definition: "SELECT id FROM users"}},
	}

	var out strings.Builder
	require.NoError(t, NewSQLiteStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "idx_email")
	assert.Contains(t, got, "CREATE VIEW v")
}

func TestStripSQLComments(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"line comment", "SELECT 1 -- trailing\nSELECT 2", "SELECT 1 \nSELECT 2"},
		{"block comment", "SELECT /* inline */ 1", "SELECT  1"},
		{"multiline block", "SELECT\n/* one\n   two */\n1", "SELECT\n\n1"},
		{"a dash inside a string is kept", "SELECT '-- not a comment'", "SELECT '-- not a comment'"},
		{"a slash star inside a string is kept", "SELECT '/* nope */'", "SELECT '/* nope */'"},
		{"an escaped quote does not end the string", "SELECT 'it''s -- fine'", "SELECT 'it''s -- fine'"},
		{"nothing to strip", "SELECT 1", "SELECT 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripSQLComments(tt.in))
		})
	}
}

func TestDispatchKey(t *testing.T) {
	assert.Equal(t, "CREATE FUNCTION F()", dispatchKey("CREATE OR REPLACE FUNCTION f()"))
	assert.Equal(t, "CREATE TABLE T", dispatchKey("  create table t  "))
	assert.Equal(t, "CREATE VIEW V", dispatchKey("CREATE OR REPLACE VIEW v"))
}

// streamRich exercises every branch of both dispatchers.
const streamRich = `CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL);
CREATE VIEW active_users AS SELECT id FROM users;
CREATE INDEX idx_email ON users (email);
CREATE UNIQUE INDEX uq_email ON users (email);
CREATE TRIGGER trg AFTER INSERT ON users BEGIN SELECT 1 END;
`

func TestSQLiteStreamParser_RichDispatchAgrees(t *testing.T) {
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

	serial := count(func(cb func(stream.SchemaObject) error) error {
		return NewSQLiteStreamParser().ParseStream(strings.NewReader(streamRich), cb)
	})
	parallel := count(func(cb func(stream.SchemaObject) error) error {
		return NewSQLiteStreamParser().ParseStreamParallel(strings.NewReader(streamRich), cb, 4)
	})

	assert.Equal(t, serial, parallel)
	// A unique index used to reach the serial path only.
	assert.Equal(t, 2, serial[stream.IndexObject])
}

func TestSQLiteStreamParser_GenerateStreamFullSchema(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "INTEGER", IsPrimaryKey: true, AutoIncrement: true},
				{Name: "email", DataType: "TEXT", IsNullable: false},
				{Name: "note", DataType: "TEXT", DefaultValue: "none"},
			},
			Constraints: []sqlmapper.Constraint{
				{Name: "uq_email", Type: "UNIQUE", Columns: []string{"email"}},
			},
			Indexes: []sqlmapper.Index{{Name: "idx_email", Columns: []string{"email"}, IsUnique: true}},
		}},
		Views:    []sqlmapper.View{{Name: "v", Definition: "SELECT id FROM users"}},
		Triggers: []sqlmapper.Trigger{{Name: "trg", Timing: "AFTER", Event: "INSERT", Table: "users", Body: "BEGIN SELECT 1 END"}},
	}

	var out strings.Builder
	require.NoError(t, NewSQLiteStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "idx_email")
	assert.Contains(t, got, "CREATE VIEW v")
	assert.Contains(t, got, "CREATE TRIGGER trg")
}

func TestSQLiteParseFullDump(t *testing.T) {
	const dump = `-- schema
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL UNIQUE,
    status TEXT CHECK (status IN ('a','b')) DEFAULT 'a',
    org_id INTEGER REFERENCES orgs(id)
);
CREATE VIEW active_users AS SELECT id FROM users;
CREATE UNIQUE INDEX idx_users_email ON users (email);
CREATE TRIGGER users_touch AFTER UPDATE ON users BEGIN SELECT 1 END;
`
	schema, err := NewSQLite().Parse(dump)
	require.NoError(t, err)

	require.Len(t, schema.Tables, 1)
	assert.Len(t, schema.Views, 1)
	assert.Len(t, schema.Triggers, 1)
	assert.Len(t, schema.Tables[0].Indexes, 1)
	assert.Len(t, schema.Tables[0].Columns, 4)
}
