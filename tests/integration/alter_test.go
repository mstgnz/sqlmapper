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

// A dump is not the only thing handed to this converter: a hand-written
// migration is DDL too, and it states most of its schema in ALTER rather than
// in CREATE. Every dialect used to recognise only the forms its own dump tool
// emits, so of the thirty-three cases below five worked and twenty-eight were
// discarded in silence. ALTER TABLE ADD COLUMN was read by one of the five: the
// column simply did not appear in the output, and nothing said so.

// alterBase is the same two-column table written five ways, with each dialect's
// statement terminator.
var alterBase = map[string]struct{ table, term string }{
	"postgres":  {"CREATE TABLE t (id integer NOT NULL, email varchar(50));", ";"},
	"mysql":     {"CREATE TABLE t (id int NOT NULL, email varchar(50));", ";"},
	"oracle":    {"CREATE TABLE t (id NUMBER, email VARCHAR2(50))\n/", "\n/"},
	"sqlserver": {"CREATE TABLE t (id INT, email VARCHAR(50));\nGO", ";\nGO"},
	"sqlite":    {"CREATE TABLE t (id INTEGER, email TEXT);", ";"},
}

var alterParsers = map[string]func() sqlmapper.Database{
	"postgres":  postgres.NewPostgreSQL,
	"mysql":     mysql.NewMySQL,
	"oracle":    oracle.NewOracle,
	"sqlserver": sqlserver.NewSQLServer,
	"sqlite":    sqlite.NewSQLite,
}

var alterDialects = []string{"postgres", "mysql", "oracle", "sqlserver", "sqlite"}

func columnNames(s *sqlmapper.Schema) []string {
	var out []string
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			out = append(out, c.Name)
		}
	}
	return out
}

func hasName(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

func column(s *sqlmapper.Schema, name string) (sqlmapper.Column, bool) {
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			if strings.EqualFold(c.Name, name) {
				return c, true
			}
		}
	}
	return sqlmapper.Column{}, false
}

