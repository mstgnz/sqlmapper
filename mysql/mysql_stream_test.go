package mysql

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

const streamDump = "CREATE TABLE users (\n" +
	"  id INT AUTO_INCREMENT PRIMARY KEY,\n" +
	"  email VARCHAR(255) NOT NULL,\n" +
	"  status ENUM('active','banned') NOT NULL DEFAULT 'active'\n" +
	");\n" +
	"\n" +
	"CREATE TABLE orders (\n" +
	"  id INT AUTO_INCREMENT PRIMARY KEY,\n" +
	"  user_id INT NOT NULL\n" +
	");\n" +
	"\n" +
	"CREATE VIEW active_users AS SELECT id, email FROM users WHERE status = 'active';\n"

// collect drains a stream parse into a map of object type to names.
func collect(t *testing.T, run func(cb func(stream.SchemaObject) error) error) map[stream.SchemaObjectType][]string {
	t.Helper()
	found := map[stream.SchemaObjectType][]string{}
	err := run(func(obj stream.SchemaObject) error {
		switch v := obj.Data.(type) {
		case *sqlmapper.Table:
			found[obj.Type] = append(found[obj.Type], v.Name)
		case *sqlmapper.View:
			found[obj.Type] = append(found[obj.Type], v.Name)
		case *sqlmapper.Function:
			found[obj.Type] = append(found[obj.Type], v.Name)
		case *sqlmapper.Procedure:
			found[obj.Type] = append(found[obj.Type], v.Name)
		case *sqlmapper.Trigger:
			found[obj.Type] = append(found[obj.Type], v.Name)
		default:
			t.Fatalf("unexpected object payload %T", obj.Data)
		}
		return nil
	})
	require.NoError(t, err)
	return found
}

func TestMySQLStreamParser_ParseStream(t *testing.T) {
	parser := NewMySQLStreamParser()

	found := collect(t, func(cb func(stream.SchemaObject) error) error {
		return parser.ParseStream(strings.NewReader(streamDump), cb)
	})

	assert.Equal(t, []string{"users", "orders"}, found[stream.TableObject])
	// A view arrives without its trailing delimiter and spread over several
	// lines; both used to stop it from being recognised at all.
	assert.Equal(t, []string{"active_users"}, found[stream.ViewObject])
}

func TestMySQLStreamParser_ParseStreamColumnDetail(t *testing.T) {
	parser := NewMySQLStreamParser()

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
	assert.Equal(t, 255, users.Columns[1].Length)
	assert.Equal(t, []string{"active", "banned"}, users.Columns[2].EnumValues)
}

func TestMySQLStreamParser_ParseStreamParallel(t *testing.T) {
	parser := NewMySQLStreamParser()

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

	// Order is not guaranteed in parallel mode, only membership.
	assert.ElementsMatch(t, []string{"users", "orders"}, names)
}

// TestMySQLStreamParser_ParallelIsRaceFree exists to be run under -race. The
// dialect parser embedded in the stream parser used to be shared by every
// worker, so concurrent statements raced on its schema pointer.
func TestMySQLStreamParser_ParallelIsRaceFree(t *testing.T) {
	var big strings.Builder
	for i := range 40 {
		big.WriteString("CREATE TABLE t")
		big.WriteString(string(rune('a' + i%26)))
		big.WriteString(" (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(50) NOT NULL);\n")
	}

	parser := NewMySQLStreamParser()
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

func TestMySQLStreamParser_CallbackErrorAborts(t *testing.T) {
	parser := NewMySQLStreamParser()
	sentinel := errors.New("stop here")

	err := parser.ParseStream(strings.NewReader(streamDump), func(obj stream.SchemaObject) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)
}

func TestMySQLStreamParser_GenerateStream(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name: "users",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "int", AutoIncrement: true, IsPrimaryKey: true},
					{Name: "email", DataType: "varchar", Length: 255, IsNullable: false},
				},
				Indexes: []sqlmapper.Index{
					{Name: "idx_email", Columns: []string{"email"}, IsUnique: true},
				},
			},
		},
		Views: []sqlmapper.View{
			{Name: "active_users", Definition: "SELECT id FROM users"},
		},
	}

	var out strings.Builder
	require.NoError(t, NewMySQLStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "email VARCHAR(255) NOT NULL")
	assert.Contains(t, got, "CREATE UNIQUE INDEX idx_email ON users(email)")
	assert.Contains(t, got, "CREATE VIEW active_users AS SELECT id FROM users")
}

