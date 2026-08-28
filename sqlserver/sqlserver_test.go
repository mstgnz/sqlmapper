package sqlserver

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
)

func TestSQLServer_Parse(t *testing.T) {
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
			name:    "CREATE TABLE",
			content: "CREATE TABLE test (id INT PRIMARY KEY, name NVARCHAR(50));",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// Additional validation logic can be added here
			},
		},
		{
			name:    "CREATE INDEX",
			content: "CREATE TABLE test (id INT PRIMARY KEY, name NVARCHAR(50)); CREATE INDEX idx_name ON test (name);",
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
			name:    "ALTER TABLE",
			content: "ALTER TABLE test ADD COLUMN email NVARCHAR(100);",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// Additional validation logic can be added here
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
			content: "CREATE TRIGGER trg_test AFTER INSERT ON test FOR EACH ROW BEGIN UPDATE test SET name = 'updated' WHERE id = NEW.id; END;",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// Additional validation logic for triggers can be added here
			},
		},
		{
			name:    "ALTER TABLE with CONSTRAINT",
			content: "ALTER TABLE test ADD CONSTRAINT chk_name CHECK (name IS NOT NULL);",
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// Additional validation logic for constraints can be added here
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLServer()
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

func TestSQLServer_Generate(t *testing.T) {
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
							{Name: "id", DataType: "INT", IsPrimaryKey: true},
							{Name: "name", DataType: "NVARCHAR", Length: 100, IsNullable: false},
							{Name: "email", DataType: "NVARCHAR", Length: 255, IsNullable: false, IsUnique: true},
						},
					},
				},
			},
			want: strings.TrimSpace(`
CREATE TABLE users (
    id INT PRIMARY KEY,
    name NVARCHAR(100) NOT NULL,
    email NVARCHAR(255) NOT NULL UNIQUE
);
GO`),
			wantErr: false,
		},
		{
			name: "Schema with table and indexes",
			schema: &sqlmapper.Schema{
				Tables: []sqlmapper.Table{
					{
						Name: "products",
						Columns: []sqlmapper.Column{
							{Name: "id", DataType: "INT", IsPrimaryKey: true},
							{Name: "name", DataType: "NVARCHAR", Length: 100, IsNullable: false},
							{Name: "price", DataType: "DECIMAL", Length: 10, Scale: 2, IsNullable: true},
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
    id INT PRIMARY KEY,
    name NVARCHAR(100) NOT NULL,
    price DECIMAL(10,2)
);
GO
CREATE NONCLUSTERED INDEX idx_name ON products (name);
GO
CREATE UNIQUE NONCLUSTERED INDEX idx_price ON products (price);
GO`),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSQLServer()
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

func TestSQLServer_Generate_ComplexSchema(t *testing.T) {
	schema := &sqlmapper.Schema{
		// Assuming a complex schema object with tables, views, and triggers
	}
	s := NewSQLServer()
	_, err := s.Generate(schema)
	assert.NoError(t, err)
}

// T-SQL bodies carry semicolons of their own and are terminated by GO. Splitting
// on the semicolon cut the body at its first inner statement.
func TestSQLServer_ParseRoutineBody(t *testing.T) {
	const ddl = `CREATE TABLE users (id INT PRIMARY KEY)
GO
CREATE TRIGGER touch_users ON users AFTER INSERT AS
BEGIN
  SELECT 1;
  SELECT 2;
END
GO
CREATE TABLE orders (id INT PRIMARY KEY)
GO`

	schema, err := NewSQLServer().Parse(ddl)
	assert.NoError(t, err)

	assert.Len(t, schema.Tables, 2, "the table after the trigger has to survive")
	assert.Equal(t, "orders", schema.Tables[1].Name)

	assert.Len(t, schema.Triggers, 1)
	assert.Contains(t, schema.Triggers[0].Body, "SELECT 2", "the whole body has to survive")
}

func TestNormalizeSQLServerTypeName(t *testing.T) {
	tests := map[string]string{
		"NVARCHAR":         "varchar",
		"varchar":          "varchar",
		"NCHAR":            "char",
		"ntext":            "text",
		"bit":              "smallint",
		"TINYINT":          "smallint",
		"datetime2":        "timestamp",
		"smalldatetime":    "timestamp",
		"DATETIMEOFFSET":   "timestamp with time zone",
		"uniqueidentifier": "uuid",
		"money":            "decimal",
		"smallmoney":       "decimal",
		"image":            "blob",
		"VARBINARY":        "blob",
		"real":             "real",
		"float":            "double precision",
		"xml":              "text",
		// Only the switch collapses whitespace; the fallback lower-cases the
		// name and leaves it as written.
		"CHARACTER   VARYING":   "character   varying",
		"SomethingUnrecognised": "somethingunrecognised",
	}

	for in, want := range tests {
		assert.Equal(t, want, normalizeSQLServerTypeName(in), "type %q", in)
	}
}

func TestIsNumericLiteral(t *testing.T) {
	for _, s := range []string{"0", "42", "-1", "3.14"} {
		assert.True(t, isNumericLiteral(s), "%q should be numeric", s)
	}
	for _, s := range []string{"", "abc", "1a", "N'x'"} {
		assert.False(t, isNumericLiteral(s), "%q should not be numeric", s)
	}
}

func TestColumnIsAutoIncrement(t *testing.T) {
	table := sqlmapper.Table{Columns: []sqlmapper.Column{
		{Name: "id", AutoIncrement: true},
		{Name: "email"},
	}}

	assert.True(t, columnIsAutoIncrement(table, "id"))
	assert.False(t, columnIsAutoIncrement(table, "email"))
	assert.False(t, columnIsAutoIncrement(table, "absent"))
}

func TestHasNamedUnique(t *testing.T) {
	constraints := []sqlmapper.Constraint{
		{Type: "UNIQUE", Columns: []string{"email"}},
		{Type: "UNIQUE", Columns: []string{"a", "b"}},
		{Type: "PRIMARY KEY", Columns: []string{"id"}},
	}

	assert.True(t, hasNamedUnique(constraints, "email"))
	assert.False(t, hasNamedUnique(constraints, "a"), "a composite unique does not cover one column")
	assert.False(t, hasNamedUnique(constraints, "id"))
}

func TestIsDeferred(t *testing.T) {
	named := sqlmapper.Constraint{Name: "fk_orders_user", RefTable: "users", Columns: []string{"user_id"}}
	anonymous := sqlmapper.Constraint{RefTable: "users", Columns: []string{"user_id"}}
	other := sqlmapper.Constraint{RefTable: "carts", Columns: []string{"cart_id"}}

	assert.True(t, isDeferred([]sqlmapper.Constraint{named}, named), "matched by name")
	assert.True(t, isDeferred([]sqlmapper.Constraint{anonymous}, anonymous), "matched by target when unnamed")
	assert.False(t, isDeferred([]sqlmapper.Constraint{anonymous}, other))
	assert.False(t, isDeferred(nil, named))
}
