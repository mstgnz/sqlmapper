package postgres

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveType(t *testing.T) {
	tests := []struct {
		name string
		col  sqlmapper.Column
		want string
	}{
		{"varchar carries its length", sqlmapper.Column{DataType: "varchar", Length: 100}, "VARCHAR(100)"},
		{"numeric carries precision and scale", sqlmapper.Column{DataType: "numeric", Length: 10, Scale: 2}, "NUMERIC(10,2)"},
		{"text never takes a length", sqlmapper.Column{DataType: "text", Length: 500}, "TEXT"},
		{"jsonb never takes a length", sqlmapper.Column{DataType: "jsonb", Length: 10}, "JSONB"},

		// Types arriving from MySQL.
		{"int", sqlmapper.Column{DataType: "int"}, "INTEGER"},
		{"tinyint", sqlmapper.Column{DataType: "tinyint"}, "SMALLINT"},
		{"datetime", sqlmapper.Column{DataType: "datetime"}, "TIMESTAMP"},
		{"json", sqlmapper.Column{DataType: "json"}, "JSONB"},
		{"longtext", sqlmapper.Column{DataType: "longtext"}, "TEXT"},
		{"blob", sqlmapper.Column{DataType: "blob"}, "BYTEA"},
		{"bool", sqlmapper.Column{DataType: "bool"}, "BOOLEAN"},

		// Auto-increment picks the matching serial width.
		{"auto increment int", sqlmapper.Column{DataType: "int", AutoIncrement: true}, "SERIAL"},
		{"auto increment bigint", sqlmapper.Column{DataType: "bigint", AutoIncrement: true}, "BIGSERIAL"},
		{"auto increment smallint", sqlmapper.Column{DataType: "smallint", AutoIncrement: true}, "SMALLSERIAL"},
		{"serial without the flag", sqlmapper.Column{DataType: "serial"}, "SERIAL"},

		{"unknown type passes through", sqlmapper.Column{DataType: "geography"}, "geography"},
	}

	p := &PostgreSQL{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, p.resolveType(tt.col, "t"))
		})
	}
}

func TestResolveTypeEnum(t *testing.T) {
	p := &PostgreSQL{}
	col := sqlmapper.Column{Name: "status", DataType: "enum", EnumValues: []string{"a", "b"}}
	// A MySQL ENUM becomes a named PostgreSQL type, declared alongside the table.
	assert.Equal(t, "users_status_enum", p.resolveType(col, "users"))
}

func TestNormalizePGTypeName(t *testing.T) {
	tests := map[string]string{
		"character varying":           "varchar",
		"CHARACTER VARYING":           "varchar",
		"character":                   "char",
		"timestamp without time zone": "timestamp",
		"time without time zone":      "time",
		"time with time zone":         "timetz",
		"bit varying":                 "varbit",
		"timestamp with time zone":    "timestamp with time zone",
		"integer":                     "integer",
		"  text  ":                    "text",
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, normalizePGTypeName(in))
		})
	}
}

func TestApplyPGType(t *testing.T) {
	tests := []struct {
		name      string
		expr      string
		wantType  string
		wantLen   int
		wantScale int
		wantArray bool
	}{
		{"plain type", "text", "text", 0, 0, false},
		{"type with length", "varchar(255)", "varchar", 255, 0, false},
		{"multi word type with length", "character varying(255)", "varchar", 255, 0, false},
		{"length and scale", "numeric(10,2)", "numeric", 10, 2, false},
		{"length and scale with spaces", "numeric(10, 2)", "numeric", 10, 2, false},
		{"multi word type", "double precision", "double precision", 0, 0, false},
		{"timestamp with time zone", "timestamp with time zone", "timestamp with time zone", 0, 0, false},
		{"array", "text[]", "text", 0, 0, true},
		{"array with a space", "integer []", "integer", 0, 0, true},
		{"nested array", "text[][]", "text", 0, 0, true},
		{"array of a sized type", "character varying(50)[]", "varchar", 50, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var col sqlmapper.Column
			applyPGType(&col, tt.expr)
			assert.Equal(t, tt.wantType, col.DataType)
			assert.Equal(t, tt.wantLen, col.Length)
			assert.Equal(t, tt.wantScale, col.Scale)
			assert.Equal(t, tt.wantArray, col.IsArray)
		})
	}
}

