package postgres

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

const streamDump = `CREATE TYPE user_status AS ENUM ('active', 'banned');

CREATE TABLE users (
    id bigserial PRIMARY KEY,
    email character varying(255) NOT NULL,
    is_active boolean DEFAULT true NOT NULL
);

CREATE TABLE orders (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL
);

CREATE VIEW active_users AS
    SELECT id, email FROM users WHERE is_active;

CREATE INDEX idx_orders_user ON orders (user_id);
`

func TestPostgreSQLStreamParser_ParseStream(t *testing.T) {
	parser := NewPostgreSQLStreamParser()

	tables := map[string]*sqlmapper.Table{}
	var views []string
	var types []string
	var indexes []string

	err := parser.ParseStream(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		switch v := obj.Data.(type) {
		case *sqlmapper.Table:
			tables[v.Name] = v
		case *sqlmapper.View:
			views = append(views, v.Name)
		case *sqlmapper.Type:
			types = append(types, v.Name)
		case *sqlmapper.Index:
			indexes = append(indexes, v.Name)
		}
		return nil
	})
	require.NoError(t, err)

	require.Contains(t, tables, "users")
	require.Contains(t, tables, "orders")
	assert.Equal(t, []string{"active_users"}, views)
	assert.Equal(t, []string{"user_status"}, types)
	assert.Equal(t, []string{"idx_orders_user"}, indexes)
}

func TestPostgreSQLStreamParser_ParseStreamColumnDetail(t *testing.T) {
	parser := NewPostgreSQLStreamParser()

	var users *sqlmapper.Table
	err := parser.ParseStream(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		if table, ok := obj.Data.(*sqlmapper.Table); ok && table.Name == "users" {
			users = table
		}
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, users)

	require.Len(t, users.Columns, 3)
	assert.True(t, users.Columns[0].AutoIncrement)
	// "character varying(255)" has to survive statement-level parsing too.
	assert.Equal(t, "varchar", users.Columns[1].DataType)
	assert.Equal(t, 255, users.Columns[1].Length)
}

func TestPostgreSQLStreamParser_ParseStreamParallel(t *testing.T) {
	parser := NewPostgreSQLStreamParser()

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

// TestPostgreSQLStreamParser_ParallelIsRaceFree exists to be run under -race.
// The dialect parser embedded in the stream parser used to be shared by every
// worker, so concurrent statements raced on its schema pointer.
func TestPostgreSQLStreamParser_ParallelIsRaceFree(t *testing.T) {
	var big strings.Builder
	for i := range 40 {
		big.WriteString("CREATE TABLE t")
		big.WriteString(string(rune('a' + i%26)))
		big.WriteString(" (id bigserial PRIMARY KEY, name character varying(50) NOT NULL);\n")
	}

	parser := NewPostgreSQLStreamParser()
	var count int
	var mu sync.Mutex
	err := parser.ParseStreamParallel(strings.NewReader(big.String()), func(obj stream.SchemaObject) error {
		mu.Lock()
		count++
		mu.Unlock()
		return nil
	}, 8)
	require.NoError(t, err)
	assert.Equal(t, 40, count)
}

func TestPostgreSQLStreamParser_CallbackErrorAborts(t *testing.T) {
	sentinel := errors.New("stop here")

	err := NewPostgreSQLStreamParser().ParseStream(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func TestPostgreSQLStreamParser_GenerateStream(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name: "users",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "bigint", AutoIncrement: true, IsPrimaryKey: true},
					{Name: "email", DataType: "varchar", Length: 255, IsNullable: false},
				},
				Indexes: []sqlmapper.Index{
					{Name: "idx_email", Columns: []string{"email"}, IsUnique: true},
				},
			},
		},
	}

	var out strings.Builder
	require.NoError(t, NewPostgreSQLStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "email VARCHAR(255) NOT NULL")
	assert.Contains(t, got, "CREATE UNIQUE INDEX idx_email ON users")
}

func TestPostgreSQLStreamParser_GenerateStreamNilSchema(t *testing.T) {
	var out strings.Builder
	assert.Error(t, NewPostgreSQLStreamParser().GenerateStream(nil, &out))
}

func TestEnsureTerminated(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"appends the missing delimiter", "CREATE TABLE t (id int)", "CREATE TABLE t (id int);"},
		{"keeps an existing delimiter", "CREATE TABLE t (id int);", "CREATE TABLE t (id int);"},
		{"collapses newlines", "CREATE VIEW v AS\n  SELECT 1", "CREATE VIEW v AS SELECT 1;"},
		{"empty input stays empty", "   \n  ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ensureTerminated(tt.input))
		})
	}
}

// streamRoutines exercises the object types that only appear in richer dumps.
const streamRoutines = `CREATE FUNCTION add_one(n integer) RETURNS integer AS $$ SELECT n + 1 $$ LANGUAGE sql;
CREATE PROCEDURE touch_user(uid integer) LANGUAGE plpgsql AS $$ BEGIN END $$;
CREATE TRIGGER trg_users BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION set_created_at();
GRANT SELECT ON TABLE users TO reporting;
`

func TestPostgreSQLStreamParser_ParseRoutines(t *testing.T) {
	parser := NewPostgreSQLStreamParser()

	var functions, procedures, triggers, permissions int
	err := parser.ParseStream(strings.NewReader(streamRoutines), func(obj stream.SchemaObject) error {
		switch obj.Type {
		case stream.FunctionObject:
			functions++
		case stream.ProcedureObject:
			procedures++
		case stream.TriggerObject:
			triggers++
		case stream.PermissionObject:
			permissions++
		}
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, 1, functions)
	assert.Equal(t, 1, procedures)
	assert.Equal(t, 1, triggers)
	assert.Equal(t, 1, permissions)
}

func TestPostgreSQLStreamParser_GenerateStreamTypes(t *testing.T) {
	schema := &sqlmapper.Schema{
		Types: []sqlmapper.Type{
			{Name: "mood", Kind: "ENUM", Definition: "'happy', 'sad'"},
		},
		Tables: []sqlmapper.Table{
			{Name: "t", Columns: []sqlmapper.Column{{Name: "id", DataType: "integer"}}},
		},
	}

	var out strings.Builder
	require.NoError(t, NewPostgreSQLStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE TYPE mood AS ENUM")
	assert.Contains(t, got, "CREATE TABLE t")
}

// TestPostgreSQLStreamParser_SerialAndParallelAgree pins the two dispatchers together.
// They are separate switch statements, and they had drifted: one of them was
// missing a branch, so an object kind survived a serial parse and vanished from
// a parallel one.
func TestPostgreSQLStreamParser_SerialAndParallelAgree(t *testing.T) {
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

	for name, dump := range map[string]string{"objects": streamDump, "routines": streamRoutines} {
		t.Run(name, func(t *testing.T) {
			serial := count(func(cb func(stream.SchemaObject) error) error {
				return NewPostgreSQLStreamParser().ParseStream(strings.NewReader(dump), cb)
			})
			parallel := count(func(cb func(stream.SchemaObject) error) error {
				return NewPostgreSQLStreamParser().ParseStreamParallel(strings.NewReader(dump), cb, 4)
			})
			assert.Equal(t, serial, parallel)
		})
	}
}
