package sqlite

import (
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realSchemaDump is what sqlite3 .schema writes, taken from SQLite 3.51. Note
// sqlite_sequence, which SQLite creates for itself when a table uses
// AUTOINCREMENT, and the trailing column-list comment it puts on a view.
const realSchemaDump = `CREATE TABLE customers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    full_name TEXT,
    is_active INTEGER NOT NULL DEFAULT 1,
    score NUMERIC(10,2) DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT customers_email_key UNIQUE (email)
);
CREATE TABLE sqlite_sequence(name,seq);
CREATE TABLE invoices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    customer_id INTEGER NOT NULL,
    amount NUMERIC(12,2) NOT NULL,
    issued_on TEXT NOT NULL,
    CONSTRAINT invoices_amount_check CHECK (amount >= 0),
    CONSTRAINT invoices_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE CASCADE
);
CREATE INDEX idx_invoices_customer ON invoices(customer_id);`

// Table constraints used to be skipped and forgotten, so every key a real
// schema declared was lost on the way out.
func TestSQLite_RealSchemaConstraints(t *testing.T) {
	schema, err := NewSQLite().Parse(realSchemaDump)
	require.NoError(t, err)

	require.Len(t, schema.Tables, 2, "sqlite_sequence belongs to SQLite, not to the schema")
	assert.Equal(t, "customers", schema.Tables[0].Name)
	assert.Equal(t, "invoices", schema.Tables[1].Name)

	customers := schema.Tables[0]
	assert.True(t, customers.Columns[0].AutoIncrement)
	assert.True(t, customers.Columns[0].IsPrimaryKey)

	// Reading the value up to the first space returned nothing at all.
	byName := map[string]sqlmapper.Column{}
	for _, c := range customers.Columns {
		byName[c.Name] = c
	}
	assert.Equal(t, "1", byName["is_active"].DefaultValue)
	assert.Equal(t, "0", byName["score"].DefaultValue)
	assert.Equal(t, "CURRENT_TIMESTAMP", byName["created_at"].DefaultValue)
	assert.Equal(t, 10, byName["score"].Length)
	assert.Equal(t, 2, byName["score"].Scale)

	require.Len(t, customers.Constraints, 1)
	assert.Equal(t, "UNIQUE", customers.Constraints[0].Type)
	assert.Equal(t, "customers_email_key", customers.Constraints[0].Name)
	assert.Equal(t, []string{"email"}, customers.Constraints[0].Columns)

	invoices := schema.Tables[1]
	require.Len(t, invoices.Constraints, 2)

	assert.Equal(t, "CHECK", invoices.Constraints[0].Type)
	assert.Equal(t, "amount >= 0", invoices.Constraints[0].CheckExpression)

	fk := invoices.Constraints[1]
	assert.Equal(t, "FOREIGN KEY", fk.Type)
	assert.Equal(t, []string{"customer_id"}, fk.Columns)
	assert.Equal(t, "customers", fk.RefTable)
	assert.Equal(t, []string{"id"}, fk.RefColumns)
	assert.Equal(t, "CASCADE", fk.DeleteRule)

	require.Len(t, invoices.Indexes, 1)
	assert.Equal(t, "idx_invoices_customer", invoices.Indexes[0].Name)
}

// SQLite to SQLite has to come back out as it went in.
func TestSQLite_RealSchemaRoundTrip(t *testing.T) {
	schema, err := NewSQLite().Parse(realSchemaDump)
	require.NoError(t, err)

	out, err := NewSQLite().Generate(schema)
	require.NoError(t, err)

	// AUTOINCREMENT is only legal directly after INTEGER PRIMARY KEY.
	assert.Contains(t, out, "id INTEGER PRIMARY KEY AUTOINCREMENT")
	assert.NotContains(t, out, "PRIMARY KEY (id)", "the key is already on the column")

	assert.Contains(t, out, "CONSTRAINT customers_email_key UNIQUE (email)")
	assert.Contains(t, out, "CONSTRAINT invoices_amount_check CHECK (amount >= 0)")
	assert.Contains(t, out, "FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE CASCADE")
	assert.Contains(t, out, "DEFAULT CURRENT_TIMESTAMP")
	assert.NotContains(t, out, "sqlite_sequence")
}

// A source type that SQLite has no equivalent for becomes the storage class
// closest to it, rather than travelling across as a name SQLite does not know.
func TestSQLite_TypeMapping(t *testing.T) {
	schema := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
		Name: "t",
		Columns: []sqlmapper.Column{
			{Name: "a", DataType: "bigint"},
			{Name: "b", DataType: "boolean"},
			{Name: "c", DataType: "character varying", Length: 255},
			{Name: "d", DataType: "jsonb"},
			{Name: "e", DataType: "timestamp with time zone"},
			{Name: "f", DataType: "numeric", Length: 10, Scale: 2},
			{Name: "g", DataType: "bytea"},
			{Name: "h", DataType: "double precision"},
			{Name: "i", DataType: "text", IsArray: true},
		},
	}}}

	out, err := NewSQLite().Generate(schema)
	require.NoError(t, err)

	for _, want := range []string{
		"a INTEGER", "b INTEGER", "c TEXT", "d TEXT", "e TEXT",
		"f NUMERIC(10,2)", "g BLOB", "h REAL", "i TEXT",
	} {
		assert.Contains(t, out, want)
	}
}

// A composite key cannot be declared on a column, so it goes at the end.
func TestSQLite_CompositePrimaryKey(t *testing.T) {
	schema := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
		Name: "memberships",
		Columns: []sqlmapper.Column{
			{Name: "user_id", DataType: "int"},
			{Name: "group_id", DataType: "int"},
		},
		Constraints: []sqlmapper.Constraint{
			{Type: "PRIMARY KEY", Columns: []string{"user_id", "group_id"}},
		},
	}}}

	out, err := NewSQLite().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "PRIMARY KEY (user_id, group_id)")
}