func TestTakeUntilStopWord(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"stops at NOT", "character varying(255) NOT NULL", "character varying(255)"},
		{"stops at DEFAULT", "boolean DEFAULT true", "boolean"},
		{"stops at PRIMARY", "bigint PRIMARY KEY", "bigint"},
		{"keeps a multi word type", "timestamp with time zone", "timestamp with time zone"},
		{"a stop word inside parens is not a stop", "numeric(10, 2) NOT NULL", "numeric(10, 2)"},
		{"a stop word inside a string is not a stop", "text DEFAULT 'not null'", "text"},
		{"nothing to take", "NOT NULL", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, takeUntilStopWord(tt.in, pgTypeStopWords))
		})
	}
}

func TestGenerateConstraintSQL(t *testing.T) {
	p := &PostgreSQL{}

	tests := []struct {
		name string
		c    sqlmapper.Constraint
		want string
	}{
		{
			"primary key",
			sqlmapper.Constraint{Type: "PRIMARY KEY", Columns: []string{"id"}},
			"PRIMARY KEY (id)",
		},
		{
			"named unique",
			sqlmapper.Constraint{Name: "uq_email", Type: "UNIQUE", Columns: []string{"email"}},
			"CONSTRAINT uq_email UNIQUE (email)",
		},
		{
			"foreign key with rules",
			sqlmapper.Constraint{
				Name: "fk_user", Type: "FOREIGN KEY",
				Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"},
				DeleteRule: "CASCADE", UpdateRule: "SET NULL",
			},
			"CONSTRAINT fk_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE ON UPDATE SET NULL",
		},
		{
			"check",
			sqlmapper.Constraint{Type: "CHECK", CheckExpression: "amount >= 0"},
			"CHECK (amount >= 0)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, p.generateConstraintSQL(tt.c))
		})
	}
}

func TestGenerateViewSQL(t *testing.T) {
	p := &PostgreSQL{}

	assert.Equal(t, "CREATE VIEW v AS SELECT 1;",
		p.generateViewSQL(sqlmapper.View{Name: "v", Definition: "SELECT 1"}, nil))
	assert.Equal(t, "CREATE VIEW v AS SELECT 1;",
		p.generateViewSQL(sqlmapper.View{Name: "v", Definition: "SELECT 1;"}, nil))
	assert.Equal(t, "CREATE MATERIALIZED VIEW mv AS SELECT 1;",
		p.generateViewSQL(sqlmapper.View{Name: "mv", Definition: "SELECT 1", IsMaterialized: true}, nil))
}

func TestGenerateTypeSQL(t *testing.T) {
	p := &PostgreSQL{}

	assert.Equal(t, "CREATE TYPE mood AS ENUM ('happy', 'sad');",
		p.generateTypeSQL(sqlmapper.Type{Name: "mood", Kind: "ENUM", Definition: "'happy', 'sad'"}))
	assert.Equal(t, "CREATE TYPE addr AS (street text, city text);",
		p.generateTypeSQL(sqlmapper.Type{Name: "addr", Kind: "COMPOSITE", Definition: "street text, city text"}))
}

func TestGenerateArrayColumnKeepsBrackets(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "t",
			Columns: []sqlmapper.Column{
				{Name: "tags", DataType: "text", IsArray: true, IsNullable: true},
			},
		}},
	}

	out, err := NewPostgreSQL().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "tags TEXT[]")
}

func TestParseAlterColumnDefaults(t *testing.T) {
	const dump = `
CREATE TABLE t (
    id bigint NOT NULL,
    label text,
    note text,
    created_at timestamp
);
ALTER TABLE ONLY t ALTER COLUMN id SET DEFAULT nextval('t_id_seq'::regclass);
ALTER TABLE ONLY t ALTER COLUMN label SET DEFAULT 'draft'::character varying;
ALTER TABLE ONLY t ALTER COLUMN note SET DEFAULT NULL;
ALTER TABLE ONLY t ALTER COLUMN created_at SET DEFAULT now();
`
	schema, err := NewPostgreSQL().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)

	cols := map[string]sqlmapper.Column{}
	for _, c := range schema.Tables[0].Columns {
		cols[c.Name] = c
	}

	assert.True(t, cols["id"].AutoIncrement)
	assert.Empty(t, cols["id"].DefaultValue)
	assert.Equal(t, "draft", cols["label"].DefaultValue)
	assert.Empty(t, cols["note"].DefaultValue)
	assert.Equal(t, "CURRENT_TIMESTAMP", cols["created_at"].DefaultValue)
}

