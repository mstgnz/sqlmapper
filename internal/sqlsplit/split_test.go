package sqlsplit

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// delimiterFor turns a mode back into the string the callers pass.
func delimiterFor(m Mode) string {
	switch m {
	case Batch:
		return "GO"
	case PLSQL:
		return "/"
	}
	return ";"
}

// all reads every statement, trimmed, which is how the callers use them.
func all(t *testing.T, src string, mode Mode) []string {
	t.Helper()
	s := New(strings.NewReader(src), delimiterFor(mode))

	var out []string
	for {
		stmt, err := s.Next()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		out = append(out, strings.TrimSpace(stmt))
	}
}

func TestSemicolonSplitting(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			"two statements",
			"CREATE TABLE a (id INT); CREATE TABLE b (id INT);",
			[]string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"},
		},
		{
			"a trailing statement without a terminator",
			"CREATE TABLE a (id INT); CREATE TABLE b (id INT)",
			[]string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"},
		},
		{
			"blank statements are skipped",
			"CREATE TABLE a (id INT);;;\n\n;CREATE TABLE b (id INT);",
			[]string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"},
		},
		{
			"a semicolon inside a string is not a terminator",
			"INSERT INTO t VALUES ('a;b'); SELECT 1;",
			[]string{"INSERT INTO t VALUES ('a;b')", "SELECT 1"},
		},
		{
			"a doubled quote does not end the string",
			"INSERT INTO t VALUES ('it''s; fine'); SELECT 1;",
			[]string{"INSERT INTO t VALUES ('it''s; fine')", "SELECT 1"},
		},
		{
			"a backslash escape does not end the string",
			`INSERT INTO t VALUES ('it\'s; fine'); SELECT 1;`,
			[]string{`INSERT INTO t VALUES ('it\'s; fine')`, "SELECT 1"},
		},
		{
			"a semicolon inside a quoted identifier is not a terminator",
			"CREATE TABLE `a;b` (id INT); SELECT 1;",
			[]string{"CREATE TABLE `a;b` (id INT)", "SELECT 1"},
		},
		{
			"a semicolon inside a bracketed identifier is not a terminator",
			"CREATE TABLE [a;b] (id INT); SELECT 1;",
			[]string{"CREATE TABLE [a;b] (id INT)", "SELECT 1"},
		},
		{
			// Comments are dropped, as the reader this replaced dropped them:
			// the dialect parsers dispatch on a statement's first keyword, and a
			// comment left in front of one hides it completely.
			"a semicolon inside a line comment is not a terminator",
			"SELECT 1 -- a; b\n; SELECT 2;",
			[]string{"SELECT 1", "SELECT 2"},
		},
		{
			"a semicolon inside a block comment is not a terminator",
			"SELECT 1 /* a; b */; SELECT 2;",
			[]string{"SELECT 1", "SELECT 2"},
		},
		{
			"a comment before a statement does not hide it",
			"-- Table structure for table `users`\nCREATE TABLE users (id INT);",
			[]string{"CREATE TABLE users (id INT)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, all(t, tt.src, Semicolon))
		})
	}
}

// TestMySQLDelimiterDirective covers what mysqldump writes around every routine.
// Splitting on the semicolon cut the trigger body in half and everything after
// it was lost.
func TestMySQLDelimiterDirective(t *testing.T) {
	const dump = "CREATE TABLE users (id INT PRIMARY KEY);\n" +
		"DELIMITER ;;\n" +
		"CREATE TRIGGER trg BEFORE INSERT ON users FOR EACH ROW BEGIN\n" +
		"  SET NEW.id = 1;\n" +
		"  SET NEW.id = NEW.id + 1;\n" +
		"END ;;\n" +
		"DELIMITER ;\n" +
		"CREATE TABLE orders (id INT PRIMARY KEY);\n"

	got := all(t, dump, Semicolon)
	require.Len(t, got, 3)

	assert.Equal(t, "CREATE TABLE users (id INT PRIMARY KEY)", got[0])
	assert.Contains(t, got[1], "CREATE TRIGGER trg")
	assert.Contains(t, got[1], "SET NEW.id = NEW.id + 1;", "the whole body has to survive")
	assert.Contains(t, got[1], "END")
	assert.Equal(t, "CREATE TABLE orders (id INT PRIMARY KEY)", got[2])
}

// TestPostgresDollarQuoting covers the other way a routine body says "the
// delimiter does not apply in here".
func TestPostgresDollarQuoting(t *testing.T) {
	const dump = `CREATE TABLE users (id int PRIMARY KEY);

CREATE FUNCTION bump(n integer) RETURNS integer AS $$
BEGIN
  n := n + 1;
  RETURN n;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE orders (id int PRIMARY KEY);`

	got := all(t, dump, Semicolon)
	require.Len(t, got, 3)

	assert.Equal(t, "CREATE TABLE users (id int PRIMARY KEY)", got[0])
	assert.Contains(t, got[1], "CREATE FUNCTION bump")
	assert.Contains(t, got[1], "RETURN n;", "the whole body has to survive")
	assert.Contains(t, got[1], "LANGUAGE plpgsql")
	assert.Equal(t, "CREATE TABLE orders (id int PRIMARY KEY)", got[2])
}

