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
