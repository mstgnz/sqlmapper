package sqlfmt

import (
	"strings"
	"testing"
)

func TestTerminate(t *testing.T) {
	tests := []struct{ in, term, want string }{
		{"CREATE TABLE t (id INT)", ";", "CREATE TABLE t (id INT);"},
		{"CREATE TABLE t (id INT);", ";", "CREATE TABLE t (id INT);"},
		{"CREATE TABLE t (id INT);\n", ";", "CREATE TABLE t (id INT);"},
		{"CREATE TABLE t (id INT)  ", ";", "CREATE TABLE t (id INT);"},
		{"", ";", ""},
		{"   \n", ";", ""},
		{"BEGIN END", ";;", "BEGIN END;;"},
		{"BEGIN END;;", ";;", "BEGIN END;;"},
	}

	for _, tt := range tests {
		if got := Terminate(tt.in, tt.term); got != tt.want {
			t.Errorf("Terminate(%q, %q) = %q, want %q", tt.in, tt.term, got, tt.want)
		}
	}
}

func TestComment(t *testing.T) {
	got := Comment("CREATE TRIGGER t\n\nBEGIN\nEND\n")
	want := "-- CREATE TRIGGER t\n--\n-- BEGIN\n-- END"
	if got != want {
		t.Errorf("Comment() = %q, want %q", got, want)
	}
	if Comment("") != "--" {
		t.Errorf("Comment(\"\") = %q", Comment(""))
	}
}

func TestForeignRoutine(t *testing.T) {
	got := ForeignRoutine("mysql", "CREATE TRIGGER bump BEFORE INSERT ON users\nBEGIN\nEND")

	for _, want := range []string{
		"-- Defined by the mysql source.",
		"-- CREATE TRIGGER bump BEFORE INSERT ON users",
		"-- BEGIN",
		"-- END",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// Every line has to be commented, or the SQL after it is read as SQL.
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "--") {
			t.Errorf("uncommented line %q in:\n%s", line, got)
		}
	}
}

func TestTriggerHeader(t *testing.T) {
	got := TriggerHeader("bump", "BEFORE", "INSERT", "users", true)
	if got != "CREATE TRIGGER bump BEFORE INSERT ON users\nFOR EACH ROW" {
		t.Errorf("got %q", got)
	}

	// A dialect that records neither timing nor row scope still gets a header
	// that names the trigger and its table.
	got = TriggerHeader("t", "", "", "users", false)
	if got != "CREATE TRIGGER t ON users" {
		t.Errorf("got %q", got)
	}
}

func TestSourceRoutineSQL(t *testing.T) {
	if got := SourceRoutineSQL("CREATE TRIGGER t ON users", "  BEGIN END  "); got != "CREATE TRIGGER t ON users\nBEGIN END" {
		t.Errorf("got %q", got)
	}
	if got := SourceRoutineSQL("CREATE TRIGGER t ON users", "   "); got != "CREATE TRIGGER t ON users" {
		t.Errorf("got %q", got)
	}
}