func TestPostgresTaggedDollarQuoting(t *testing.T) {
	const dump = `CREATE FUNCTION f() RETURNS void AS $body$
BEGIN
  PERFORM 'a $$ b';
END;
$body$ LANGUAGE plpgsql;
SELECT 1;`

	got := all(t, dump, Semicolon)
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "$body$")
	assert.Contains(t, got[0], "PERFORM 'a $$ b';", "an inner $$ is not the closing tag")
	assert.Equal(t, "SELECT 1", got[1])
}

func TestDollarThatIsNotAQuote(t *testing.T) {
	// A dollar can appear in an identifier, where it opens nothing.
	got := all(t, "SELECT a$b FROM t; SELECT 1;", Semicolon)
	assert.Equal(t, []string{"SELECT a$b FROM t", "SELECT 1"}, got)
}

func TestBatchMode(t *testing.T) {
	const script = `CREATE TABLE a (id INT)
GO
CREATE VIEW v AS SELECT id FROM a
GO`

	got := all(t, script, Batch)
	require.Len(t, got, 2)
	assert.Equal(t, "CREATE TABLE a (id INT)", got[0])
	assert.Equal(t, "CREATE VIEW v AS SELECT id FROM a", got[1])
}

func TestBatchModeKeepsSemicolonsInsideAStatement(t *testing.T) {
	const script = `CREATE PROCEDURE p AS BEGIN SELECT 1; SELECT 2; END
GO
SELECT 3
GO`

	got := all(t, script, Batch)
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "SELECT 1; SELECT 2;", "GO is the only terminator here")
	assert.Equal(t, "SELECT 3", got[1])
}

func TestBatchModeIgnoresGoInsideAWord(t *testing.T) {
	// GO only terminates when it stands alone on its line.
	got := all(t, "SELECT category FROM goods\nGO", Batch)
	require.Len(t, got, 1)
	assert.Equal(t, "SELECT category FROM goods", got[0])
}

func TestPLSQLMode(t *testing.T) {
	const script = `CREATE TABLE t (id NUMBER);
CREATE OR REPLACE TRIGGER trg BEFORE INSERT ON t FOR EACH ROW
BEGIN
  :NEW.id := 1;
  :NEW.id := :NEW.id + 1;
END;
/
CREATE TABLE u (id NUMBER);`

	got := all(t, script, PLSQL)
	require.Len(t, got, 3)

	assert.Equal(t, "CREATE TABLE t (id NUMBER)", got[0])
	assert.Contains(t, got[1], "CREATE OR REPLACE TRIGGER trg")
	assert.Contains(t, got[1], ":NEW.id := :NEW.id + 1;",
		"inside a PL/SQL block only a slash terminates")
	assert.Equal(t, "CREATE TABLE u (id NUMBER)", got[2])
}

func TestPLSQLModeSplitsPlainStatementsOnSemicolon(t *testing.T) {
	// A DBMS_METADATA dump has no slashes at all: every statement ends with a
	// semicolon, so those still have to split.
	const script = `CREATE TABLE a (id NUMBER);
CREATE TABLE b (id NUMBER);
CREATE INDEX i ON b (id);`

	got := all(t, script, PLSQL)
	assert.Len(t, got, 3)
}

func TestModeFor(t *testing.T) {
	assert.Equal(t, Semicolon, ModeFor(";"))
	assert.Equal(t, Batch, ModeFor("GO"))
	assert.Equal(t, Batch, ModeFor("go"))
	assert.Equal(t, PLSQL, ModeFor("/"))
	assert.Equal(t, Semicolon, ModeFor("anything else"))
}

func TestUnterminatedConstructsDoNotSwallowTheInput(t *testing.T) {
	// Input that ends mid-string still comes back rather than vanishing, so a
	// truncated dump produces a parse error rather than silence.
	for _, src := range []string{
		"SELECT 'unterminated",
		"SELECT `unterminated",
		"CREATE FUNCTION f() AS $$ unterminated",
		"SELECT 1 /* unterminated",
	} {
		t.Run(src, func(t *testing.T) {
			got := all(t, src, Semicolon)
			require.Len(t, got, 1)
			assert.NotEmpty(t, got[0], "the text is returned, not dropped")
		})
	}
}

func TestEmptyInput(t *testing.T) {
	assert.Empty(t, all(t, "", Semicolon))
	assert.Empty(t, all(t, "   \n\n  ", Semicolon))
	assert.Empty(t, all(t, ";;;", Semicolon))
}

