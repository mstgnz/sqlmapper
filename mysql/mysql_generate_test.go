package mysql

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
		{"plain int", sqlmapper.Column{DataType: "int"}, "INT"},
		{"varchar carries its length", sqlmapper.Column{DataType: "varchar", Length: 100}, "VARCHAR(100)"},
		{"decimal carries precision and scale", sqlmapper.Column{DataType: "decimal", Length: 10, Scale: 2}, "DECIMAL(10,2)"},
		{"text never takes a length", sqlmapper.Column{DataType: "text", Length: 500}, "TEXT"},
		{"json never takes a length", sqlmapper.Column{DataType: "json", Length: 10}, "JSON"},

		// Types arriving from PostgreSQL.
		{"character varying", sqlmapper.Column{DataType: "character varying", Length: 255}, "VARCHAR(255)"},
		{"integer", sqlmapper.Column{DataType: "integer"}, "INT"},
		{"double precision", sqlmapper.Column{DataType: "double precision"}, "DOUBLE"},
		{"jsonb", sqlmapper.Column{DataType: "jsonb"}, "JSON"},
		{"boolean", sqlmapper.Column{DataType: "boolean"}, "TINYINT(1)"},
		{"uuid becomes a fixed-width string", sqlmapper.Column{DataType: "uuid"}, "VARCHAR(36)"},
		{"timestamptz", sqlmapper.Column{DataType: "timestamptz"}, "DATETIME"},
		{"bytea", sqlmapper.Column{DataType: "bytea"}, "BLOB"},

		// Serial variants keep the base integer type; AUTO_INCREMENT carries the rest.
		{"serial", sqlmapper.Column{DataType: "serial"}, "INT"},
		{"bigserial", sqlmapper.Column{DataType: "bigserial"}, "BIGINT"},
		{"smallserial", sqlmapper.Column{DataType: "smallserial"}, "SMALLINT"},

		// MySQL has no array type.
		{"array becomes json", sqlmapper.Column{DataType: "text", IsArray: true}, "JSON"},

		// ENUM and SET preserve their values.
		{"enum with values", sqlmapper.Column{DataType: "enum", EnumValues: []string{"a", "b"}}, "ENUM('a','b')"},
		{"enum without values", sqlmapper.Column{DataType: "enum"}, "VARCHAR(255)"},
		{"set without values", sqlmapper.Column{DataType: "set"}, "TEXT"},

		{"unknown type passes through", sqlmapper.Column{DataType: "geography"}, "GEOGRAPHY"},
		{"unknown type keeps its length", sqlmapper.Column{DataType: "geography", Length: 4}, "GEOGRAPHY(4)"},
	}

	m := &MySQL{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, m.resolveType(tt.col))
		})
	}
}

func TestDefaultLiteral(t *testing.T) {
	m := &MySQL{}

	tests := []struct {
		name      string
		col       sqlmapper.Column
		mysqlType string
		want      string
	}{
		{"no default", sqlmapper.Column{}, "INT", ""},
		{"literal NULL is dropped", sqlmapper.Column{DefaultValue: "NULL"}, "JSON", ""},
		{"numbers are unquoted", sqlmapper.Column{DefaultValue: "42"}, "INT", "42"},
		{"negative numbers are unquoted", sqlmapper.Column{DefaultValue: "-1.5"}, "DECIMAL(10,2)", "-1.5"},
		{"strings are quoted", sqlmapper.Column{DefaultValue: "active"}, "VARCHAR(50)", "'active'"},
		{"quotes are escaped", sqlmapper.Column{DefaultValue: "it's"}, "VARCHAR(50)", "'it''s'"},
		{"current timestamp is a keyword", sqlmapper.Column{DefaultValue: "CURRENT_TIMESTAMP"}, "DATETIME", "CURRENT_TIMESTAMP"},

		// A PostgreSQL boolean becomes TINYINT(1); DEFAULT 'true' is rejected by
		// MySQL in strict mode.
		{"boolean true", sqlmapper.Column{DefaultValue: "true"}, "TINYINT(1)", "1"},
		{"boolean false", sqlmapper.Column{DefaultValue: "false"}, "TINYINT(1)", "0"},

		// MySQL refuses a literal default on these types outright.
		{"text takes no default", sqlmapper.Column{DefaultValue: "hello"}, "TEXT", ""},
		{"json takes no default", sqlmapper.Column{DefaultValue: "{}"}, "JSON", ""},
		{"blob takes no default", sqlmapper.Column{DefaultValue: "x"}, "BLOB", ""},

		// Expressions are carried over with PostgreSQL syntax stripped.
		// The expression layer drops the cast and the parentheses the dump put
		// around the value, because the tree records neither.
		{"cast is stripped", sqlmapper.Column{DefaultValue: "(0)::numeric"}, "DECIMAL(10,2)", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, m.defaultLiteral(tt.col, tt.mysqlType))
		})
	}
}

