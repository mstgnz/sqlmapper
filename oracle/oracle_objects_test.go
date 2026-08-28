package oracle

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSequence(t *testing.T) {
	const dump = `CREATE SEQUENCE app.order_seq START WITH 100 INCREMENT BY 5 MINVALUE 1 MAXVALUE 999 CACHE 20
/`
	schema, err := NewOracle().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Sequences, 1)

	seq := schema.Sequences[0]
	assert.Equal(t, "order_seq", seq.Name)
	assert.Equal(t, "app", seq.Schema)
	assert.Equal(t, 100, seq.StartValue)
	assert.Equal(t, 5, seq.IncrementBy)
	assert.Equal(t, 1, seq.MinValue)
	assert.Equal(t, 999, seq.MaxValue)
	assert.Equal(t, 20, seq.Cache)
}

func TestParseTrigger(t *testing.T) {
	const dump = `CREATE OR REPLACE TRIGGER app.users_bi BEFORE INSERT ON app.users FOR EACH ROW BEGIN :NEW.created_at := SYSDATE
/`
	schema, err := NewOracle().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Triggers, 1)

	trg := schema.Triggers[0]
	assert.Equal(t, "users_bi", trg.Name)
	assert.Equal(t, "BEFORE", trg.Timing)
	assert.Equal(t, "INSERT", trg.Event)
	assert.Contains(t, trg.Table, "users")
}

func TestParseIndex(t *testing.T) {
	const dump = `CREATE TABLE app.users (
    id NUMBER PRIMARY KEY,
    email VARCHAR2(255)
)
/
CREATE UNIQUE INDEX idx_users_email ON app.users (email)
/`
	schema, err := NewOracle().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)
	require.Len(t, schema.Tables[0].Indexes, 1)

	idx := schema.Tables[0].Indexes[0]
	assert.Equal(t, "idx_users_email", idx.Name)
	assert.Equal(t, []string{"email"}, idx.Columns)
	assert.True(t, idx.IsUnique)
}

func TestGenerateSequenceSQL(t *testing.T) {
	o := &Oracle{}
	got := o.generateSequenceSQL(sqlmapper.Sequence{
		Name: "order_seq", StartValue: 100, IncrementBy: 5, MinValue: 1, MaxValue: 999, Cache: 20,
	})

	assert.Contains(t, got, "CREATE SEQUENCE order_seq")
	assert.Contains(t, got, "START WITH 100")
	assert.Contains(t, got, "INCREMENT BY 5")
}

func TestGenerateTypeSQL(t *testing.T) {
	o := &Oracle{}
	got := o.generateTypeSQL(sqlmapper.Type{Name: "addr_t", Definition: "OBJECT (street VARCHAR2(100))"})
	assert.Equal(t, "CREATE TYPE addr_t AS OBJECT (street VARCHAR2(100))", got)
}

func TestGenerateIndexSQL(t *testing.T) {
	o := &Oracle{}

	plain := o.generateIndexSQL("users", sqlmapper.Index{Name: "idx_email", Columns: []string{"email"}})
	assert.Contains(t, plain, "CREATE INDEX idx_email")
	assert.Contains(t, plain, "users")

	unique := o.generateIndexSQL("users", sqlmapper.Index{Name: "uq_email", Columns: []string{"email"}, IsUnique: true})
	assert.Contains(t, unique, "UNIQUE")
}

func TestOracleGenerateNilSchema(t *testing.T) {
	_, err := NewOracle().Generate(nil)
	assert.Error(t, err)
}

func TestOracleParseEmptyContent(t *testing.T) {
	_, err := NewOracle().Parse("")
	assert.Error(t, err)
}

func TestOracleStreamParser_GenerateStreamObjects(t *testing.T) {
	schema := &sqlmapper.Schema{
		Sequences: []sqlmapper.Sequence{{Name: "s", StartValue: 1, IncrementBy: 1}},
		Types:     []sqlmapper.Type{{Name: "addr_t", Definition: "OBJECT (street VARCHAR2(100))"}},
		Tables: []sqlmapper.Table{{
			Name:    "users",
			Columns: []sqlmapper.Column{{Name: "id", DataType: "NUMBER"}},
			Indexes: []sqlmapper.Index{{Name: "idx_id", Columns: []string{"id"}}},
		}},
	}

	var out strings.Builder
	require.NoError(t, NewOracleStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE SEQUENCE s")
	assert.Contains(t, got, "CREATE TYPE addr_t")
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "idx_id")
}

