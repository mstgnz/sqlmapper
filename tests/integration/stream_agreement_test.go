package integration

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/oracle"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/sqlite"
	"github.com/mstgnz/sqlmapper/sqlserver"
	"github.com/mstgnz/sqlmapper/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The two ways to read a dump have to produce the same schema. They did not:
// each package had a second table reader used only by the stream, and on a real
// dump it dropped columns, split a type at the comma inside its precision, left
// the brackets on every SSMS name, and failed outright on Oracle. Neither
// PostgreSQL nor SQL Server reported the constraints their dump tools write as
// separate ALTER TABLE statements, so a streamed schema kept none of its keys.
func TestParseAndParseStreamAgree(t *testing.T) {
	tests := []struct {
		name   string
		dump   string
		file   func() sqlmapper.Database
		stream func() stream.StreamParser
	}{
		{"mysql", mysqlDump, mysql.NewMySQL,
			func() stream.StreamParser { return mysql.NewMySQLStreamParser() }},
		{"postgres", postgresDump, postgres.NewPostgreSQL,
			func() stream.StreamParser { return postgres.NewPostgreSQLStreamParser() }},
		{"sqlite", sqliteDump, sqlite.NewSQLite,
			func() stream.StreamParser { return sqlite.NewSQLiteStreamParser() }},
		{"oracle", oracleDump, oracle.NewOracle,
			func() stream.StreamParser { return oracle.NewOracleStreamParser() }},
		{"sqlserver", sqlserverDump, sqlserver.NewSQLServer,
			func() stream.StreamParser { return sqlserver.NewSQLServerStreamParser() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileSchema, err := tt.file().Parse(tt.dump)
			require.NoError(t, err)

			streamSchema := &sqlmapper.Schema{}
			var looseConstraints []string
			err = tt.stream().ParseStream(strings.NewReader(tt.dump), func(obj stream.SchemaObject) error {
				switch v := obj.Data.(type) {
				case *sqlmapper.Table:
					streamSchema.Tables = append(streamSchema.Tables, *v)
				case *sqlmapper.View:
					streamSchema.Views = append(streamSchema.Views, *v)
				case *sqlmapper.Index:
					streamSchema.Tables[0].Indexes = append(streamSchema.Tables[0].Indexes, *v)
				case *sqlmapper.Constraint:
					looseConstraints = append(looseConstraints, constraintKey(*v))
				}
				return nil
			})
			require.NoError(t, err)

			assert.Equal(t, schemaShape(fileSchema, nil), schemaShape(streamSchema, looseConstraints))
		})
	}
}

// schemaShape reduces a schema to what both readers must agree on. A stream
// reports an index or a constraint on its own, because the table it belongs to
// was already handed to the caller, so the association is left out of the
// comparison rather than the object.
func schemaShape(s *sqlmapper.Schema, looseConstraints []string) string {
	var tables, columns, constraints, indexes, views []string

	for _, t := range s.Tables {
		tables = append(tables, t.Name)
		for _, c := range t.Columns {
			columns = append(columns, t.Name+"."+c.Name+":"+c.DataType)
		}
		for _, c := range t.Constraints {
			constraints = append(constraints, constraintKey(c))
		}
		for _, i := range t.Indexes {
			indexes = append(indexes, i.Name)
		}
	}
	constraints = append(constraints, looseConstraints...)
	for _, v := range s.Views {
		views = append(views, v.Name)
	}

	var b strings.Builder
	for _, set := range [][]string{tables, columns, constraints, indexes, views} {
		sort.Strings(set)
		fmt.Fprintf(&b, "%v\n", set)
	}
	return b.String()
}

func constraintKey(c sqlmapper.Constraint) string {
	return c.Type + "(" + strings.Join(c.Columns, ",") + ")"
}

const mysqlDump = "CREATE TABLE `users` (\n" +
	"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
	"  `email` varchar(255) NOT NULL,\n" +
	"  `score` decimal(10,2) DEFAULT NULL,\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  UNIQUE KEY `uq_email` (`email`)\n" +
	") ENGINE=InnoDB;\n" +
	"CREATE VIEW `active` AS select `users`.`id` AS `id` from `users`;\n"