func TestNeverPanics(t *testing.T) {
	inputs := []string{
		"", ";", "$", "$$", "$$$", "'", "`", "[", "--", "/*", "GO", "/",
		"DELIMITER", "DELIMITER ;;", "$tag$", "a$b$c",
	}
	for _, src := range inputs {
		for _, mode := range []Mode{Semicolon, Batch, PLSQL} {
			t.Run(src, func(t *testing.T) {
				assert.NotPanics(t, func() {
					s := New(strings.NewReader(src), delimiterFor(mode))
					for {
						if _, err := s.Next(); err != nil {
							return
						}
					}
				})
			})
		}
	}
}

// TestSQLiteTriggerBody covers the dialect with no delimiter mechanism at all.
// SQLite marks the end of a trigger body with END, and splitting on the first
// inner semicolon left the trigger registered with an empty body.
func TestSQLiteTriggerBody(t *testing.T) {
	const dump = `CREATE TABLE users (id INTEGER PRIMARY KEY, u TEXT);
CREATE TRIGGER touch AFTER UPDATE ON users BEGIN
  UPDATE users SET u = 1;
  UPDATE users SET u = 2;
END;
CREATE TABLE orders (id INTEGER PRIMARY KEY);`

	got := all(t, dump, Semicolon)
	require.Len(t, got, 3)

	assert.Equal(t, "CREATE TABLE users (id INTEGER PRIMARY KEY, u TEXT)", got[0])
	assert.Contains(t, got[1], "CREATE TRIGGER touch")
	assert.Contains(t, got[1], "UPDATE users SET u = 2;", "the whole body has to survive")
	assert.Contains(t, got[1], "END")
	assert.Equal(t, "CREATE TABLE orders (id INTEGER PRIMARY KEY)", got[2])
}

func TestOrdinaryStatementsAreUnaffectedByTheRoutineRule(t *testing.T) {
	// Only a routine start turns the rule on; everything else still ends at the
	// first semicolon.
	got := all(t, "CREATE TABLE a (id INT); CREATE TABLE b (id INT); SELECT 1;", Semicolon)
	assert.Len(t, got, 3)
}

// A PostgreSQL trigger has no block body of its own: it names the function to
// run. Treating every CREATE TRIGGER as a block made such a statement wait for
// an END that never came, swallowing the rest of the file.
func TestTriggerWithoutABlockBody(t *testing.T) {
	const dump = `CREATE TRIGGER trg BEFORE INSERT ON users FOR EACH ROW EXECUTE FUNCTION set_created_at();
GRANT SELECT ON TABLE users TO reporting;`

	got := all(t, dump, Semicolon)
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "EXECUTE FUNCTION set_created_at()")
	assert.Contains(t, got[1], "GRANT SELECT")
}

// BEGIN inside a string is not the start of a block.
func TestBeginInsideAStringIsNotABlock(t *testing.T) {
	const dump = `CREATE TRIGGER trg BEFORE INSERT ON audit FOR EACH ROW WHEN (NEW.phase = 'BEGIN') EXECUTE FUNCTION f();
CREATE TABLE t (id INT);`

	got := all(t, dump, Semicolon)
	require.Len(t, got, 2)
	assert.Contains(t, got[1], "CREATE TABLE t")
}

// A MySQL version block is not a comment. mysqldump wraps every routine in one,
// so a splitter that discards it discards the routine with it.
func TestMySQLVersionCommentIsSQL(t *testing.T) {
	const dump = "/*!40101 SET @saved = @@character_set_client */;\n" +
		"CREATE TABLE users (id INT);\n" +
		"/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `bump` " +
		"BEFORE INSERT ON `users` FOR EACH ROW BEGIN\n  SET NEW.n = 1;\nEND */;\n"

	got := all(t, dump, Semicolon)
	require.Len(t, got, 3)

	assert.Contains(t, got[0], "SET @saved", "the SET inside the block is a statement")
	assert.Equal(t, "CREATE TABLE users (id INT)", got[1])

	// The wrapper is gone and the trigger is whole.
	assert.Contains(t, got[2], "TRIGGER `bump`")
	assert.Contains(t, got[2], "DEFINER=`root`@`localhost`")
	assert.Contains(t, got[2], "SET NEW.n = 1;", "the body survives the inner semicolon")
	assert.NotContains(t, got[2], "/*!")
	assert.NotContains(t, got[2], "*/")
}

// An ordinary block comment is still a comment.
func TestOrdinaryBlockCommentIsStillDropped(t *testing.T) {
	got := all(t, "/* dropped */ CREATE TABLE users (id INT);", Semicolon)
	require.Len(t, got, 1)
	assert.NotContains(t, got[0], "dropped")
	assert.Contains(t, got[0], "CREATE TABLE users")
}

