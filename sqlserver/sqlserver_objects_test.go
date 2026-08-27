package sqlserver

import (
	"strings"
	"sync"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTableConstraints(t *testing.T) {
	const dump = `CREATE TABLE app.orders (
    id INT IDENTITY(1,1),
    user_id INT NOT NULL,
    code NVARCHAR(20) NOT NULL,
    CONSTRAINT PK_orders PRIMARY KEY (id),
    CONSTRAINT UQ_orders_code UNIQUE (code),
    CONSTRAINT FK_orders_users FOREIGN KEY (user_id) REFERENCES app.users(id) ON DELETE CASCADE
);
`
	schema, err := NewSQLServer().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)

	table := schema.Tables[0]
	assert.Equal(t, "orders", table.Name)

	byType := map[string]sqlmapper.Constraint{}
	for _, c := range table.Constraints {
		byType[c.Type] = c
	}

	require.Contains(t, byType, "PRIMARY KEY")
	assert.Equal(t, []string{"id"}, byType["PRIMARY KEY"].Columns)

	require.Contains(t, byType, "UNIQUE")
	assert.Equal(t, []string{"code"}, byType["UNIQUE"].Columns)

	require.Contains(t, byType, "FOREIGN KEY")
	fk := byType["FOREIGN KEY"]
	assert.Equal(t, []string{"user_id"}, fk.Columns)
	assert.Contains(t, fk.RefTable, "users")
}

func TestParseView(t *testing.T) {
	const dump = `CREATE TABLE app.users (id INT, email NVARCHAR(255));
GO
CREATE VIEW app.active_users AS SELECT id, email FROM app.users;
GO
`
	schema, err := NewSQLServer().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Views, 1)
	assert.Contains(t, schema.Views[0].Name, "active_users")
}

func TestGenerateIndexSQL(t *testing.T) {
	s := &SQLServer{}

	plain := s.generateIndexSQL("users", sqlmapper.Index{Name: "idx_email", Columns: []string{"email"}})
	assert.Contains(t, plain, "INDEX idx_email")
	assert.Contains(t, plain, "NONCLUSTERED", "SQL Server names the default index kind explicitly")
	assert.Contains(t, plain, "users")

	unique := s.generateIndexSQL("users", sqlmapper.Index{Name: "uq_email", Columns: []string{"email"}, IsUnique: true})
	assert.Contains(t, unique, "UNIQUE")
}

func TestSQLServerGenerateNilSchema(t *testing.T) {
	_, err := NewSQLServer().Generate(nil)
	assert.Error(t, err)
}

func TestSQLServerParseEmptyContent(t *testing.T) {
	_, err := NewSQLServer().Parse("")
	assert.Error(t, err)
}

// TestSQLServerStreamParser_ParseRoutines drives the regex-based helpers that
// only the stream path uses.
func TestSQLServerStreamParser_ParseRoutines(t *testing.T) {
	const dump = `CREATE FUNCTION dbo.add_one(@n INT) RETURNS INT AS BEGIN RETURN @n + 1 END
GO
CREATE PROCEDURE dbo.touch_user(@uid INT) AS BEGIN SELECT @uid END
GO
CREATE TRIGGER dbo.trg_users ON dbo.users AFTER INSERT AS BEGIN SELECT 1 END
GO
`
	var functions, procedures, triggers int
	err := NewSQLServerStreamParser().ParseStream(strings.NewReader(dump), func(obj stream.SchemaObject) error {
		switch obj.Type {
		case stream.FunctionObject:
			functions++
		case stream.ProcedureObject:
			procedures++
		case stream.TriggerObject:
			triggers++
		}
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, 1, functions)
	assert.Equal(t, 1, procedures)
	assert.Equal(t, 1, triggers)
}

func TestSQLServerStreamParser_GenerateStreamObjects(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "INT", IsPrimaryKey: true},
				{Name: "email", DataType: "NVARCHAR", Length: 255},
			},
			Indexes: []sqlmapper.Index{{Name: "idx_email", Columns: []string{"email"}}},
		}},
		Views: []sqlmapper.View{{Name: "v", Definition: "SELECT id FROM users"}},
	}

	var out strings.Builder
	require.NoError(t, NewSQLServerStreamParser().GenerateStream(schema, &out))

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

// streamRich exercises every branch of both dispatchers, including the routine
// and index kinds the basic fixture leaves out.
const streamRich = `CREATE TABLE users (id INT IDENTITY(1,1) PRIMARY KEY, email NVARCHAR(255))
GO
CREATE VIEW active_users AS SELECT id FROM users
GO
CREATE FUNCTION dbo.add_one(@n INT) RETURNS INT AS BEGIN RETURN @n + 1 END
GO
CREATE PROCEDURE dbo.touch(@uid INT) AS BEGIN SELECT @uid END
GO
CREATE TRIGGER dbo.trg ON dbo.users AFTER INSERT AS BEGIN SELECT 1 END
GO
CREATE INDEX idx_email ON users (email)
GO
CREATE UNIQUE INDEX uq_email ON users (email)
GO
CREATE NONCLUSTERED INDEX nc_email ON users (email)
GO
`

func TestSQLServerStreamParser_RichDispatchAgrees(t *testing.T) {
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
		return NewSQLServerStreamParser().ParseStream(strings.NewReader(streamRich), cb)
	})
	parallel := count(func(cb func(stream.SchemaObject) error) error {
		return NewSQLServerStreamParser().ParseStreamParallel(strings.NewReader(streamRich), cb, 4)
	})

	assert.Equal(t, serial, parallel)
	assert.Equal(t, 3, serial[stream.IndexObject])
	assert.Equal(t, 1, serial[stream.TriggerObject])
}

func TestSQLServerStreamParser_GenerateStreamFullSchema(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "INT", IsPrimaryKey: true, AutoIncrement: true},
				{Name: "email", DataType: "NVARCHAR", Length: 255, IsNullable: false},
				{Name: "note", DataType: "NVARCHAR", Length: 50, DefaultValue: "none"},
			},
			Constraints: []sqlmapper.Constraint{
				{Name: "uq_email", Type: "UNIQUE", Columns: []string{"email"}},
			},
			Indexes: []sqlmapper.Index{{Name: "idx_email", Columns: []string{"email"}, IsUnique: true}},
		}},
		Views: []sqlmapper.View{{Name: "v", Definition: "SELECT id FROM users"}},
		Functions: []sqlmapper.Function{
			{Name: "f", Returns: "INT", Body: "BEGIN RETURN 1 END",
				Parameters: []sqlmapper.Parameter{{Name: "@n", DataType: "INT"}}},
			{Name: "p", IsProc: true, Body: "BEGIN SELECT 1 END",
				Parameters: []sqlmapper.Parameter{{Name: "@n", DataType: "INT"}}},
		},
		Triggers: []sqlmapper.Trigger{
			{Name: "trg", Timing: "AFTER", Event: "INSERT", Table: "users", Body: "BEGIN SELECT 1 END"},
		},
	}

	var out strings.Builder
	require.NoError(t, NewSQLServerStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "idx_email")
	assert.Contains(t, got, "CREATE VIEW v")
	assert.Contains(t, got, "CREATE TRIGGER trg")
}