func TestParseIdentityColumn(t *testing.T) {
	const dump = `
CREATE TABLE t (id bigint GENERATED ALWAYS AS IDENTITY NOT NULL, name text);
`
	schema, err := NewPostgreSQL().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)
	assert.True(t, schema.Tables[0].Columns[0].AutoIncrement,
		"GENERATED AS IDENTITY behaves like AUTO_INCREMENT elsewhere")
}

func TestParseSequenceOptionOrder(t *testing.T) {
	// pg_dump writes the options in this order and uses NO MINVALUE / NO MAXVALUE.
	const dump = `
CREATE SEQUENCE public.s
    START WITH 5
    INCREMENT BY 2
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;
`
	schema, err := NewPostgreSQL().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Sequences, 1)

	seq := schema.Sequences[0]
	assert.Equal(t, "s", seq.Name)
	assert.Equal(t, "public", seq.Schema)
	assert.Equal(t, 5, seq.StartValue)
	assert.Equal(t, 2, seq.IncrementBy)
	assert.Equal(t, 1, seq.Cache)
	assert.False(t, seq.Cycle)
}

func TestParseSequenceCycle(t *testing.T) {
	schema, err := NewPostgreSQL().Parse("CREATE SEQUENCE s INCREMENT BY 1 CYCLE;")
	require.NoError(t, err)
	require.Len(t, schema.Sequences, 1)
	assert.True(t, schema.Sequences[0].Cycle)

	schema, err = NewPostgreSQL().Parse("CREATE SEQUENCE s INCREMENT BY 1 NO CYCLE;")
	require.NoError(t, err)
	require.Len(t, schema.Sequences, 1)
	assert.False(t, schema.Sequences[0].Cycle, "NO CYCLE must not read as CYCLE")
}

func TestColumnIsAutoIncrement(t *testing.T) {
	table := sqlmapper.Table{
		Columns: []sqlmapper.Column{
			{Name: "id", AutoIncrement: true},
			{Name: "name"},
		},
	}

	assert.True(t, columnIsAutoIncrement(table, "id"))
	assert.False(t, columnIsAutoIncrement(table, "name"))
	assert.False(t, columnIsAutoIncrement(table, "missing"))
}

func TestStripSchemaPrefix(t *testing.T) {
	assert.Equal(t, "users", stripSchemaPrefix("public.users"))
	assert.Equal(t, "users", stripSchemaPrefix("users"))
	assert.Equal(t, "users", stripSchemaPrefix(`"public"."users"`))
}

func TestGenerateNilSchema(t *testing.T) {
	_, err := NewPostgreSQL().Generate(nil)
	assert.Error(t, err)
}

func TestParseEmptyContent(t *testing.T) {
	_, err := NewPostgreSQL().Parse("")
	assert.Error(t, err)
}