func TestBlockBody(t *testing.T) {
	tests := map[string]string{
		// A parser that strips the keywords gets them back.
		"SET NEW.n = 1;": "BEGIN\nSET NEW.n = 1;\nEND",
		"  SET a = 1;  ": "BEGIN\nSET a = 1;\nEND",
		// A parser that keeps them is left alone.
		"BEGIN\nSET a = 1;\nEND": "BEGIN\nSET a = 1;\nEND",
		"begin SET a = 1; end":   "begin SET a = 1; end",
		// A word that merely starts with the keyword is not the keyword.
		"BEGINNING_OF_TIME := 1": "BEGIN\nBEGINNING_OF_TIME := 1\nEND",
		"":                       "BEGIN\nEND",
		"   ":                    "BEGIN\nEND",
	}

	for in, want := range tests {
		if got := BlockBody(in); got != want {
			t.Errorf("BlockBody(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnsupportedRoutine(t *testing.T) {
	got := UnsupportedRoutine("SQLite has no stored functions.", "CREATE FUNCTION f()\nBEGIN\nEND")

	if !strings.Contains(got, "-- SQLite has no stored functions.") {
		t.Errorf("missing the reason in:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "--") {
			t.Errorf("uncommented line %q in:\n%s", line, got)
		}
	}
}

func TestUnquoteLiteral(t *testing.T) {
	tests := map[string]string{
		"'active'":      "active",
		"'two words'":   "two words",
		"'it''s'":       "it's",
		"''":            "",
		"active":        "active",
		"5":             "5",
		"'unterminated": "'unterminated",
		"  'padded'  ":  "padded",
		"'":             "'",
	}

	for in, want := range tests {
		if got := UnquoteLiteral(in); got != want {
			t.Errorf("UnquoteLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommentOn(t *testing.T) {
	if got := CommentOn("TABLE", "users", "people who buy things"); got != "COMMENT ON TABLE users IS 'people who buy things';" {
		t.Errorf("got %q", got)
	}
	if got := CommentOn("COLUMN", "users.email", "it's unique"); got != "COMMENT ON COLUMN users.email IS 'it''s unique';" {
		t.Errorf("got %q", got)
	}
	if got := CommentOn("TABLE", "users", ""); got != "" {
		t.Errorf("no comment means no statement, got %q", got)
	}
}

func TestPartialIndexNote(t *testing.T) {
	// The note exists so a widened index is visible. An index with no filter has
	// nothing to say and must stay silent, or every ordinary index grows a
	// comment.
	if got := PartialIndexNote("ix", "", false); got != "" {
		t.Errorf("an unfiltered index says nothing, got %q", got)
	}

	plain := PartialIndexNote("ix_open", "status <> 'shipped'", false)
	if !strings.Contains(plain, "-- index ix_open was partial in the source") ||
		!strings.Contains(plain, "WHERE status <> 'shipped'") {
		t.Errorf("note = %q", plain)
	}

	// The unique case is the one that matters: widening it makes the index
	// stricter than the source, so the note has to say which kind it was.
	unique := PartialIndexNote("uq_open", "is_active", true)
	if !strings.Contains(unique, "-- unique index uq_open") {
		t.Errorf("note = %q", unique)
	}
	if !strings.HasSuffix(plain, "\n") {
		t.Error("the note ends with a newline so the index starts on its own line")
	}
}

func TestForeignType(t *testing.T) {
	got := ForeignType("oracle", "addr_t", "COMPOSITE", `OBJECT ("STREET" VARCHAR2(100))`)

	// Every line is commented out: the point is that it must not execute.
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "--") {
			t.Errorf("this line would run: %q", line)
		}
	}
	if !strings.Contains(got, "Defined by the oracle source") {
		t.Errorf("the note drops its provenance: %q", got)
	}
	// The statement is shown as the source wrote it, not as the target would
	// have mangled it.
	if !strings.Contains(got, `CREATE TYPE addr_t AS OBJECT ("STREET" VARCHAR2(100));`) {
		t.Errorf("the note lost the source statement: %q", got)
	}

	// A schema built by hand carries no source dialect, and the note still has
	// to name something.
	if !strings.Contains(ForeignType("", "t", "", "x"), "Defined by the source") {
		t.Error("an unknown provenance still reads as a sentence")
	}
}

func TestSplitTopLevelCommasBytes(t *testing.T) {
	// The byte form exists for the parsers that work on []byte, and has to agree
	// with the string one: a comma inside NUMERIC(10,2) is not a separator.
	in := `id INT, amount NUMERIC(10,2), note VARCHAR(20) DEFAULT 'a,b'`
	got := SplitTopLevelCommasBytes([]byte(in))
	want := SplitTopLevelCommas(in)

	if len(got) != len(want) {
		t.Fatalf("the two forms disagree: %d vs %d parts", len(got), len(want))
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Errorf("part %d: %q vs %q", i, got[i], want[i])
		}
	}
	if len(want) != 3 {
		t.Fatalf("want three columns, got %d: %q", len(want), want)
	}
}

// TestForeignTypeUsesTheSourcesKeyword checks the note reads as the source
// wrote it. SQL Server's alias is declared with FROM, and showing it with AS
// states a statement nobody wrote.
func TestForeignTypeUsesTheSourcesKeyword(t *testing.T) {
	alias := ForeignType("sqlserver", "phone", "ALIAS", "VARCHAR(20) NOT NULL")
	if !strings.Contains(alias, "CREATE TYPE phone FROM VARCHAR(20) NOT NULL;") {
		t.Errorf("an alias is declared with FROM: %q", alias)
	}

	shape := ForeignType("oracle", "addr_t", "COMPOSITE", "OBJECT (street VARCHAR2(100))")
	if !strings.Contains(shape, "CREATE TYPE addr_t AS OBJECT") {
		t.Errorf("a shape is declared with AS: %q", shape)
	}
}