// TestAlterIsReplayed reads a table and then changes it, the way a migration
// does, and checks the schema shows the change. An empty statement for a
// dialect means the dialect has no such form, which is stated rather than
// skipped so that one gaining it is noticed.
func TestAlterIsReplayed(t *testing.T) {
	cases := []struct {
		name  string
		alter map[string]string
		check func(*sqlmapper.Schema) string
	}{
		{"add column", map[string]string{
			"postgres":  "ALTER TABLE t ADD COLUMN note varchar(20)",
			"mysql":     "ALTER TABLE t ADD COLUMN note varchar(20)",
			"oracle":    "ALTER TABLE t ADD (note VARCHAR2(20))",
			"sqlserver": "ALTER TABLE t ADD note VARCHAR(20)",
			"sqlite":    "ALTER TABLE t ADD COLUMN note TEXT",
		}, func(s *sqlmapper.Schema) string {
			if hasName(columnNames(s), "note") {
				return ""
			}
			return "the added column is missing: " + strings.Join(columnNames(s), ",")
		}},

		{"drop column", map[string]string{
			"postgres":  "ALTER TABLE t DROP COLUMN email",
			"mysql":     "ALTER TABLE t DROP COLUMN email",
			"oracle":    "ALTER TABLE t DROP COLUMN email",
			"sqlserver": "ALTER TABLE t DROP COLUMN email",
			"sqlite":    "ALTER TABLE t DROP COLUMN email",
		}, func(s *sqlmapper.Schema) string {
			if !hasName(columnNames(s), "email") {
				return ""
			}
			return "the dropped column is still there: " + strings.Join(columnNames(s), ",")
		}},

		{"rename column", map[string]string{
			"postgres":  "ALTER TABLE t RENAME COLUMN email TO mail",
			"mysql":     "ALTER TABLE t RENAME COLUMN email TO mail",
			"oracle":    "ALTER TABLE t RENAME COLUMN email TO mail",
			"sqlserver": "EXEC sp_rename 't.email', 'mail', 'COLUMN'",
			"sqlite":    "ALTER TABLE t RENAME COLUMN email TO mail",
		}, func(s *sqlmapper.Schema) string {
			if hasName(columnNames(s), "mail") && !hasName(columnNames(s), "email") {
				return ""
			}
			return "not renamed: " + strings.Join(columnNames(s), ",")
		}},

		{"rename table", map[string]string{
			"postgres":  "ALTER TABLE t RENAME TO t2",
			"mysql":     "ALTER TABLE t RENAME TO t2",
			"oracle":    "ALTER TABLE t RENAME TO t2",
			"sqlserver": "EXEC sp_rename 't', 't2'",
			"sqlite":    "ALTER TABLE t RENAME TO t2",
		}, func(s *sqlmapper.Schema) string {
			for _, tb := range s.Tables {
				if strings.EqualFold(tb.Name, "t2") {
					return ""
				}
			}
			return "not renamed"
		}},

		// SQLite has no MODIFY at all: changing a column means rebuilding the
		// table, which is why the three cases below leave it out.
		{"change the type", map[string]string{
			"postgres":  "ALTER TABLE t ALTER COLUMN email TYPE varchar(200)",
			"mysql":     "ALTER TABLE t MODIFY COLUMN email varchar(200)",
			"oracle":    "ALTER TABLE t MODIFY (email VARCHAR2(200))",
			"sqlserver": "ALTER TABLE t ALTER COLUMN email VARCHAR(200)",
		}, func(s *sqlmapper.Schema) string {
			c, ok := column(s, "email")
			if ok && c.Length == 200 {
				return ""
			}
			return "the new length was not applied"
		}},

		{"set not null", map[string]string{
			"postgres":  "ALTER TABLE t ALTER COLUMN email SET NOT NULL",
			"mysql":     "ALTER TABLE t MODIFY COLUMN email varchar(50) NOT NULL",
			"oracle":    "ALTER TABLE t MODIFY (email NOT NULL)",
			"sqlserver": "ALTER TABLE t ALTER COLUMN email VARCHAR(50) NOT NULL",
		}, func(s *sqlmapper.Schema) string {
			c, ok := column(s, "email")
			if ok && !c.IsNullable {
				return ""
			}
			return "still nullable"
		}},

		{"set default", map[string]string{
			"postgres":  "ALTER TABLE t ALTER COLUMN email SET DEFAULT 'none'",
			"mysql":     "ALTER TABLE t ALTER COLUMN email SET DEFAULT 'none'",
			"oracle":    "ALTER TABLE t MODIFY (email DEFAULT 'none')",
			"sqlserver": "ALTER TABLE t ADD CONSTRAINT df_email DEFAULT 'none' FOR email",
		}, func(s *sqlmapper.Schema) string {
			c, ok := column(s, "email")
			if !ok || c.DefaultValue == "" {
				return "no default"
			}
			// The schema holds the value, not the literal: every generator
			// quotes it again for its own dialect.
			if c.DefaultValue != "none" {
				return "the default kept its quoting: " + c.DefaultValue
			}
			return ""
		}},

		// SQLite has no DROP CONSTRAINT: a constraint is dropped by rebuilding
		// the table.
		{"drop constraint", map[string]string{
			"postgres":  "ALTER TABLE t ADD CONSTRAINT uq_email UNIQUE (email);\nALTER TABLE t DROP CONSTRAINT uq_email",
			"mysql":     "ALTER TABLE t ADD CONSTRAINT uq_email UNIQUE (email);\nALTER TABLE t DROP CONSTRAINT uq_email",
			"oracle":    "ALTER TABLE t ADD CONSTRAINT uq_email UNIQUE (email)\n/\nALTER TABLE t DROP CONSTRAINT uq_email",
			"sqlserver": "ALTER TABLE t ADD CONSTRAINT uq_email UNIQUE (email);\nGO\nALTER TABLE t DROP CONSTRAINT uq_email",
		}, func(s *sqlmapper.Schema) string {
			for _, tb := range s.Tables {
				for _, c := range tb.Constraints {
					if strings.EqualFold(c.Name, "uq_email") {
						return "the constraint survived the drop"
					}
				}
			}
			return ""
		}},
	}

	for _, c := range cases {
		for _, d := range alterDialects {
			stmt, ok := c.alter[d]
			if !ok || stmt == "" {
				continue // the dialect has no such form
			}
			t.Run(c.name+"/"+d, func(t *testing.T) {
				b := alterBase[d]
				schema, err := alterParsers[d]().Parse(b.table + "\n" + stmt + b.term + "\n")
				if err != nil {
					t.Fatalf("parse: %v", err)
				}
				if msg := c.check(schema); msg != "" {
					t.Error(msg)
				}
			})
		}
	}
}

// TestAlterSurvivesTheConversion checks the replayed change reaches the output,
// not only the schema. A column added by an ALTER and then dropped by the
// generator would be the same loss one step later.
func TestAlterSurvivesTheConversion(t *testing.T) {
	for _, src := range alterDialects {
		b := alterBase[src]
		add := map[string]string{
			"postgres":  "ALTER TABLE t ADD COLUMN note varchar(20)",
			"mysql":     "ALTER TABLE t ADD COLUMN note varchar(20)",
			"oracle":    "ALTER TABLE t ADD (note VARCHAR2(20))",
			"sqlserver": "ALTER TABLE t ADD note VARCHAR(20)",
			"sqlite":    "ALTER TABLE t ADD COLUMN note TEXT",
		}[src]

		schema, err := alterParsers[src]().Parse(b.table + "\n" + add + b.term + "\n")
		if err != nil {
			t.Fatalf("%s parse: %v", src, err)
		}

		for _, dst := range alterDialects {
			t.Run(src+"_to_"+dst, func(t *testing.T) {
				out, err := alterParsers[dst]().Generate(schema)
				if err != nil {
					t.Fatalf("generate: %v", err)
				}
				if !strings.Contains(out, "note") {
					t.Errorf("the added column did not reach the output:\n%s", out)
				}
			})
		}
	}
}

