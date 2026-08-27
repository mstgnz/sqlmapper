package integration

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/oracle"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/sqlite"
	"github.com/mstgnz/sqlmapper/sqlserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realPGDump mirrors what pg_dump -s actually emits: schema-qualified names, the
// spelled-out "character varying", constraints and sequence wiring in trailing
// ALTER TABLE statements, and no SERIAL keyword anywhere.
const realPGDump = `--
-- PostgreSQL database dump
--

SET statement_timeout = 0;
SET client_encoding = 'UTF8';
SELECT pg_catalog.set_config('search_path', '', false);

CREATE TABLE public.customers (
    id bigint NOT NULL,
    email character varying(255) NOT NULL,
    full_name text,
    is_active boolean DEFAULT true NOT NULL,
    score numeric(10,2) DEFAULT 0,
    tags text[],
    meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE public.customers OWNER TO postgres;

CREATE VIEW public.active_customers AS
 SELECT id,
    email
   FROM public.customers
  WHERE is_active;

CREATE SEQUENCE public.customers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

ALTER SEQUENCE public.customers_id_seq OWNED BY public.customers.id;

CREATE TABLE public.invoices (
    id bigint NOT NULL,
    customer_id bigint NOT NULL,
    amount numeric(12,2) NOT NULL,
    issued_on date NOT NULL,
    CONSTRAINT invoices_amount_check CHECK ((amount >= (0)::numeric))
);

ALTER TABLE ONLY public.customers ALTER COLUMN id SET DEFAULT nextval('public.customers_id_seq'::regclass);

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_email_key UNIQUE (email);

ALTER TABLE ONLY public.invoices
    ADD CONSTRAINT invoices_pkey PRIMARY KEY (id);

ALTER TABLE ONLY public.invoices
    ADD CONSTRAINT invoices_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;

CREATE INDEX idx_invoices_customer ON public.invoices USING btree (customer_id);
`

// TestRealPGDumpToMySQL converts the output of a genuine pg_dump and asserts on
// the details that used to be silently wrong. The generated SQL from this fixture
// has been verified to apply cleanly against MySQL 8.
func TestRealPGDumpToMySQL(t *testing.T) {
	schema, err := postgres.NewPostgreSQL().Parse(realPGDump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 2)

	customers := schema.Tables[0]
	require.Equal(t, "customers", customers.Name)

	t.Run("character varying keeps its length", func(t *testing.T) {
		email := columnByName(t, customers, "email")
		// "character varying(255)" used to collapse to a bare CHAR, silently
		// truncating every value to a single byte.
		assert.Equal(t, "varchar", email.DataType)
		assert.Equal(t, 255, email.Length)
	})

	t.Run("nextval default becomes auto increment", func(t *testing.T) {
		id := columnByName(t, customers, "id")
		assert.True(t, id.AutoIncrement, "column wired to a sequence must be auto-increment")
		assert.Empty(t, id.DefaultValue, "the nextval call must not survive as a literal default")
	})

	t.Run("array column is flagged", func(t *testing.T) {
		tags := columnByName(t, customers, "tags")
		assert.True(t, tags.IsArray)
		assert.Equal(t, "text", tags.DataType)
	})

	t.Run("sequences are parsed", func(t *testing.T) {
		// pg_dump orders the options START WITH / INCREMENT BY / NO MINVALUE,
		// which an ordered single-pattern match could not read.
		require.Len(t, schema.Sequences, 1)
		assert.Equal(t, "customers_id_seq", schema.Sequences[0].Name)
		assert.Equal(t, 1, schema.Sequences[0].StartValue)
		assert.Equal(t, 1, schema.Sequences[0].IncrementBy)
	})

	t.Run("foreign key target loses its schema prefix", func(t *testing.T) {
		invoices := schema.Tables[1]
		fk := constraintByType(t, invoices, "FOREIGN KEY")
		assert.Equal(t, "customers", fk.RefTable, "public. prefix is meaningless in MySQL")
	})

	out, err := mysql.NewMySQL().Generate(schema)
	require.NoError(t, err)

	t.Run("generated MySQL is valid", func(t *testing.T) {
		assert.Contains(t, out, "email VARCHAR(255) NOT NULL")
		assert.Contains(t, out, "id BIGINT AUTO_INCREMENT PRIMARY KEY")
		assert.Contains(t, out, "tags JSON", "MySQL has no array type")
		assert.Contains(t, out, "is_active TINYINT(1) NOT NULL DEFAULT 1",
			"DEFAULT 'true' is rejected by MySQL in strict mode")
		assert.Contains(t, out, "REFERENCES customers (id)")

		assert.NotContains(t, out, "TEXT[]", "array syntax is a MySQL parse error")
		assert.NotContains(t, out, "public.", "PostgreSQL schema prefix must not leak")
		assert.NotContains(t, out, "::", "PostgreSQL casts are a MySQL parse error")
		assert.NotContains(t, out, "CHAR NOT NULL", "varchar must not degrade to char")
	})

	t.Run("primary key is declared exactly once per table", func(t *testing.T) {
		// Emitting both an inline PRIMARY KEY and a table-level constraint made
		// MySQL fail with "Multiple primary key defined".
		for _, table := range []string{"customers", "invoices"} {
			body := tableBody(t, out, table)
			assert.Equal(t, 1, strings.Count(body, "PRIMARY KEY"),
				"table %s declares its primary key more than once:\n%s", table, body)
		}
	})

	t.Run("views are emitted", func(t *testing.T) {
		require.Len(t, schema.Views, 1)
		assert.Contains(t, out, "CREATE VIEW active_customers AS")
	})
}

