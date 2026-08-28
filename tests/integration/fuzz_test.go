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
	"github.com/mstgnz/sqlmapper/stream"
)

// A parser may reject anything, but it may never panic: this is a library, and
// a panic takes the caller's process down with it. Malformed SQL is not exotic
// input here, it is a truncated download or a dump from a database nobody
// tested against.
//
// Running go test exercises the seeds below. Running
//
//	go test ./tests/integration -run FuzzParseNeverPanics -fuzz FuzzParseNeverPanics
//
// keeps mutating them. That found unguarded slices in three places: a pair of
// parenthesis positions used without checking they were in order, so ")(" read
// backwards, and a column whose IDENTITY clause was stripped after its fields
// had already been counted, so the offset ran past the end of what was left.
func FuzzParseNeverPanics(f *testing.F) {
	seeds := []string{
		"",
		" ",
		";",
		"CREATE TABLE",
		"CREATE TABLE t",
		"CREATE TABLE t (",
		"CREATE TABLE t )(",
		"CREATE TABLE t (id INT",
		"CREATE TABLE t (id INT)",

		// A parenthesis pair in the wrong order, inside a constraint.
		"CREATE TABLE t (id INT, CONSTRAINT c PRIMARY KEY) (id)",
		"CREATE TABLE t (id INT, CONSTRAINT c FOREIGN KEY) (a) REFERENCES b) (c)",
		"CREATE TABLE t (id INT, CONSTRAINT c CHECK) (id > 0)",
		"CREATE INDEX i ON t )(",

		// A column whose IDENTITY is longer than what is left after it goes.
		"CREATE TABLE t (IDENTITY(1,1) x)",
		"CREATE TABLE [t]([id] [int] IDENTITY(1,1) NOT NULL)",

		// Unbalanced quoting of every kind.
		"CREATE TABLE t (id INT DEFAULT 'unterminated)",
		"CREATE TABLE `t` (`id` int",
		`CREATE TABLE "t" ("id" int`,
		"CREATE FUNCTION f() RETURNS int AS $$ unterminated",
		"/*! unterminated",
		"-- comment with no end",

		// Routine bodies cut in the middle.
		"DELIMITER ;;\nCREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW BEGIN",
		"CREATE OR REPLACE TRIGGER t BEGIN\n/",
		"CREATE TABLE t (id INT)\nGO\nALTER TABLE t ADD  CONSTRAINT",
		"ALTER TABLE ONLY x ADD CONSTRAINT c PRIMARY KEY (",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	parsers := map[string]func() sqlmapper.Database{
		"mysql":     mysql.NewMySQL,
		"postgres":  postgres.NewPostgreSQL,
		"sqlite":    sqlite.NewSQLite,
		"oracle":    oracle.NewOracle,
		"sqlserver": sqlserver.NewSQLServer,
	}
	streams := map[string]func() stream.StreamParser{
		"mysql":     func() stream.StreamParser { return mysql.NewMySQLStreamParser() },
		"postgres":  func() stream.StreamParser { return postgres.NewPostgreSQLStreamParser() },
		"sqlite":    func() stream.StreamParser { return sqlite.NewSQLiteStreamParser() },
		"oracle":    func() stream.StreamParser { return oracle.NewOracleStreamParser() },
		"sqlserver": func() stream.StreamParser { return sqlserver.NewSQLServerStreamParser() },
	}

	f.Fuzz(func(t *testing.T, input string) {
		for name, mk := range parsers {
			schema, err := mk().Parse(input)
			if err != nil {
				continue
			}
			// Whatever came out has to survive being written again, in every
			// dialect: a generator reading a half-built schema is the other
			// half of this.
			for _, gen := range parsers {
				if _, err := gen().Generate(schema); err != nil {
					continue
				}
			}
			_ = name
		}

		for _, mk := range streams {
			_ = mk().ParseStream(strings.NewReader(input), func(stream.SchemaObject) error { return nil })
		}
	})
}
