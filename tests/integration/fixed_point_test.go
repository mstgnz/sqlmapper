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

// Converting a dump and converting the result again has to land on the same
// text. Anything else means the generator writes something its own parser reads
// differently, and repeating the conversion keeps changing the schema.
//
// It caught a crop of them: SQL Server wrote REFERENCES S customers because
// REFERENCES was sliced at nine characters, MySQL wrote a key prefix its own
// pattern could not read back so the list never closed, Oracle and SQLite kept
// the quotes on a default and added another pair on every pass, PostgreSQL
// emitted no CREATE TYPE for a type it had just parsed, Oracle dropped NOCACHE,
// and SQLite forgot a CHECK written on a column.
//
// What it does not cover is a type with no faithful equivalent, which widens on
// the way back: Oracle NUMBER(5) holds more than a smallint, so it becomes an
// int and returns as NUMBER(10), and SQL Server BIT has no boolean to return to.
// Those are the mapping choices the README documents.
func TestConversionReachesAFixedPoint(t *testing.T) {
	dialects := map[string]func() sqlmapper.Database{
		"mysql":     mysql.NewMySQL,
		"postgres":  postgres.NewPostgreSQL,
		"sqlite":    sqlite.NewSQLite,
		"oracle":    oracle.NewOracle,
		"sqlserver": sqlserver.NewSQLServer,
	}

	sources := []struct {
		name    string
		dialect string
		dump    string
	}{
		{"mysql", "mysql", fixedPointMySQL},
		{"postgres", "postgres", fixedPointPostgres},
		{"sqlite", "sqlite", fixedPointSQLite},
		{"oracle", "oracle", fixedPointOracle},
		{"sqlserver", "sqlserver", fixedPointSQLServer},
	}

	for _, src := range sources {
		schema, err := dialects[src.dialect]().Parse(src.dump)
		require.NoError(t, err)

		for target, mk := range dialects {
			if widensOnTheWayBack[src.name+"_to_"+target] {
				continue
			}
			t.Run(src.name+"_to_"+target, func(t *testing.T) {
				first, err := mk().Generate(schema)
				require.NoError(t, err)

				reparsed, err := mk().Parse(first)
				require.NoError(t, err, "the generator's own output has to parse")

				second, err := mk().Generate(reparsed)
				require.NoError(t, err)

				// The provenance notes are compared away. A "not carried" line
				// states something the target could not hold, so it appears on
				// the first conversion and not on the second: by then the object
				// it describes is no longer in the schema to be missed. That is
				// the note doing its job, not the schema failing to converge.
				assert.Equal(t, withoutNotes(first), withoutNotes(second))
			})
		}
	}
}

// widensOnTheWayBack names the pairs where a type has no faithful equivalent in
// the target, so converting the result again widens it. SQLite has one integer
// class and says nothing about its width, so its key becomes Oracle NUMBER(10),
// which holds more than an int and returns as NUMBER(19). These are the mapping
// choices the README documents, not defects.
var widensOnTheWayBack = map[string]bool{
	"sqlite_to_oracle": true,
}

// The fixtures avoid the other types that widen, so a failure here is a defect
// rather than a documented mapping choice.
const fixedPointMySQL = "CREATE TABLE `users` (\n" +
	"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
	"  `email` text NOT NULL,\n" +
	"  `status` varchar(20) NOT NULL DEFAULT 'active',\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  UNIQUE KEY `uq_email` (`email`(255))\n" +
	") ENGINE=InnoDB;\n"

const fixedPointPostgres = `CREATE TYPE status_t AS ENUM ('active', 'banned');
CREATE TABLE public.users (
    id bigint NOT NULL,
    label character varying(20) DEFAULT 'none'::character varying,
    state status_t
);
ALTER TABLE ONLY public.users ADD CONSTRAINT users_pkey PRIMARY KEY (id);
`

const fixedPointSQLite = `CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    label TEXT DEFAULT 'none',
    meta TEXT CHECK (json_valid(meta))
);
`

const fixedPointOracle = `CREATE SEQUENCE users_seq
START WITH 1
INCREMENT BY 1
NOCACHE;

CREATE TABLE users (
    id NUMBER(19) PRIMARY KEY,
    label VARCHAR2(20) DEFAULT 'none' NOT NULL
);
`

const fixedPointSQLServer = `CREATE TABLE [dbo].[customers](
    [id] [bigint] IDENTITY(1,1) NOT NULL,
 CONSTRAINT [PK_customers] PRIMARY KEY CLUSTERED ([id] ASC)
)
GO
CREATE TABLE [dbo].[invoices](
    [id] [bigint] IDENTITY(1,1) NOT NULL,
    [customer_id] [bigint] NOT NULL,
 CONSTRAINT [PK_invoices] PRIMARY KEY CLUSTERED ([id] ASC),
 CONSTRAINT [FK_invoices] FOREIGN KEY ([customer_id]) REFERENCES [dbo].[customers] ([id]) ON DELETE CASCADE
)
GO
`

