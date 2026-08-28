package sqlserver

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
				// Every script opens with the SET options a filtered index needs,
				// the way SSMS writes them; the case below is about what follows.
				assert.Equal(t, tt.want, strings.TrimSpace(strings.TrimPrefix(result, mssScriptPreamble)))
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

// SQL Server keeps a comment in an extended property rather than in the DDL,
// and nothing read those, so a commented schema arrived with none of them.
func TestSQLServer_ExtendedPropertyComments(t *testing.T) {
	const script = `CREATE TABLE [dbo].[customers](
    [id] [bigint] NOT NULL,
    [email] [nvarchar](255) NOT NULL
)
GO
EXEC sys.sp_addextendedproperty @name=N'MS_Description', @value=N'people who buy things',
    @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'customers';
GO
EXEC sys.sp_addextendedproperty @name=N'MS_Description', @value=N'login address',
    @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'customers',
    @level2type=N'COLUMN', @level2name=N'email';
GO
EXEC sys.sp_addextendedproperty @name=N'Something_Else', @value=N'ignored',
    @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'customers';
GO
EXEC sys.sp_addextendedproperty @name=N'MS_Description', @value=N'no such table',
    @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'absent';
GO
`

	schema, err := NewSQLServer().Parse(script)
	assert.NoError(t, err)
	assert.Len(t, schema.Tables, 1)

	table := schema.Tables[0]
	assert.Equal(t, "people who buy things", table.Comment,
		"a property that is not MS_Description does not overwrite the comment")
	assert.Equal(t, "login address", table.Columns[1].Comment)

	// And they go back out the way SQL Server stores them.
	out, err := NewSQLServer().Generate(schema)
	assert.NoError(t, err)
	assert.Contains(t, out, "sp_addextendedproperty")
	assert.Contains(t, out, "@value=N'people who buy things'")
	assert.Contains(t, out, "@level2type=N'COLUMN', @level2name=N'email'")
}