func TestOracleStreamParser_GenerateStreamFullSchema(t *testing.T) {
	schema := &sqlmapper.Schema{
		Sequences: []sqlmapper.Sequence{{Name: "s", StartValue: 10, IncrementBy: 2, MinValue: 1, MaxValue: 99, Cache: 5}},
		Types:     []sqlmapper.Type{{Name: "addr_t", Definition: "OBJECT (street VARCHAR2(100))"}},
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "NUMBER", IsPrimaryKey: true, IsNullable: false},
				{Name: "email", DataType: "VARCHAR2", Length: 255, IsNullable: false},
				{Name: "note", DataType: "VARCHAR2", Length: 50, DefaultValue: "none"},
			},
			Constraints: []sqlmapper.Constraint{
				{Name: "uq_email", Type: "UNIQUE", Columns: []string{"email"}},
			},
			// idx_email covers the same column as uq_email, which Oracle already
			// indexes, so it is left out: ORA-01408.
			Indexes: []sqlmapper.Index{
				{Name: "idx_email", Columns: []string{"email"}, IsUnique: true},
				{Name: "idx_note", Columns: []string{"note"}},
			},
		}},
		Views: []sqlmapper.View{{Name: "v", Definition: "SELECT id FROM users"}},
		Functions: []sqlmapper.Function{
			{Name: "f", Returns: "NUMBER", Body: "BEGIN RETURN 1; END;",
				Parameters: []sqlmapper.Parameter{{Name: "n", DataType: "NUMBER"}}},
			{Name: "p", IsProc: true, Body: "BEGIN NULL; END;",
				Parameters: []sqlmapper.Parameter{{Name: "n", DataType: "NUMBER"}}},
		},
		Triggers: []sqlmapper.Trigger{
			{Name: "trg", Timing: "BEFORE", Event: "INSERT", Table: "users", Body: "BEGIN NULL; END;"},
		},
	}

	var out strings.Builder
	require.NoError(t, NewOracleStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE SEQUENCE s")
	assert.Contains(t, got, "CREATE TYPE addr_t")
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "idx_note")
	assert.NotContains(t, got, "idx_email")
	assert.Contains(t, got, "CREATE OR REPLACE VIEW v")
}

func TestOracleParseTableWithNestedCommas(t *testing.T) {
	// The comma inside NUMBER(10,2) and inside a CHECK list must not split the
	// column definition in half.
	const dump = `CREATE TABLE app.orders (
    id NUMBER PRIMARY KEY,
    amount NUMBER(10,2) NOT NULL,
    status VARCHAR2(10) CHECK (status IN ('new','done')),
    CONSTRAINT fk_user FOREIGN KEY (user_id) REFERENCES app.users(id) ON DELETE CASCADE
)
/`
	schema, err := NewOracle().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)

	table := schema.Tables[0]
	assert.Equal(t, "orders", table.Name)
	assert.Equal(t, "app", table.Schema)

	names := make([]string, 0, len(table.Columns))
	for _, c := range table.Columns {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"id", "amount", "status"}, names)

	amount := table.Columns[1]
	assert.Equal(t, 10, amount.Length)
	assert.Equal(t, 2, amount.Scale)
}

func TestOracleGenerateWithConstraints(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "orders",
			Columns: []sqlmapper.Column{
				{Name: "id", DataType: "NUMBER", IsPrimaryKey: true, IsNullable: false},
				{Name: "user_id", DataType: "NUMBER", IsNullable: false},
			},
			Constraints: []sqlmapper.Constraint{
				{Name: "fk_user", Type: "FOREIGN KEY", Columns: []string{"user_id"},
					RefTable: "users", RefColumns: []string{"id"}, DeleteRule: "CASCADE"},
				{Name: "chk", Type: "CHECK", CheckExpression: "id > 0"},
			},
		}},
	}

	out, err := NewOracle().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "CREATE TABLE orders")
	assert.Contains(t, out, "user_id")
}

func TestOracleParseColumnAttributes(t *testing.T) {
	const dump = `CREATE TABLE app.accounts (
    id NUMBER PRIMARY KEY,
    code VARCHAR2(20) NOT NULL UNIQUE,
    balance NUMBER(12,2) DEFAULT 0 NOT NULL,
    label VARCHAR2(50) DEFAULT 'none',
    note CLOB
)
/`
	schema, err := NewOracle().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)

	table := schema.Tables[0]
	require.Len(t, table.Columns, 5)

	byName := map[string]sqlmapper.Column{}
	for _, c := range table.Columns {
		byName[c.Name] = c
	}

	assert.True(t, byName["id"].IsPrimaryKey)
	assert.True(t, byName["code"].IsUnique)
	assert.False(t, byName["code"].IsNullable)
	assert.Equal(t, 20, byName["code"].Length)
	assert.Equal(t, 12, byName["balance"].Length)
	assert.Equal(t, 2, byName["balance"].Scale)
	assert.Equal(t, "0", byName["balance"].DefaultValue)
	// The schema holds the value, not the literal: every generator quotes it
	// again for its own dialect, and keeping the quotes here corrupted the
	// default in every conversion.
	assert.Equal(t, "none", byName["label"].DefaultValue)
	// Oracle type names are folded onto the shared vocabulary so the other
	// dialects' type maps can pick them up; CLOB becomes text.
	assert.Equal(t, "text", byName["note"].DataType)
}

func TestOracleGenerateEmptySchema(t *testing.T) {
	out, err := NewOracle().Generate(&sqlmapper.Schema{})
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out))
}

func TestOracleParseMaterializedView(t *testing.T) {
	const dump = `CREATE MATERIALIZED VIEW app.user_counts AS SELECT COUNT(*) FROM app.users
/`
	schema, err := NewOracle().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Views, 1)
	assert.Contains(t, schema.Views[0].Name, "user_counts")
}
