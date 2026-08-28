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