// TestParseFullDump walks the branches of Parse that a table-only fixture never
// reaches: extensions, composite types, materialized views, triggers, comments
// and grants.
func TestParseFullDump(t *testing.T) {
	const dump = `CREATE SCHEMA app;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA public;
CREATE TYPE mood AS ENUM ('happy', 'sad');
CREATE TYPE addr AS (street text, city text);

CREATE TABLE app.users (
    id bigserial PRIMARY KEY,
    email character varying(255) NOT NULL UNIQUE,
    feeling mood,
    CONSTRAINT users_email_check CHECK (email <> '')
);

CREATE TABLE app.orders (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL
);

ALTER TABLE ONLY app.orders
    ADD CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES app.users(id) ON DELETE CASCADE ON UPDATE RESTRICT;

CREATE INDEX idx_orders_user ON app.orders USING btree (user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX idx_users_email ON app.users (email);

CREATE VIEW app.active_users AS SELECT id FROM app.users;
CREATE MATERIALIZED VIEW app.user_counts AS SELECT count(*) FROM app.users WITH NO DATA;

CREATE FUNCTION app.add_one(n integer) RETURNS integer AS $$ SELECT n + 1 $$ LANGUAGE sql;
CREATE PROCEDURE app.touch(uid integer) LANGUAGE plpgsql AS $$ BEGIN END $$;
CREATE TRIGGER users_bi BEFORE INSERT ON app.users FOR EACH ROW EXECUTE FUNCTION app.stamp();

COMMENT ON TABLE app.users IS 'application users';
COMMENT ON COLUMN app.users.email IS 'login address';

GRANT SELECT, INSERT ON TABLE app.users TO reporting;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA app TO admin;
GRANT EXECUTE ON FUNCTION app.add_one(integer) TO reporting;
REVOKE INSERT ON TABLE app.users FROM reporting;
`
	schema, err := NewPostgreSQL().Parse(dump)
	require.NoError(t, err)

	require.Len(t, schema.Tables, 2)
	assert.NotEmpty(t, schema.Extensions)
	assert.Len(t, schema.Types, 2)
	assert.Len(t, schema.Views, 2)
	assert.NotEmpty(t, schema.Functions)
	assert.NotEmpty(t, schema.Triggers)
	assert.NotEmpty(t, schema.Permissions)

	users := schema.Tables[0]
	assert.Equal(t, "application users", users.Comment)
	require.NotEmpty(t, users.Indexes)

	orders := schema.Tables[1]
	fk := constraintOfType(t, orders, "FOREIGN KEY")
	assert.Equal(t, "users", fk.RefTable)
	assert.Equal(t, "CASCADE", fk.DeleteRule)
	assert.Equal(t, "RESTRICT", fk.UpdateRule)
}

func constraintOfType(t *testing.T, table sqlmapper.Table, kind string) sqlmapper.Constraint {
	t.Helper()
	for _, c := range table.Constraints {
		if c.Type == kind {
			return c
		}
	}
	t.Fatalf("table %q has no %s constraint", table.Name, kind)
	return sqlmapper.Constraint{}
}

func TestParseConstraintForms(t *testing.T) {
	p := &PostgreSQL{}

	pk, err := p.parseConstraint("PRIMARY KEY (id, tenant_id)")
	require.NoError(t, err)
	assert.Equal(t, []string{"id", "tenant_id"}, pk.Columns)

	fk, err := p.parseConstraint("CONSTRAINT fk FOREIGN KEY (user_id) REFERENCES public.users (id) ON DELETE SET NULL ON UPDATE NO ACTION")
	require.NoError(t, err)
	assert.Equal(t, "users", fk.RefTable)
	assert.Equal(t, "SET NULL", fk.DeleteRule)
	assert.Equal(t, "NO ACTION", fk.UpdateRule)

	fkDefault, err := p.parseConstraint("FOREIGN KEY (a) REFERENCES b (c) ON DELETE SET DEFAULT")
	require.NoError(t, err)
	assert.Equal(t, "SET DEFAULT", fkDefault.DeleteRule)

	ck, err := p.parseConstraint("CONSTRAINT chk CHECK (total >= 0)")
	require.NoError(t, err)
	assert.Equal(t, "total >= 0", ck.CheckExpression)
}

func TestGenerateTableSQLCompositePrimaryKey(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "memberships",
			Columns: []sqlmapper.Column{
				{Name: "user_id", DataType: "bigint", IsNullable: false},
				{Name: "org_id", DataType: "bigint", IsNullable: false},
			},
			Constraints: []sqlmapper.Constraint{
				{Type: "PRIMARY KEY", Columns: []string{"user_id", "org_id"}},
			},
			TableSpace: "fast",
		}},
	}

	out, err := NewPostgreSQL().Generate(schema)
	require.NoError(t, err)

	// A composite key can never be inline, so it must appear exactly once as a
	// table-level constraint.
	assert.Equal(t, 1, strings.Count(out, "PRIMARY KEY"))
	assert.Contains(t, out, "PRIMARY KEY (user_id, org_id)")
	assert.Contains(t, out, "TABLESPACE fast")
}