// TestStreamDoesNotReplayAlter pins a limit rather than letting it be an
// accident.
//
// The streaming parsers hand back one object per statement and hold no schema
// of their own, so there is nothing for an ALTER to change: adding a column to
// a table read three statements ago is not something the callback can express.
// Whole-file Parse replays them and is what the CLI uses; the stream is for a
// dump too large to hold, which is CREATE-heavy by nature. If this test starts
// failing because a stream grew the ability, that is a change worth noticing.
func TestStreamDoesNotReplayAlter(t *testing.T) {
	const script = "CREATE TABLE t (id INT);\nALTER TABLE t ADD COLUMN note VARCHAR(20);\n"

	streams := map[string]func() stream.StreamParser{
		"postgres":  func() stream.StreamParser { return postgres.NewPostgreSQLStreamParser() },
		"mysql":     func() stream.StreamParser { return mysql.NewMySQLStreamParser() },
		"sqlite":    func() stream.StreamParser { return sqlite.NewSQLiteStreamParser() },
		"sqlserver": func() stream.StreamParser { return sqlserver.NewSQLServerStreamParser() },
	}

	for name, newParser := range streams {
		t.Run(name, func(t *testing.T) {
			var tables []*sqlmapper.Table
			err := newParser().ParseStream(strings.NewReader(script), func(obj stream.SchemaObject) error {
				if obj.Type == stream.TableObject {
					if tb, ok := obj.Data.(*sqlmapper.Table); ok {
						tables = append(tables, tb)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			if len(tables) != 1 {
				t.Fatalf("want one table, got %d", len(tables))
			}
			for _, c := range tables[0].Columns {
				if strings.EqualFold(c.Name, "note") {
					t.Fatal("the stream replayed an ALTER; that is new, and the " +
						"comment above this test needs rewriting")
				}
			}
		})
	}
}

// TestDropIsReplayed reads a schema that creates two tables and then drops one.
// A file that created a table and dropped it used to keep both, so the output
// declared something the source no longer had.
func TestDropIsReplayed(t *testing.T) {
	base := map[string]string{
		"postgres":  "CREATE TABLE keep (a integer);\nCREATE TABLE gone (b integer);\nDROP TABLE gone;\n",
		"mysql":     "CREATE TABLE keep (a int);\nCREATE TABLE gone (b int);\nDROP TABLE gone;\n",
		"oracle":    "CREATE TABLE keep (a NUMBER)\n/\nCREATE TABLE gone (b NUMBER)\n/\nDROP TABLE gone\n/\n",
		"sqlserver": "CREATE TABLE keep (a INT);\nGO\nCREATE TABLE gone (b INT);\nGO\nDROP TABLE gone;\nGO\n",
		"sqlite":    "CREATE TABLE keep (a INTEGER);\nCREATE TABLE gone (b INTEGER);\nDROP TABLE gone;\n",
	}

	for _, d := range alterDialects {
		t.Run(d, func(t *testing.T) {
			schema, err := alterParsers[d]().Parse(base[d])
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var names []string
			for _, tb := range schema.Tables {
				names = append(names, tb.Name)
			}
			if len(names) != 1 || !strings.EqualFold(names[0], "keep") {
				t.Errorf("tables = %v, want [keep]", names)
			}
		})
	}
}

// TestMakeRoomDropIsNotADrop is the rule that keeps a dump readable. Every dump
// tool writes a drop ahead of the CREATE that replaces it, and treating those
// as real deleted the whole schema.
func TestMakeRoomDropIsNotADrop(t *testing.T) {
	dumps := map[string]string{
		"postgres": "DROP TABLE IF EXISTS keep;\nCREATE TABLE keep (a integer);\n",
		"mysql":    "DROP TABLE IF EXISTS `keep`;\nCREATE TABLE `keep` (a int);\n",
		"sqlite":   "DROP TABLE IF EXISTS keep;\nCREATE TABLE keep (a INTEGER);\n",
		"sqlserver": "IF OBJECT_ID('keep') IS NOT NULL DROP TABLE keep;\nGO\n" +
			"CREATE TABLE keep (a INT);\nGO\n",
	}

	for d, dump := range dumps {
		t.Run(d, func(t *testing.T) {
			schema, err := alterParsers[d]().Parse(dump)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(schema.Tables) != 1 {
				t.Errorf("the make-room drop removed the table it made room for: %+v", schema.Tables)
			}
		})
	}
}

// TestFixturesSurviveTheirOwnDropStatements guards the comprehensive fixtures,
// which are real dump output and carry the drops a dump tool writes.
func TestFixturesSurviveTheirOwnDropStatements(t *testing.T) {
	for _, d := range fullSchemas {
		t.Run(d.name, func(t *testing.T) {
			schema, err := d.parser().Parse(loadFullSchema(t, d.file))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(schema.Tables) < 3 {
				t.Errorf("a drop in the dump removed a table it recreates: %d tables", len(schema.Tables))
			}
		})
	}
}