func TestGenerateConstraintSQL(t *testing.T) {
	m := &MySQL{}

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
			"composite primary key",
			sqlmapper.Constraint{Type: "PRIMARY KEY", Columns: []string{"a", "b"}},
			"PRIMARY KEY (a, b)",
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
				DeleteRule: "CASCADE", UpdateRule: "RESTRICT",
			},
			"CONSTRAINT fk_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE ON UPDATE RESTRICT",
		},
		{
			// The expression layer drops the cast and, because the tree has no
			// node for a parenthesis, the redundant ones the dump wrote as well.
			"check drops PostgreSQL casts",
			sqlmapper.Constraint{Name: "chk", Type: "CHECK", CheckExpression: "(amount >= (0)::numeric)"},
			"CONSTRAINT chk CHECK (amount >= 0)",
		},
		{
			"unknown type renders nothing but its name",
			sqlmapper.Constraint{Name: "weird", Type: "EXCLUDE"},
			"CONSTRAINT weird ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, m.generateConstraintSQL(tt.c))
		})
	}
}

func TestGenerateViewSQL(t *testing.T) {
	m := &MySQL{}

	tests := []struct {
		name string
		view sqlmapper.View
		want string
	}{
		{
			"plain view",
			sqlmapper.View{Name: "v", Definition: "SELECT 1"},
			"CREATE VIEW v AS SELECT 1;",
		},
		{
			"schema prefix is dropped from the name and the body",
			sqlmapper.View{Name: "public.v", Definition: "SELECT id FROM public.users"},
			"CREATE VIEW v AS SELECT id FROM users;",
		},
		{
			"trailing delimiter is not doubled",
			sqlmapper.View{Name: "v", Definition: "SELECT 1;"},
			"CREATE VIEW v AS SELECT 1;",
		},
		{
			"materialized views degrade to plain views",
			sqlmapper.View{Name: "v", Definition: "SELECT 1", IsMaterialized: true},
			"CREATE VIEW v AS SELECT 1;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, m.generateViewSQL(tt.view))
		})
	}
}

// TestGeneratedExpressionsAreTranslated checks the behaviour through the
// generator rather than through a helper: the expression layer itself is tested
// in internal/expr, and what matters here is that the generator routes through
// it.
func TestGeneratedExpressionsAreTranslated(t *testing.T) {
	tests := []struct {
		name string
		c    sqlmapper.Constraint
		want string
	}{
		{
			"a cast is dropped",
			sqlmapper.Constraint{Type: "CHECK", CheckExpression: "(amount >= (0)::numeric)"},
			"CHECK (amount >= 0)",
		},
		{
			"redundant parentheses are dropped",
			sqlmapper.Constraint{Type: "CHECK", CheckExpression: "((status IN ('a','b')))"},
			"CHECK (status IN ('a', 'b'))",
		},
		{
			"an unreadable expression survives unchanged",
			sqlmapper.Constraint{Type: "CHECK", CheckExpression: "a @> b[1:2]"},
			"CHECK (a @> b[1:2])",
		},
	}

	m := &MySQL{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, m.generateConstraintSQL(tt.c))
		})
	}
}

func TestGeneratedViewDropsForeignSchema(t *testing.T) {
	m := &MySQL{}
	got := m.generateViewSQL(sqlmapper.View{
		Name:       "v",
		Definition: "SELECT id FROM public.users WHERE is_active",
	})
	assert.Equal(t, "CREATE VIEW v AS SELECT id FROM users WHERE is_active;", got)
}

func TestColumnIsAutoIncrement(t *testing.T) {
	table := sqlmapper.Table{
		Name: "t",
		Columns: []sqlmapper.Column{
			{Name: "id", AutoIncrement: true},
			{Name: "name"},
		},
	}

	assert.True(t, columnIsAutoIncrement(table, "id"))
	assert.False(t, columnIsAutoIncrement(table, "name"))
	assert.False(t, columnIsAutoIncrement(table, "missing"))
}

func TestIsNumeric(t *testing.T) {
	assert.True(t, isNumeric("0"))
	assert.True(t, isNumeric("42"))
	assert.True(t, isNumeric("-1.5"))
	assert.False(t, isNumeric(""))
	assert.False(t, isNumeric("abc"))
	assert.False(t, isNumeric("1a"))
	assert.False(t, isNumeric("CURRENT_TIMESTAMP"))
}

func TestStripSchemaPrefix(t *testing.T) {
	assert.Equal(t, "users", stripSchemaPrefix("public.users"))
	assert.Equal(t, "users", stripSchemaPrefix("users"))
	assert.Equal(t, "users", stripSchemaPrefix(`"public"."users"`))
	assert.Equal(t, "", stripSchemaPrefix(""))
}

func TestGenerateEmptySchema(t *testing.T) {
	out, err := NewMySQL().Generate(&sqlmapper.Schema{})
	assert.NoError(t, err)
	assert.Empty(t, out)
}

func TestGenerateNilSchema(t *testing.T) {
	_, err := NewMySQL().Generate(nil)
	assert.Error(t, err)
}

func TestParseEmptyContent(t *testing.T) {
	_, err := NewMySQL().Parse("")
	assert.Error(t, err)
}

