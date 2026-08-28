package sqlite

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestSQLiteDefaultValue(t *testing.T) {
	tests := map[string]string{
		"n INTEGER DEFAULT 1":          "1",
		"n INTEGER NOT NULL DEFAULT 0": "0",
		// The schema holds the value, not the literal: the generator quotes
		// it again for whichever dialect it is writing.
		"s TEXT DEFAULT 'draft'":                    "draft",
		"s TEXT DEFAULT 'two words'":                "two words",
		"t TEXT DEFAULT CURRENT_TIMESTAMP":          "CURRENT_TIMESTAMP",
		"t TEXT DEFAULT CURRENT_TIMESTAMP NOT NULL": "CURRENT_TIMESTAMP",
		"t TEXT DEFAULT (datetime('now'))":          "(datetime('now'))",
		"n INTEGER":                                 "",
		"n INTEGER DEFAULT":                         "",
		"s TEXT DEFAULT 'unterminated":              "'unterminated",
	}

	for def, want := range tests {
		if got := sqliteDefaultValue([]byte(def)); got != want {
			t.Errorf("sqliteDefaultValue(%q) = %q, want %q", def, got, want)
		}
	}
}

func TestSQLiteColumnType(t *testing.T) {
	tests := []struct {
		col  sqlmapper.Column
		want string
	}{
		{sqlmapper.Column{DataType: "bigint"}, "INTEGER"},
		{sqlmapper.Column{DataType: "VARCHAR(255)"}, "TEXT"},
		{sqlmapper.Column{DataType: "varchar", Length: 255}, "TEXT"},
		{sqlmapper.Column{DataType: "numeric", Length: 10, Scale: 2}, "NUMERIC(10,2)"},
		{sqlmapper.Column{DataType: "numeric", Length: 10}, "NUMERIC(10)"},
		{sqlmapper.Column{DataType: "real", Length: 10, Scale: 2}, "REAL(10,2)"},
		{sqlmapper.Column{DataType: "text", IsArray: true}, "TEXT"},
		// A name SQLite does not know is left as written rather than guessed at.
		{sqlmapper.Column{DataType: "geography"}, "GEOGRAPHY"},
	}

	for _, tt := range tests {
		if got := sqliteColumnType(tt.col); got != tt.want {
			t.Errorf("sqliteColumnType(%+v) = %q, want %q", tt.col, got, tt.want)
		}
	}
}

func TestSQLiteTableConstraintRejectsAColumn(t *testing.T) {
	if _, ok := parseSQLiteTableConstraint([]byte("id INTEGER NOT NULL")); ok {
		t.Error("a column definition is not a table constraint")
	}
	if _, ok := parseSQLiteTableConstraint([]byte("PRIMARY KEY (a, b)")); !ok {
		t.Error("a table-level primary key is a constraint")
	}
}

func TestIsSQLiteInternalTable(t *testing.T) {
	for _, name := range []string{"sqlite_sequence", "sqlite_stat1", "SQLITE_MASTER"} {
		if !isSQLiteInternalTable(name) {
			t.Errorf("%q belongs to SQLite", name)
		}
	}
	for _, name := range []string{"customers", "sqlitex", "my_sqlite_table"} {
		if isSQLiteInternalTable(name) {
			t.Errorf("%q is a normal table", name)
		}
	}
}

// TestSQLiteGeneratedColumn pins the computed column, which SQLite has had
// since 3.31 and this package read as a plain column that computes nothing.
func TestSQLiteGeneratedColumn(t *testing.T) {
	const script = `CREATE TABLE t (
    a INTEGER,
    b INTEGER GENERATED ALWAYS AS (a * 2) VIRTUAL,
    c INTEGER GENERATED ALWAYS AS (a + 1) STORED,
    d TEXT DEFAULT 'as (x)'
);`

	schema, err := NewSQLite().Parse(script)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)
	require.Len(t, schema.Tables[0].Columns, 4)

	cols := map[string]sqlmapper.Column{}
	for _, c := range schema.Tables[0].Columns {
		cols[c.Name] = c
	}

	assert.Equal(t, "a * 2", cols["b"].GeneratedExpression)
	assert.False(t, cols["b"].GeneratedStored)
	assert.Equal(t, "INTEGER", cols["b"].DataType)

	assert.Equal(t, "a + 1", cols["c"].GeneratedExpression)
	assert.True(t, cols["c"].GeneratedStored)

	// A default whose value happens to read like the clause is not one.
	assert.Empty(t, cols["d"].GeneratedExpression)
	assert.Equal(t, "as (x)", cols["d"].DefaultValue)

	out, err := NewSQLite().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "GENERATED ALWAYS AS (a * 2) VIRTUAL")
	assert.Contains(t, out, "GENERATED ALWAYS AS (a + 1) STORED")

	// SQLite is the only target that takes a column with no type at all, which
	// is what a SQL Server source gives it, and it must not come out with the
	// gap an empty type leaves behind.
	untyped := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
		Name: "t",
		Columns: []sqlmapper.Column{
			{Name: "a", DataType: "int"},
			{Name: "b", GeneratedExpression: "a * 2", GeneratedStored: true},
		},
	}}}
	out, err = NewSQLite().Generate(untyped)
	require.NoError(t, err)
	assert.Contains(t, out, "b GENERATED ALWAYS AS (a * 2) STORED")
	assert.NotContains(t, out, "b  ")
}

// TestSQLiteUnsignedAndSequenceNotes covers the two things SQLite has no form
// for: an unsigned integer, whose range has to be kept by widening, and a
// sequence, which is stated rather than dropped without a word.
func TestSQLiteUnsignedAndSequenceNotes(t *testing.T) {
	schema := &sqlmapper.Schema{
		Sequences: []sqlmapper.Sequence{
			{Name: "ticket_seq", StartValue: 100, IncrementBy: 5},
			{Name: "users_id_seq", StartValue: 1, IncrementBy: 1},
		},
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "int", AutoIncrement: true},
				{Name: "small", DataType: "tinyint", IsUnsigned: true},
				{Name: "big", DataType: "bigint", IsUnsigned: true},
			},
		}},
	}

	out, err := NewSQLite().Generate(schema)
	require.NoError(t, err)

	// SQLite has one integer class, so widening a small unsigned changes
	// nothing; a bigint unsigned runs past a signed one and becomes a decimal.
	assert.Contains(t, out, "small INTEGER")
	assert.Contains(t, out, "big NUMERIC(20)")

	assert.Contains(t, out, "-- not carried, SQLite has no sequence: ticket_seq (start 100, increment 5)")
	// The one the auto-increment column carries is already accounted for.
	assert.NotContains(t, out, "users_id_seq")
}