func TestPostgreSQLStreamParser_GenerateStreamRoutines(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name:    "users",
			Columns: []sqlmapper.Column{{Name: "id", DataType: "integer"}},
			Indexes: []sqlmapper.Index{{Name: "idx_id", Columns: []string{"id"}, IsUnique: true}},
		}},
		Views: []sqlmapper.View{{Name: "v", Definition: "SELECT id FROM users"}},
		Functions: []sqlmapper.Function{
			{Name: "f", Returns: "integer", Body: "SELECT 1", Language: "sql",
				Parameters: []sqlmapper.Parameter{{Name: "n", DataType: "integer"}}},
		},
		Triggers: []sqlmapper.Trigger{
			{Name: "trg", Timing: "BEFORE", Event: "INSERT", Table: "users", Body: "stamp()"},
		},
	}

	var out strings.Builder
	require.NoError(t, NewPostgreSQLStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Contains(t, got, "CREATE TABLE users")
	assert.Contains(t, got, "idx_id")
}

func TestGenerateIndexOptions(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name:    "events",
			Columns: []sqlmapper.Column{{Name: "id", DataType: "bigint"}, {Name: "kind", DataType: "text"}},
			Indexes: []sqlmapper.Index{{
				Name:       "idx_events_kind",
				Columns:    []string{"kind"},
				IsUnique:   true,
				Type:       "btree",
				Condition:  "kind IS NOT NULL",
				TableSpace: "fast",
			}},
		}},
	}

	out, err := NewPostgreSQL().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "CREATE UNIQUE INDEX idx_events_kind ON events USING btree(kind)")
	assert.Contains(t, out, "WHERE kind IS NOT NULL")
	assert.Contains(t, out, "TABLESPACE fast")
}

func TestGenerateEnumTypesForMySQLSchema(t *testing.T) {
	// A MySQL ENUM has no PostgreSQL equivalent as a column type, so Generate
	// declares a named type ahead of the table that uses it.
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name: "users",
			Columns: []sqlmapper.Column{
				{Name: "status", DataType: "enum", EnumValues: []string{"active", "banned"}, IsNullable: false},
			},
		}},
	}

	out, err := NewPostgreSQL().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "CREATE TYPE users_status_enum AS ENUM ('active', 'banned');")
	assert.Contains(t, out, "status users_status_enum NOT NULL")
}

func TestParseTypesWithSchemaQualifiedNames(t *testing.T) {
	const dump = `CREATE TYPE app.mood AS ENUM ('happy', 'sad');
CREATE TYPE app.addr AS (street text, city text);
`
	schema, err := NewPostgreSQL().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Types, 2)

	assert.Equal(t, "mood", schema.Types[0].Name)
	assert.Equal(t, "app", schema.Types[0].Schema)
	assert.Equal(t, "ENUM", schema.Types[0].Kind)

	assert.Equal(t, "addr", schema.Types[1].Name)
	assert.Equal(t, "COMPOSITE", schema.Types[1].Kind)
}

func TestParsePermissionForms(t *testing.T) {
	const dump = `GRANT SELECT, UPDATE ON TABLE users TO reporting WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA app TO admin;
GRANT EXECUTE ON FUNCTION add_one(integer) TO reporting;
REVOKE UPDATE ON TABLE users FROM reporting;
`
	schema, err := NewPostgreSQL().Parse(dump)
	require.NoError(t, err)
	require.NotEmpty(t, schema.Permissions)

	var grants, revokes int
	for _, p := range schema.Permissions {
		switch p.Type {
		case "GRANT":
			grants++
		case "REVOKE":
			revokes++
		}
	}
	assert.GreaterOrEqual(t, grants, 3)
	assert.Equal(t, 1, revokes)
}

func TestParseAlterTableConstraintsUnknownTableIsSkipped(t *testing.T) {
	// A partial dump can carry an ALTER TABLE for a table it does not define.
	// That must be skipped rather than aborting the parse.
	const dump = `CREATE TABLE known (id bigint);
ALTER TABLE ONLY missing ADD CONSTRAINT missing_pkey PRIMARY KEY (id);
`
	schema, err := NewPostgreSQL().Parse(dump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)
	assert.Empty(t, schema.Tables[0].Constraints)
}