// The schema holds a default's value, not the literal that wrote it. Two
// parsers kept the quotes, so a default of 'active' reached PostgreSQL as a
// pair of empty strings around a bare word.
func TestDefaultsAreStoredUnquoted(t *testing.T) {
	tests := []struct {
		name string
		db   func() sqlmapper.Database
		ddl  string
	}{
		{"mysql", mysql.NewMySQL, "CREATE TABLE t (status varchar(20) DEFAULT 'active');"},
		{"postgres", postgres.NewPostgreSQL, "CREATE TABLE t (status varchar(20) DEFAULT 'active'::character varying);"},
		{"sqlite", sqlite.NewSQLite, "CREATE TABLE t (status TEXT DEFAULT 'active');"},
		{"oracle", oracle.NewOracle, "CREATE TABLE t (status VARCHAR2(20) DEFAULT 'active');"},
		{"sqlserver", sqlserver.NewSQLServer, "CREATE TABLE t (status NVARCHAR(20) DEFAULT ('active'));"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema, err := tt.db().Parse(tt.ddl)
			require.NoError(t, err)
			require.NotEmpty(t, schema.Tables)
			assert.Equal(t, "active", schema.Tables[0].Columns[0].DefaultValue)
		})
	}

	// And every generator quotes it again for its own dialect.
	schema := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
		Name:    "t",
		Columns: []sqlmapper.Column{{Name: "status", DataType: "varchar", Length: 20, DefaultValue: "active"}},
	}}}

	for _, tt := range tests {
		t.Run(tt.name+"_generate", func(t *testing.T) {
			out, err := tt.db().Generate(schema)
			require.NoError(t, err)
			assert.Contains(t, out, "'active'")
			assert.NotContains(t, out, "''active''")
		})
	}
}

// A default is written by one shared renderer. Five copies of it disagreed:
// one did not double the quote inside a value, so a default of it's came out as
// 'it's' and would not load; one wrote the string 'NULL' where the others wrote
// no default at all; and three wrote the string 'true' onto an integer column.
func TestDefaultsAgreeAcrossDialects(t *testing.T) {
	dialects := map[string]func() sqlmapper.Database{
		"mysql":     mysql.NewMySQL,
		"postgres":  postgres.NewPostgreSQL,
		"sqlite":    sqlite.NewSQLite,
		"oracle":    oracle.NewOracle,
		"sqlserver": sqlserver.NewSQLServer,
	}

	tests := []struct {
		name     string
		dataType string
		value    string
		want     string   // what every dialect writes
		except   []string // dialects that write something else, and why
	}{
		{name: "a quote inside a value is doubled", dataType: "varchar", value: "it's", want: "DEFAULT 'it''s'"},
		{name: "a string is quoted", dataType: "varchar", value: "active", want: "DEFAULT 'active'"},
		{name: "two words stay one value", dataType: "varchar", value: "two words", want: "DEFAULT 'two words'"},
		{name: "a number is not quoted", dataType: "int", value: "5", want: "DEFAULT 5"},
		{name: "a boolean on an integer column is a number", dataType: "int", value: "true", want: "DEFAULT 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
				Name: "t",
				Columns: []sqlmapper.Column{{
					Name: "c", DataType: tt.dataType, Length: 20,
					DefaultValue: tt.value, IsNullable: true,
				}},
			}}}

			for name, mk := range dialects {
				out, err := mk().Generate(schema)
				require.NoError(t, err)
				assert.Contains(t, out, tt.want, "%s wrote something else", name)
			}
		})
	}

	// A default of NULL is the same as no default, everywhere.
	t.Run("NULL is no default", func(t *testing.T) {
		schema := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
			Name: "t",
			Columns: []sqlmapper.Column{{
				Name: "c", DataType: "varchar", Length: 20,
				DefaultValue: "NULL", IsNullable: true,
			}},
		}}}

		for name, mk := range dialects {
			out, err := mk().Generate(schema)
			require.NoError(t, err)
			assert.NotContains(t, out, "DEFAULT", "%s wrote a default for NULL", name)
		}
	})
}

// withoutNotes drops the provenance comments from generated SQL, which are
// annotations rather than schema: they say what the target could not hold.
func withoutNotes(sql string) string {
	var kept []string
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "-- not carried") ||
			strings.Contains(trimmed, "was partial in the source") ||
			strings.Contains(trimmed, "states no type for it") ||
			strings.Contains(trimmed, "checks every constraint per statement") {
			continue
		}
		kept = append(kept, line)
	}
	// The trailing blank a removed note leaves behind is not schema either.
	return strings.TrimRight(strings.Join(kept, "\n"), "\n")
}