func TestMSSReferentialAction(t *testing.T) {
	tests := map[string]string{
		"":            "",
		"CASCADE":     "CASCADE",
		"cascade":     "CASCADE",
		"SET NULL":    "SET NULL",
		"SET DEFAULT": "SET DEFAULT",
		"NO ACTION":   "NO ACTION",
		// SQL Server has no RESTRICT; the standard says it behaves as NO ACTION.
		"RESTRICT": "NO ACTION",
		"NONSENSE": "",
	}

	for in, want := range tests {
		if got := mssReferentialAction(in); got != want {
			t.Errorf("mssReferentialAction(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSQLServerSequences pins the object SQL Server has had since 2012 and this
// package read none of: every sequence in a PostgreSQL or Oracle schema was
// dropped on the way in and on the way out.
func TestSQLServerSequences(t *testing.T) {
	const script = `CREATE TABLE t (id INT);
GO
CREATE SEQUENCE dbo.order_seq AS BIGINT START WITH 100 INCREMENT BY 5 MINVALUE 1 MAXVALUE 9999 CACHE 20;
GO
CREATE SEQUENCE plain_seq;
GO
CREATE SEQUENCE quiet_seq START WITH 7 INCREMENT BY 1 NO CACHE CYCLE;
GO`

	schema, err := NewSQLServer().Parse(script)
	require.NoError(t, err)
	require.Len(t, schema.Sequences, 3)

	full := schema.Sequences[0]
	assert.Equal(t, "order_seq", full.Name)
	assert.Equal(t, "dbo", full.Schema)
	assert.Equal(t, 100, full.StartValue)
	assert.Equal(t, 5, full.IncrementBy)
	assert.Equal(t, 1, full.MinValue)
	assert.Equal(t, 9999, full.MaxValue)
	assert.Equal(t, 20, full.Cache)
	assert.False(t, full.Cycle)

	// A sequence with no options at all still starts at one and steps by one,
	// which is what SQL Server itself does.
	assert.Equal(t, 1, schema.Sequences[1].StartValue)
	assert.Equal(t, 1, schema.Sequences[1].IncrementBy)

	// NO CACHE is a cache of none, which the schema carries as one: that is how
	// PostgreSQL states the same thing.
	assert.Equal(t, 1, schema.Sequences[2].Cache)
	assert.True(t, schema.Sequences[2].Cycle)

	out, err := NewSQLServer().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "CREATE SEQUENCE order_seq AS BIGINT START WITH 100 INCREMENT BY 5")
	assert.Contains(t, out, "MAXVALUE 9999 CACHE 20")
	assert.Contains(t, out, "NO CACHE")
	assert.Contains(t, out, "CYCLE")

	// The stream reads the same statement into the same object.
	var streamed []sqlmapper.Sequence
	require.NoError(t, NewSQLServerStreamParser().ParseStream(strings.NewReader(script), func(obj stream.SchemaObject) error {
		if obj.Type == stream.SequenceObject {
			streamed = append(streamed, *obj.Data.(*sqlmapper.Sequence))
		}
		return nil
	}))
	require.Len(t, streamed, 3)
	assert.Equal(t, schema.Sequences[0], streamed[0])
}

// TestSQLServerFilteredIndex pins the clause that turns a filtered index into a
// full one when it is dropped. A filtered UNIQUE index widened that way is
// stricter than the source and starts rejecting rows that were legal before.
func TestSQLServerFilteredIndex(t *testing.T) {
	const script = `CREATE TABLE customers (id INT, email VARCHAR(255), is_active BIT);
GO
CREATE UNIQUE NONCLUSTERED INDEX IX_active_email ON dbo.customers (email ASC) WHERE ([is_active] = 1);
GO
CREATE NONCLUSTERED INDEX IX_plain ON dbo.customers (id ASC);
GO`

	schema, err := NewSQLServer().Parse(script)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)
	require.Len(t, schema.Tables[0].Indexes, 2)

	filtered := schema.Tables[0].Indexes[0]
	assert.Equal(t, "IX_active_email", filtered.Name)
	assert.True(t, filtered.IsUnique)
	// The brackets are stripped so the other four dialects can read the clause.
	assert.Equal(t, "is_active = 1", filtered.Condition)
	assert.Empty(t, schema.Tables[0].Indexes[1].Condition)

	out, err := NewSQLServer().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "WHERE is_active = 1")

	// The stream used to carry a second regex for the same job, without the
	// filter, so a filtered index read a statement at a time came back full.
	var streamed []sqlmapper.Index
	require.NoError(t, NewSQLServerStreamParser().ParseStream(strings.NewReader(script), func(obj stream.SchemaObject) error {
		if obj.Type == stream.IndexObject {
			streamed = append(streamed, *obj.Data.(*sqlmapper.Index))
		}
		return nil
	}))
	require.Len(t, streamed, 2)
	assert.Equal(t, "is_active = 1", streamed[0].Condition)
}

// TestSQLServerPermissions pins access control, which nothing read here and
// nothing wrote anywhere.
func TestSQLServerPermissions(t *testing.T) {
	const script = `CREATE TABLE customers (id INT);
GO
GRANT SELECT, INSERT ON dbo.customers TO reporting;
GO
GRANT ALL PRIVILEGES ON dbo.customers TO admin WITH GRANT OPTION;
GO
REVOKE DELETE ON dbo.customers FROM reporting;
GO`

	schema, err := NewSQLServer().Parse(script)
	require.NoError(t, err)
	require.Len(t, schema.Permissions, 3)

	assert.Equal(t, "GRANT", schema.Permissions[0].Type)
	assert.Equal(t, []string{"SELECT", "INSERT"}, schema.Permissions[0].Privileges)
	assert.Equal(t, "dbo.customers", schema.Permissions[0].Object)
	assert.Equal(t, "reporting", schema.Permissions[0].Grantee)
	assert.False(t, schema.Permissions[0].WithGrant)

	assert.True(t, schema.Permissions[1].WithGrant)
	assert.Equal(t, "REVOKE", schema.Permissions[2].Type)

	out, err := NewSQLServer().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "GRANT SELECT, INSERT ON customers TO reporting;")
	// T-SQL has no ALL PRIVILEGES: the widest object privilege is ALL.
	assert.Contains(t, out, "GRANT ALL ON customers TO admin WITH GRANT OPTION;")
	assert.Contains(t, out, "REVOKE DELETE ON customers FROM reporting;")
	// The source's schema qualifier names nothing once the table is written bare.
	assert.NotContains(t, out, "ON dbo.customers")

	// A grant with no grantee or no object is not a statement anything can load.
	empty, err := NewSQLServer().Generate(&sqlmapper.Schema{
		Permissions: []sqlmapper.Permission{{Type: "GRANT", Object: "t"}, {Type: "GRANT", Grantee: "x"}},
	})
	require.NoError(t, err)
	assert.NotContains(t, empty, "GRANT")
}