func TestMySQLStreamParser_GenerateStreamNilSchema(t *testing.T) {
	var out strings.Builder
	err := NewMySQLStreamParser().GenerateStream(nil, &out)
	assert.Error(t, err)
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

// streamRoutines exercises the object types that only appear in richer dumps.
// The bodies deliberately contain no semicolon: the stream reader splits on the
// delimiter, so a multi-statement routine body cannot survive it. That limit is
// documented in docs/stream_processing.md.
const streamRoutines = "CREATE FUNCTION add_one(n INT) RETURNS INT BEGIN RETURN n + 1 END;\n" +
	"CREATE PROCEDURE touch_user(IN uid INT) BEGIN SELECT uid END;\n" +
	"CREATE TRIGGER trg_users BEFORE INSERT ON users FOR EACH ROW BEGIN SET NEW.created_at = NOW() END;\n"

func TestMySQLStreamParser_ParseRoutines(t *testing.T) {
	parser := NewMySQLStreamParser()

	var functions, procedures, triggers []string
	err := parser.ParseStream(strings.NewReader(streamRoutines), func(obj stream.SchemaObject) error {
		switch v := obj.Data.(type) {
		case *sqlmapper.Function:
			functions = append(functions, v.Name)
		case *sqlmapper.Procedure:
			procedures = append(procedures, v.Name)
		case *sqlmapper.Trigger:
			triggers = append(triggers, v.Name)
		}
		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"add_one"}, functions)
	assert.Equal(t, []string{"touch_user"}, procedures)
	assert.Equal(t, []string{"trg_users"}, triggers)
}

func TestMySQLStreamParser_GenerateStreamRoutines(t *testing.T) {
	schema := &sqlmapper.Schema{
		Functions: []sqlmapper.Function{
			{Name: "add_one", Returns: "INT", Body: "BEGIN RETURN n + 1 END",
				Parameters: []sqlmapper.Parameter{{Name: "n", DataType: "INT"}}},
			{Name: "touch_user", IsProc: true, Body: "BEGIN SELECT 1 END",
				Parameters: []sqlmapper.Parameter{{Name: "uid", DataType: "INT"}}},
		},
		Triggers: []sqlmapper.Trigger{
			{Name: "trg", Timing: "BEFORE", Event: "INSERT", Table: "users", Body: "BEGIN SET NEW.x = 1 END"},
		},
	}

	var out strings.Builder
	require.NoError(t, NewMySQLStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE FUNCTION add_one(n INT) RETURNS INT")
	assert.Contains(t, got, "CREATE PROCEDURE touch_user(uid INT)")
	assert.Contains(t, got, "CREATE TRIGGER trg BEFORE INSERT ON users")
}

// TestMySQLStreamParser_SerialAndParallelAgree pins the two dispatchers together.
// They are separate switch statements, and they had drifted: one of them was
// missing a branch, so an object kind survived a serial parse and vanished from
// a parallel one.
func TestMySQLStreamParser_SerialAndParallelAgree(t *testing.T) {
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
				return NewMySQLStreamParser().ParseStream(strings.NewReader(dump), cb)
			})
			parallel := count(func(cb func(stream.SchemaObject) error) error {
				return NewMySQLStreamParser().ParseStreamParallel(strings.NewReader(dump), cb, 4)
			})
			assert.Equal(t, serial, parallel)
		})
	}
}