const postgresDump = `CREATE TABLE public.users (
    id bigint NOT NULL,
    email character varying(255) NOT NULL,
    score numeric(10,2)
);
ALTER TABLE ONLY public.users ADD CONSTRAINT users_pkey PRIMARY KEY (id);
ALTER TABLE ONLY public.users ADD CONSTRAINT users_email_key UNIQUE (email);
CREATE INDEX idx_users_email ON public.users USING btree (email);
CREATE VIEW public.active AS SELECT id FROM public.users;
`

const sqliteDump = `CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    score NUMERIC(10,2),
    CONSTRAINT users_email_key UNIQUE (email)
);
CREATE TABLE sqlite_sequence(name,seq);
CREATE INDEX idx_users_email ON users(email);
CREATE VIEW active AS SELECT id FROM users;
`

const oracleDump = `  CREATE TABLE "APP"."USERS"
   (	"ID" NUMBER NOT NULL ENABLE,
	"EMAIL" VARCHAR2(255) NOT NULL ENABLE,
	"SCORE" NUMBER(10,2),
	 CONSTRAINT "USERS_PK" PRIMARY KEY ("ID")
  USING INDEX  ENABLE
   ) ;

  CREATE INDEX "APP"."IDX_USERS_EMAIL" ON "APP"."USERS" ("EMAIL")
  ;

  CREATE OR REPLACE FORCE EDITIONABLE VIEW "APP"."ACTIVE" ("ID") AS
  SELECT id FROM users;
`

const sqlserverDump = `CREATE TABLE [dbo].[users](
    [id] [bigint] IDENTITY(1,1) NOT NULL,
    [email] [nvarchar](255) NOT NULL,
    [score] [decimal](10, 2) NULL,
 CONSTRAINT [PK_users] PRIMARY KEY CLUSTERED ([id] ASC)
)
GO
ALTER TABLE [dbo].[users]  WITH CHECK ADD  CONSTRAINT [CHK_score] CHECK  (([score]>=(0)))
GO
ALTER TABLE [dbo].[users] CHECK CONSTRAINT [CHK_score]
GO
CREATE UNIQUE NONCLUSTERED INDEX [IX_users_email] ON [dbo].[users] ([email] ASC)
GO
CREATE VIEW [dbo].[active] AS SELECT id FROM dbo.users
GO
`

// ParseStreamParallel is a third reader, and it has to see what ParseStream
// sees. It did not: each stream parser had two dispatches, an if chain in
// ParseStream and a switch in parseStatement, and the constraints one of them
// reported were dropped by the other. Order is not preserved in parallel mode,
// so the objects are compared as a set.
func TestParseStreamAndParallelAgree(t *testing.T) {
	tests := []struct {
		name   string
		dump   string
		stream func() stream.StreamParser
	}{
		{"mysql", mysqlDump, func() stream.StreamParser { return mysql.NewMySQLStreamParser() }},
		{"postgres", postgresDump, func() stream.StreamParser { return postgres.NewPostgreSQLStreamParser() }},
		{"sqlite", sqliteDump, func() stream.StreamParser { return sqlite.NewSQLiteStreamParser() }},
		{"oracle", oracleDump, func() stream.StreamParser { return oracle.NewOracleStreamParser() }},
		{"sqlserver", sqlserverDump, func() stream.StreamParser { return sqlserver.NewSQLServerStreamParser() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serial, err := collectObjects(func(cb func(stream.SchemaObject) error) error {
				return tt.stream().ParseStream(strings.NewReader(tt.dump), cb)
			})
			require.NoError(t, err)
			require.NotEmpty(t, serial)

			parallel, err := collectObjects(func(cb func(stream.SchemaObject) error) error {
				return tt.stream().ParseStreamParallel(strings.NewReader(tt.dump), cb, 4)
			})
			require.NoError(t, err)

			assert.Equal(t, serial, parallel)
		})
	}
}