// realMySQLDump mirrors mysqldump output: conditional comments, backtick quoting,
// LOCK TABLES noise, and an ON UPDATE clause following a NULL default.
const realMySQLDump = "-- MySQL dump 10.13  Distrib 8.0.35\n" +
	"/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n" +
	"\n" +
	"DROP TABLE IF EXISTS `users`;\n" +
	"CREATE TABLE `users` (\n" +
	"  `id` bigint unsigned NOT NULL AUTO_INCREMENT,\n" +
	"  `email` varchar(255) COLLATE utf8mb4_unicode_ci NOT NULL,\n" +
	"  `status` enum('active','banned') COLLATE utf8mb4_unicode_ci NOT NULL DEFAULT 'active',\n" +
	"  `meta` json DEFAULT NULL,\n" +
	"  `created_at` timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,\n" +
	"  `updated_at` timestamp NULL DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP,\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  UNIQUE KEY `uq_users_email` (`email`)\n" +
	") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n" +
	"\n" +
	"LOCK TABLES `users` WRITE;\n" +
	"INSERT INTO `users` VALUES (1,'a@b.com','active',NULL,'2024-01-01 00:00:00',NULL);\n" +
	"UNLOCK TABLES;\n" +
	"\n" +
	"CREATE VIEW `active_users` AS SELECT `id`,`email` FROM `users` WHERE `status` = 'active';\n"

// TestRealMySQLDumpToPostgres converts genuine mysqldump output. The generated SQL
// from this fixture has been verified to apply cleanly against PostgreSQL 17.
func TestRealMySQLDumpToPostgres(t *testing.T) {
	schema, err := mysql.NewMySQL().Parse(realMySQLDump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)

	users := schema.Tables[0]

	t.Run("DEFAULT NULL is not a literal", func(t *testing.T) {
		// This used to be carried through as the string "NULL" and then quoted,
		// producing DEFAULT 'NULL', which PostgreSQL rejects for jsonb.
		meta := columnByName(t, users, "meta")
		assert.Empty(t, meta.DefaultValue)
	})

	t.Run("ON UPDATE does not become the default", func(t *testing.T) {
		// "DEFAULT NULL ON UPDATE CURRENT_TIMESTAMP" used to read the update
		// trigger as the column default.
		updated := columnByName(t, users, "updated_at")
		assert.Empty(t, updated.DefaultValue)
		created := columnByName(t, users, "created_at")
		assert.Equal(t, "CURRENT_TIMESTAMP", created.DefaultValue)
	})

	out, err := postgres.NewPostgreSQL().Generate(schema)
	require.NoError(t, err)

	t.Run("generated PostgreSQL is valid", func(t *testing.T) {
		assert.Contains(t, out, "CREATE TYPE users_status_enum AS ENUM ('active', 'banned')")
		assert.Contains(t, out, "id BIGSERIAL PRIMARY KEY")
		assert.Contains(t, out, "meta JSONB")
		assert.NotContains(t, out, "DEFAULT 'NULL'", "the literal string NULL is not a valid jsonb value")
		assert.NotContains(t, out, "updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP")
	})

	t.Run("views are emitted", func(t *testing.T) {
		require.Len(t, schema.Views, 1)
		assert.Contains(t, out, "CREATE VIEW active_users AS")
	})
}

// TestCommentBeforeStatementIsNotSwallowed guards the dialects that split on the
// statement delimiter. A comment line sitting above CREATE TABLE used to end up
// glued to the statement, which was then discarded as if it were a comment, so
// the table vanished with no error reported.
func TestCommentBeforeStatementIsNotSwallowed(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		const dump = `-- Example schema

-- User management tables
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE
);

-- Index on the username
CREATE INDEX idx_users_username ON users(username);
`
		schema, err := sqlite.NewSQLite().Parse(dump)
		require.NoError(t, err)
		require.Len(t, schema.Tables, 1)
		assert.Equal(t, "users", schema.Tables[0].Name)
		assert.Len(t, schema.Tables[0].Indexes, 1)
	})

	t.Run("sqlserver", func(t *testing.T) {
		const dump = `-- Create Tables
CREATE TABLE app.users (
    id INT IDENTITY(1,1) PRIMARY KEY,
    username NVARCHAR(50) NOT NULL
);

-- Index on the username
CREATE INDEX idx_users_username ON app.users(username);
`
		schema, err := sqlserver.NewSQLServer().Parse(dump)
		require.NoError(t, err)
		require.Len(t, schema.Tables, 1)
		assert.Equal(t, "users", schema.Tables[0].Name)
		assert.Len(t, schema.Tables[0].Indexes, 1,
			"the index table name arrives glued to the column list")
	})
}