// TestRoutineStartIsReadFromTheHead pins the bound the routine test reads.
//
// The pattern is anchored at the start, so only the head of a statement can
// change the answer, and reading the whole buffer instead rescanned every
// buffered statement on every terminator. The bound has to be generous enough
// for the longest real header, which is a MySQL routine carrying DEFINER,
// ALGORITHM and SQL SECURITY before the object keyword.
func TestRoutineStartIsReadFromTheHead(t *testing.T) {
	long := "CREATE DEFINER=`" + strings.Repeat("u", 120) + "`@`" +
		strings.Repeat("h", 120) + "` SQL SECURITY DEFINER PROCEDURE p() " +
		"BEGIN SELECT 1; SELECT 2; END"

	got := all(t, long+";\nCREATE TABLE t (id INT);", Semicolon)
	if len(got) != 2 {
		t.Fatalf("a long routine header was cut at an inner semicolon: %d statements\n%q", len(got), got)
	}
	if !strings.Contains(got[0], "SELECT 2") {
		t.Errorf("the body was truncated: %q", got[0])
	}

	// The bound has to clear the longest header by a wide margin, since a
	// statement past it stops looking like a routine and its body would be cut
	// at the first inner semicolon.
	if len(long)-len("BEGIN SELECT 1; SELECT 2; END") >= routineStartBound {
		t.Errorf("the longest real header is %d bytes and the bound is %d",
			len(long), routineStartBound)
	}
}

// TestStartsRoutineAgreesWithTheGrammar holds the scanner to the pattern.
//
// routineStart is the written grammar and startsRoutine is what runs: asking a
// backtracking regex once per statement was a quarter of the time it took to
// parse a large dump. Two implementations of one rule drift unless something
// compares them, so this does.
func TestStartsRoutineAgreesWithTheGrammar(t *testing.T) {
	heads := []string{
		// routines, in the shapes the dump tools write
		"CREATE FUNCTION f() RETURNS int AS $$",
		"CREATE OR REPLACE FUNCTION f() RETURNS int",
		"create procedure p()",
		"CREATE PROC p",
		"CREATE TRIGGER trg BEFORE INSERT ON t",
		"CREATE OR ALTER TRIGGER trg ON t",
		"CREATE DEFINER=`root`@`localhost` TRIGGER `bump` BEFORE INSERT ON `users`",
		"CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER TRIGGER t",
		"CREATE OR REPLACE EDITIONABLE FUNCTION \"APP\".\"F\" RETURN NUMBER IS",
		"CREATE OR REPLACE NONEDITIONABLE PROCEDURE p IS",
		"CREATE PACKAGE pkg AS",
		"CREATE TYPE BODY addr_t AS",
		"DECLARE x int;",
		"BEGIN",
		"  begin",

		// not routines
		"CREATE TABLE t (a int)",
		"CREATE OR REPLACE VIEW v AS SELECT 1",
		"CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`localhost` SQL SECURITY DEFINER VIEW `v` AS select 1",
		"CREATE UNIQUE INDEX ix ON t (a)",
		"CREATE SEQUENCE s",
		"CREATE TYPE addr_t AS OBJECT (street VARCHAR2(100))",
		"CREATE OR REPLACE TYPE addr_t AS OBJECT (a int)",
		"ALTER TABLE t ADD COLUMN a int",
		"INSERT INTO t VALUES (1)",
		"SET @x = 1",
		"GRANT SELECT ON t TO r",
		"",
		"   ",
		"CREATE",
		"-- CREATE FUNCTION f()",
	}

	for _, head := range heads {
		want := routineStart.MatchString(head)
		if got := startsRoutine(head); got != want {
			t.Errorf("startsRoutine(%q) = %v, the grammar says %v", head, got, want)
		}
	}
}

// TestStartsRoutineAgreesOnTheFixtures runs the same comparison over every
// statement of the comprehensive fixtures, which are real dump output.
func TestStartsRoutineAgreesOnTheFixtures(t *testing.T) {
	for _, file := range []string{"postgres.sql", "mysql.sql", "oracle.sql", "sqlserver.sql", "sqlite.sql"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "tests", "integration", "testdata", "schemas", file))
		if err != nil {
			t.Skipf("fixture unavailable: %v", err)
		}
		// Every prefix boundary is checked, not only whole statements: the
		// splitter asks the question against whatever it has buffered so far.
		text := string(b)
		for i := 0; i < len(text); i += 97 {
			head := text[:i]
			if len(head) > routineStartBound {
				head = head[:routineStartBound]
			}
			want := routineStart.MatchString(head)
			if got := startsRoutine(head); got != want {
				t.Fatalf("%s at %d: startsRoutine = %v, the grammar says %v\nhead: %q",
					file, i, got, want, head)
			}
		}
	}
}