func TestGenerateOrdersTablesByDependency(t *testing.T) {
	// mysqldump emits tables alphabetically, so the child comes first and the
	// generated SQL used to fail with "relation \"users\" does not exist".
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name:    "orders",
				Columns: []sqlmapper.Column{{Name: "user_id", DataType: "bigint"}},
				Constraints: []sqlmapper.Constraint{
					{Name: "fk_ou", Type: "FOREIGN KEY", Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}},
				},
			},
			{Name: "users", Columns: []sqlmapper.Column{{Name: "id", DataType: "bigint"}}},
		},
	}

	out, err := NewPostgreSQL().Generate(schema)
	require.NoError(t, err)
	assert.Less(t, strings.Index(out, "CREATE TABLE users"), strings.Index(out, "CREATE TABLE orders"))
	assert.NotContains(t, out, "ALTER TABLE", "ordering alone resolves this, no deferral needed")
}

func TestGenerateDefersCircularForeignKeys(t *testing.T) {
	cyclic := func(name, ref string) sqlmapper.Table {
		return sqlmapper.Table{
			Name:    name,
			Columns: []sqlmapper.Column{{Name: "id", DataType: "bigint"}, {Name: ref + "_id", DataType: "bigint"}},
			Constraints: []sqlmapper.Constraint{
				{Name: "fk_" + name, Type: "FOREIGN KEY", Columns: []string{ref + "_id"}, RefTable: ref, RefColumns: []string{"id"}},
			},
		}
	}

	out, err := NewPostgreSQL().Generate(&sqlmapper.Schema{
		Tables: []sqlmapper.Table{cyclic("a", "b"), cyclic("b", "a")},
	})
	require.NoError(t, err)

	assert.Equal(t, 1, strings.Count(out, "ALTER TABLE"))
	assert.Contains(t, out, "ADD CONSTRAINT")
	assert.Greater(t, strings.Index(out, "ALTER TABLE"), strings.Index(out, "CREATE TABLE b"))
}

func TestIsDeferred(t *testing.T) {
	named := sqlmapper.Constraint{Name: "fk_a", Type: "FOREIGN KEY", RefTable: "b", Columns: []string{"b_id"}}
	anon := sqlmapper.Constraint{Type: "FOREIGN KEY", RefTable: "b", Columns: []string{"b_id"}}
	other := sqlmapper.Constraint{Name: "fk_z", Type: "FOREIGN KEY", RefTable: "z", Columns: []string{"z_id"}}

	assert.True(t, isDeferred([]sqlmapper.Constraint{named}, named), "matched by name")
	assert.True(t, isDeferred([]sqlmapper.Constraint{anon}, anon), "matched by shape when unnamed")
	assert.False(t, isDeferred([]sqlmapper.Constraint{named}, other))
	assert.False(t, isDeferred(nil, named))
}

func TestGenerateDropsMariaDBJSONValidCheck(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{{
			Name:    "users",
			Columns: []sqlmapper.Column{{Name: "meta", DataType: "json"}},
			Constraints: []sqlmapper.Constraint{
				{Type: "CHECK", CheckExpression: "json_valid(`meta`)"},
				{Name: "chk_kept", Type: "CHECK", CheckExpression: "meta IS NOT NULL"},
			},
		}},
	}

	out, err := NewPostgreSQL().Generate(schema)
	require.NoError(t, err)

	assert.NotContains(t, out, "json_valid", "PostgreSQL has no json_valid function")
	assert.Contains(t, out, "chk_kept", "unrelated checks must survive")
}

func TestPostgreSQLStreamParser_GenerateStreamOrdersTables(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name:    "orders",
				Columns: []sqlmapper.Column{{Name: "user_id", DataType: "bigint"}},
				Constraints: []sqlmapper.Constraint{
					{Name: "fk_ou", Type: "FOREIGN KEY", Columns: []string{"user_id"}, RefTable: "users", RefColumns: []string{"id"}},
				},
			},
			{Name: "users", Columns: []sqlmapper.Column{{Name: "id", DataType: "bigint"}}},
		},
	}

	var out strings.Builder
	require.NoError(t, NewPostgreSQLStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Less(t, strings.Index(got, "CREATE TABLE users"), strings.Index(got, "CREATE TABLE orders"))
}