// collectObjects names every object a reader reports, sorted, because the
// parallel reader does not preserve order.
func collectObjects(run func(func(stream.SchemaObject) error) error) ([]string, error) {
	var mu sync.Mutex
	var out []string

	err := run(func(obj stream.SchemaObject) error {
		mu.Lock()
		defer mu.Unlock()
		switch v := obj.Data.(type) {
		case *sqlmapper.Table:
			out = append(out, "table "+v.Name)
		case *sqlmapper.View:
			out = append(out, "view "+v.Name)
		case *sqlmapper.Index:
			out = append(out, "index "+v.Name)
		case *sqlmapper.Constraint:
			out = append(out, "constraint "+constraintKey(*v))
		case *sqlmapper.Function:
			out = append(out, "function "+v.Name)
		case *sqlmapper.Trigger:
			out = append(out, "trigger "+v.Name)
		default:
			out = append(out, fmt.Sprintf("other %T", obj.Data))
		}
		return nil
	})

	sort.Strings(out)
	return out, err
}

// The two ways to write a schema have to produce the same SQL. They did not:
// the stream wrote a view body as it stood, so another dialect's schema
// qualifier survived and a bare boolean column reached Oracle and SQL Server,
// which have no boolean and reject it. It also wrote no CREATE TYPE for a MySQL
// ENUM column, leaving PostgreSQL tables referring to a type that never existed,
// and an Oracle sequence with CACHE 1, which Oracle rejects outright.
func TestGenerateAndGenerateStreamAgree(t *testing.T) {
	sources := []struct {
		name   string
		dump   string
		parser func() sqlmapper.Database
	}{
		{"mysql", mysqlEnumDump, mysql.NewMySQL},
		{"postgres", postgresDump, postgres.NewPostgreSQL},
		{"sqlite", sqliteDump, sqlite.NewSQLite},
		{"oracle", oracleDump, oracle.NewOracle},
		{"sqlserver", sqlserverDump, sqlserver.NewSQLServer},
	}

	targets := []struct {
		name   string
		file   func() sqlmapper.Database
		stream func() stream.StreamParser
	}{
		{"mysql", mysql.NewMySQL, func() stream.StreamParser { return mysql.NewMySQLStreamParser() }},
		{"postgres", postgres.NewPostgreSQL, func() stream.StreamParser { return postgres.NewPostgreSQLStreamParser() }},
		{"sqlite", sqlite.NewSQLite, func() stream.StreamParser { return sqlite.NewSQLiteStreamParser() }},
		{"oracle", oracle.NewOracle, func() stream.StreamParser { return oracle.NewOracleStreamParser() }},
		{"sqlserver", sqlserver.NewSQLServer, func() stream.StreamParser { return sqlserver.NewSQLServerStreamParser() }},
	}

	for _, src := range sources {
		schema, err := src.parser().Parse(src.dump)
		require.NoError(t, err)

		for _, target := range targets {
			t.Run(src.name+"_to_"+target.name, func(t *testing.T) {
				fileOut, err := target.file().Generate(schema)
				require.NoError(t, err)

				var streamOut strings.Builder
				require.NoError(t, target.stream().GenerateStream(schema, &streamOut))

				assert.Equal(t, sqlStatements(fileOut), sqlStatements(streamOut.String()))
			})
		}
	}
}

// sqlStatements reduces generated SQL to its statements, so the two writers are
// compared on what they emit rather than on how they space it.
func sqlStatements(sql string) []string {
	var out []string
	for _, raw := range strings.Split(sql, ";") {
		s := strings.Join(strings.Fields(raw), " ")
		for _, sep := range []string{"GO", "/"} {
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), sep))
			s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), sep))
		}
		if s != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

const mysqlEnumDump = "CREATE TABLE `users` (\n" +
	"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
	"  `email` varchar(255) NOT NULL,\n" +
	"  `status` enum('active','banned') NOT NULL DEFAULT 'active',\n" +
	"  PRIMARY KEY (`id`),\n" +
	"  UNIQUE KEY `uq_email` (`email`)\n" +
	") ENGINE=InnoDB;\n" +
	"CREATE VIEW `active` AS select `users`.`id` AS `id` from `users` where `users`.`id` > 0;\n"
