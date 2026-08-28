package sqlite

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
)

func TestSQLite_Parse(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  bool
		validate func(*testing.T, *sqlmapper.Schema)
	}{
		{
			name:    "Empty content",
			content: "",
			wantErr: true,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Nil(t, schema)
			},
		},
		{
			name:    "Valid content",
			content: "CREATE TABLE test (id INTEGER PRIMARY KEY);",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
			},
		},
		{
			name:    "CREATE TABLE",
			content: "CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT);",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// Additional validation logic can be added here
			},
		},
		{
			name:    "CREATE INDEX",
			content: "CREATE TABLE test (id INTEGER PRIMARY KEY, name TEXT); CREATE INDEX idx_name ON test (name);",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				assert.Len(t, schema.Tables, 1)
				assert.Len(t, schema.Tables[0].Indexes, 1)
				assert.Equal(t, "idx_name", schema.Tables[0].Indexes[0].Name)
				assert.Equal(t, []string{"name"}, schema.Tables[0].Indexes[0].Columns)
			},
		},
		{
			name:    "CREATE VIEW",
			content: "CREATE VIEW test_view AS SELECT id, name FROM test;",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// Additional validation logic for views can be added here
			},
		},
		{
			name:    "CREATE TRIGGER",
			content: "CREATE TRIGGER test_trigger AFTER INSERT ON test BEGIN UPDATE test SET name = 'updated' WHERE id = NEW.id; END;",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// Additional validation logic for triggers can be added here
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLite()
			schema, err := s.Parse(tt.content)
			if (err != nil) != tt.wantErr {
				t.Errorf("Parse() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.validate != nil {
				tt.validate(t, schema)
			}
		})
	}
}

func TestSQLite_Generate(t *testing.T) {
	tests := []struct {
		name    string
		schema  *sqlmapper.Schema
		want    string
		wantErr bool
	}{
		{
			name:    "Nil schema",
			schema:  nil,
			wantErr: true,
		},
		{
			name: "Basic schema with one table",
			schema: &sqlmapper.Schema{
				Tables: []sqlmapper.Table{
					{
						Name: "users",
						Columns: []sqlmapper.Column{
							{Name: "id", DataType: "INTEGER", IsPrimaryKey: true},
							{Name: "name", DataType: "TEXT", Length: 100, IsNullable: false},
							{Name: "email", DataType: "TEXT", Length: 255, IsNullable: false, IsUnique: true},
						},
					},
				},
			},
			want: strings.TrimSpace(`
CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE
);`),
			wantErr: false,
		},
		{
			name: "Schema with table and indexes",
			schema: &sqlmapper.Schema{
				Tables: []sqlmapper.Table{
					{
						Name: "products",
						Columns: []sqlmapper.Column{
							{Name: "id", DataType: "INTEGER", IsPrimaryKey: true},
							{Name: "name", DataType: "TEXT", Length: 100, IsNullable: false},
							{Name: "price", DataType: "REAL", Length: 10, Scale: 2, IsNullable: true},
						},
						Indexes: []sqlmapper.Index{
							{Name: "idx_name", Columns: []string{"name"}},
							{Name: "idx_price", Columns: []string{"price"}, IsUnique: true},
						},
					},
				},
			},
			want: strings.TrimSpace(`
CREATE TABLE products (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    price REAL(10,2)
);
CREATE INDEX idx_name ON products(name);
CREATE UNIQUE INDEX idx_price ON products(price);`),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLite()
			result, err := s.Generate(tt.schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("Generate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.want != "" {
				assert.Equal(t, tt.want, strings.TrimSpace(result))
			}
		})
	}
}

func TestSQLite_Generate_ComplexSchema(t *testing.T) {
	schema := &sqlmapper.Schema{
		// Assuming a complex schema object with tables, views, and triggers
	}
	s := NewSQLite()
	_, err := s.Generate(schema)
	assert.NoError(t, err)
}

// A trigger body spans several statements of its own. Splitting the file on
// every semicolon cut the body at its first inner statement and dropped the rest
// of the file with it, so the trigger was registered with an empty body and the
// tables after it went missing.
func TestSQLite_ParseTriggerBody(t *testing.T) {
	const ddl = `CREATE TABLE users (id INTEGER PRIMARY KEY, updated_at TEXT);
CREATE TRIGGER touch_users AFTER UPDATE ON users
BEGIN
  UPDATE users SET updated_at = 'first' WHERE id = NEW.id;
  UPDATE users SET updated_at = 'second' WHERE id = NEW.id;
END;
CREATE TABLE orders (id INTEGER PRIMARY KEY);`

	schema, err := NewSQLite().Parse(ddl)
	assert.NoError(t, err)

	assert.Len(t, schema.Tables, 2, "the table after the trigger has to survive")
	assert.Equal(t, "orders", schema.Tables[1].Name)

	assert.Len(t, schema.Triggers, 1)
	trigger := schema.Triggers[0]
	assert.Equal(t, "touch_users", trigger.Name)
	assert.Equal(t, "users", trigger.Table)
	assert.Equal(t, "AFTER", trigger.Timing)
	assert.Equal(t, "UPDATE", trigger.Event)
	assert.Contains(t, trigger.Body, "'first'")
	assert.Contains(t, trigger.Body, "'second'", "the whole body has to survive")
	assert.NotContains(t, trigger.Body, "CREATE TABLE orders")
}

// The event is read off the header, not the whole statement. A trigger that
// fires on INSERT while its body runs an UPDATE used to be recorded as UPDATE.
func TestSQLite_ParseTriggerEventComesFromTheHeader(t *testing.T) {
	const ddl = `CREATE TRIGGER log_insert AFTER INSERT ON users
BEGIN
  UPDATE counters SET n = n + 1;
END;`

	schema, err := NewSQLite().Parse(ddl)
	assert.NoError(t, err)
	assert.Len(t, schema.Triggers, 1)
	assert.Equal(t, "INSERT", schema.Triggers[0].Event)
	assert.Equal(t, "users", schema.Triggers[0].Table)
}