// alphabeticalMySQLDump reproduces what mysqldump actually emits: tables in
// alphabetical order, which puts the child table before its parent.
const alphabeticalMySQLDump = "CREATE TABLE `orders` (\n" +
	"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
	"  `user_id` bigint NOT NULL,\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  CONSTRAINT `fk_orders_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`) ON DELETE CASCADE\n" +
	") ENGINE=InnoDB;\n" +
	"CREATE TABLE `users` (\n" +
	"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
	"  `email` varchar(255) NOT NULL,\n" +
	"  PRIMARY KEY (`id`)\n" +
	") ENGINE=InnoDB;\n"

// TestTablesAreEmittedInDependencyOrder guards the case that made real dumps
// unloadable: mysqldump sorts tables alphabetically, so "orders" preceded
// "users" and the generated SQL failed with "relation \"users\" does not exist".
func TestTablesAreEmittedInDependencyOrder(t *testing.T) {
	schema, err := mysql.NewMySQL().Parse(alphabeticalMySQLDump)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 2)
	require.Equal(t, "orders", schema.Tables[0].Name, "the source really does list the child first")

	t.Run("postgres output", func(t *testing.T) {
		out, err := postgres.NewPostgreSQL().Generate(schema)
		require.NoError(t, err)
		assert.Less(t, strings.Index(out, "CREATE TABLE users"), strings.Index(out, "CREATE TABLE orders"))
	})

	t.Run("mysql output", func(t *testing.T) {
		out, err := mysql.NewMySQL().Generate(schema)
		require.NoError(t, err)
		assert.Less(t, strings.Index(out, "CREATE TABLE users"), strings.Index(out, "CREATE TABLE orders"))
	})
}

// TestCircularForeignKeysAreDeferred covers the case ordering cannot fix: two
// tables referencing each other. One side has to become a trailing ALTER TABLE.
func TestCircularForeignKeysAreDeferred(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name: "a",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "bigint", AutoIncrement: true},
					{Name: "b_id", DataType: "bigint"},
				},
				Constraints: []sqlmapper.Constraint{
					{Name: "a_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
					{Name: "fk_a_b", Type: "FOREIGN KEY", Columns: []string{"b_id"}, RefTable: "b", RefColumns: []string{"id"}},
				},
			},
			{
				Name: "b",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "bigint", AutoIncrement: true},
					{Name: "a_id", DataType: "bigint"},
				},
				Constraints: []sqlmapper.Constraint{
					{Name: "b_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
					{Name: "fk_b_a", Type: "FOREIGN KEY", Columns: []string{"a_id"}, RefTable: "a", RefColumns: []string{"id"}},
				},
			},
		},
	}

	for name, generate := range map[string]func(*sqlmapper.Schema) (string, error){
		"postgres": postgres.NewPostgreSQL().Generate,
		"mysql":    mysql.NewMySQL().Generate,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := generate(schema)
			require.NoError(t, err)

			// Exactly one of the two directions is deferred; the other stays inline.
			assert.Equal(t, 1, strings.Count(out, "ALTER TABLE"),
				"one side of the cycle must be added after both tables exist:\n%s", out)

			alterIdx := strings.Index(out, "ALTER TABLE")
			assert.Greater(t, alterIdx, strings.Index(out, "CREATE TABLE a"))
			assert.Greater(t, alterIdx, strings.Index(out, "CREATE TABLE b"))
		})
	}
}

// TestMariaDBJSONValidCheckIsDropped covers MariaDB's JSON emulation: it stores
// JSON as LONGTEXT guarded by CHECK (json_valid(col)). PostgreSQL has no such
// function, and the column becomes a real jsonb, so the check must not survive.
func TestMariaDBJSONValidCheckIsDropped(t *testing.T) {
	const dump = "CREATE TABLE `users` (\n" +
		"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
		"  `meta` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL CHECK (json_valid(`meta`)),\n" +
		"  PRIMARY KEY (`id`)\n" +
		") ENGINE=InnoDB;\n"

	schema, err := mysql.NewMySQL().Parse(dump)
	require.NoError(t, err)

	// No target dialect has a json_valid function, so the guard must be dropped
	// for all of them, not just the one that happened to be fixed first.
	for name, generate := range map[string]func(*sqlmapper.Schema) (string, error){
		"postgres":  postgres.NewPostgreSQL().Generate,
		"oracle":    oracle.NewOracle().Generate,
		"sqlserver": sqlserver.NewSQLServer().Generate,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := generate(schema)
			require.NoError(t, err)
			assert.NotContains(t, out, "json_valid", "%s has no json_valid function", name)
		})
	}

	// MySQL does have JSON_VALID, so nothing is dropped on that path.
	out, err := mysql.NewMySQL().Generate(schema)
	require.NoError(t, err)
	assert.Contains(t, out, "json_valid", "MySQL has the function, so the check survives")
}