// TestParseFullDump walks the branches of Parse that a table-only fixture never
// reaches: comments, grants, triggers and routines.
func TestParseFullDump(t *testing.T) {
	const dump = "CREATE DATABASE IF NOT EXISTS shop;\n" +
		"CREATE TABLE `users` (\n" +
		"  `id` int NOT NULL AUTO_INCREMENT,\n" +
		"  `email` varchar(255) NOT NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  KEY `idx_email` (`email`)\n" +
		") ENGINE=InnoDB;\n" +
		"CREATE UNIQUE INDEX idx_users_email ON users (email);\n" +
		"CREATE VIEW active_users AS SELECT id FROM users;\n" +
		"CREATE FUNCTION add_one(n INT) RETURNS INT BEGIN RETURN n + 1 END;\n" +
		"CREATE PROCEDURE touch_user(IN uid INT) BEGIN SELECT uid END;\n" +
		"CREATE TRIGGER trg_users BEFORE INSERT ON users FOR EACH ROW BEGIN SET NEW.email = 'x' END;\n" +
		"ALTER TABLE users COMMENT = 'application users';\n" +
		"GRANT SELECT, INSERT ON shop.users TO 'app'@'%';\n" +
		"GRANT EXECUTE ON PROCEDURE touch_user TO 'app'@'%';\n" +
		"REVOKE INSERT ON shop.users FROM 'app'@'%';\n"

	schema, err := NewMySQL().Parse(dump)
	require.NoError(t, err)

	assert.Equal(t, "shop", schema.Name)
	require.Len(t, schema.Tables, 1)
	assert.Equal(t, "application users", schema.Tables[0].Comment)
	assert.NotEmpty(t, schema.Tables[0].Indexes)
	assert.Len(t, schema.Views, 1)
	assert.NotEmpty(t, schema.Functions)
	assert.Len(t, schema.Triggers, 1)
	assert.NotEmpty(t, schema.Permissions)
}

func TestParseConstraintForms(t *testing.T) {
	m := &MySQL{}

	pk, err := m.parseConstraint("PRIMARY KEY (id, tenant_id)")
	require.NoError(t, err)
	assert.Equal(t, "PRIMARY KEY", pk.Type)
	assert.Equal(t, []string{"id", "tenant_id"}, pk.Columns)

	uq, err := m.parseConstraint("CONSTRAINT uq_email UNIQUE KEY idx (email)")
	require.NoError(t, err)
	assert.Equal(t, "UNIQUE", uq.Type)
	assert.Equal(t, "uq_email", uq.Name)

	fk, err := m.parseConstraint("CONSTRAINT fk_u FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE SET NULL ON UPDATE NO ACTION")
	require.NoError(t, err)
	assert.Equal(t, "FOREIGN KEY", fk.Type)
	assert.Equal(t, "users", fk.RefTable)
	assert.Equal(t, "SET NULL", fk.DeleteRule)
	assert.Equal(t, "NO ACTION", fk.UpdateRule)

	ck, err := m.parseConstraint("CONSTRAINT chk CHECK (total >= 0)")
	require.NoError(t, err)
	assert.Equal(t, "CHECK", ck.Type)
	assert.Equal(t, "total >= 0", ck.CheckExpression)
}

func TestTakeUntilStopWord(t *testing.T) {
	assert.Equal(t, "CURRENT_TIMESTAMP",
		takeUntilStopWord("CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP", mysqlDefaultStopWords))
	assert.Equal(t, "'a b'",
		takeUntilStopWord("'a b' COMMENT 'x'", mysqlDefaultStopWords))
	assert.Equal(t, "", takeUntilStopWord("ON UPDATE NOW()", mysqlDefaultStopWords))
}

func TestMySQLStreamParser_ParseStatementAllTypes(t *testing.T) {
	p := NewMySQLStreamParser()

	// parseStatement is the parallel path's dispatcher; drive every branch.
	for _, stmt := range []string{
		"CREATE TABLE t (id INT)",
		"CREATE VIEW v AS SELECT 1",
		"CREATE FUNCTION f(n INT) RETURNS INT BEGIN RETURN n END",
		"CREATE PROCEDURE p(IN n INT) BEGIN SELECT n END",
		"CREATE TRIGGER g BEFORE INSERT ON t FOR EACH ROW BEGIN SET NEW.id = 1 END",
	} {
		obj, err := p.parseStatement(stmt)
		require.NoError(t, err, stmt)
		require.NotNil(t, obj, stmt)
	}

	// Anything else is skipped rather than treated as an error.
	obj, err := p.parseStatement("SET foreign_key_checks = 0")
	require.NoError(t, err)
	assert.Nil(t, obj)
}

func TestGenerateOrdersTablesByDependency(t *testing.T) {
	// mysqldump emits tables alphabetically, so the child comes first.
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

	out, err := NewMySQL().Generate(schema)
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

	out, err := NewMySQL().Generate(&sqlmapper.Schema{
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

func TestMySQLStreamParser_GenerateStreamOrdersTables(t *testing.T) {
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
	require.NoError(t, NewMySQLStreamParser().GenerateStream(schema, &out))

	got := out.String()
	assert.Less(t, strings.Index(got, "CREATE TABLE users"), strings.Index(got, "CREATE TABLE orders"))
}
