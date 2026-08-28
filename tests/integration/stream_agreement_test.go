package integration

import (
	"fmt"
	"sort"
	"strings"
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
