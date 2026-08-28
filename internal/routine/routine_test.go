package routine

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
)

func schemaWith() *sqlmapper.Schema {
	return &sqlmapper.Schema{
		SourceDialect: sqlmapper.MySQL,
		Functions: []sqlmapper.Function{
			{Name: "add_one", Returns: "INT", Body: "BEGIN RETURN 1; END",
				Parameters: []sqlmapper.Parameter{{Name: "v", DataType: "INT"}}},
			{Name: "touch", IsProc: true, Body: "BEGIN SELECT 1; END"},
		},
		Procedures: []sqlmapper.Procedure{
			{Name: "purge", Body: "BEGIN DELETE FROM t; END",
				Parameters: []sqlmapper.Parameter{{Name: "days", DataType: "INT", Direction: "IN"}}},
		},
		Triggers: []sqlmapper.Trigger{
			{Name: "bump", Timing: "BEFORE", Event: "INSERT", Table: "users",
				ForEachRow: true, Body: "SET NEW.n = 1;"},
		},
	}
}

func TestCount(t *testing.T) {
	if got := Count(schemaWith()); got != 4 {
		t.Errorf("Count() = %d, want 4", got)
	}
	if got := Count(&sqlmapper.Schema{}); got != 0 {
		t.Errorf("Count() on an empty schema = %d, want 0", got)
	}
}

func TestForeignSQL(t *testing.T) {
	got := ForeignSQL(schemaWith())

	for _, want := range []string{
		"Defined by the mysql source",
		"CREATE FUNCTION add_one(v INT) RETURNS INT",
		"CREATE PROCEDURE touch()",
		"CREATE PROCEDURE purge(IN days INT)",
		"CREATE TRIGGER bump BEFORE INSERT ON users",
		"FOR EACH ROW",
	} {
		if !strings.Contains(got, "-- "+want) && !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// Nothing may reach the target as executable SQL.
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "--") {
			t.Errorf("uncommented line %q in:\n%s", line, got)
		}
	}
}

// A trigger body means different things in different databases, and the
// reconstruction has to say what the source meant.
func TestForeignSQLRendersTheSourcesTriggerShape(t *testing.T) {
	// MySQL keeps procedural code, with the block keywords stripped.
	my := ForeignSQL(&sqlmapper.Schema{
		SourceDialect: sqlmapper.MySQL,
		Triggers:      []sqlmapper.Trigger{{Name: "t", Table: "users", Body: "SET NEW.n = 1;"}},
	})
	if !strings.Contains(my, "-- BEGIN") || !strings.Contains(my, "-- END") {
		t.Errorf("a MySQL body needs its block keywords back:\n%s", my)
	}

	// PostgreSQL keeps the name of the function the trigger runs.
	pg := ForeignSQL(&sqlmapper.Schema{
		SourceDialect: sqlmapper.PostgreSQL,
		Triggers:      []sqlmapper.Trigger{{Name: "t", Table: "users", Body: "public.touch"}},
	})
	if !strings.Contains(pg, "EXECUTE FUNCTION public.touch()") {
		t.Errorf("a PostgreSQL trigger runs a function:\n%s", pg)
	}
	if strings.Contains(pg, "BEGIN") {
		t.Errorf("a PostgreSQL trigger has no block body:\n%s", pg)
	}
}

// A schema with no source dialect was built by the caller, not parsed.
func TestForeignSQLWithoutASourceDialect(t *testing.T) {
	got := ForeignSQL(&sqlmapper.Schema{
		Triggers: []sqlmapper.Trigger{{Name: "t", Table: "users", Body: "SET a = 1;"}},
	})
	if !strings.Contains(got, "Defined by the source") {
		t.Errorf("expected a neutral wording:\n%s", got)
	}
}

func TestUnsupportedSQL(t *testing.T) {
	got := UnsupportedSQL(schemaWith(), "SQLite has no stored functions.")

	if !strings.Contains(got, "SQLite has no stored functions.") {
		t.Errorf("missing the reason:\n%s", got)
	}
	if !strings.Contains(got, "add_one") || !strings.Contains(got, "purge") {
		t.Errorf("functions and procedures both belong here:\n%s", got)
	}
	if strings.Contains(got, "bump") {
		t.Errorf("a trigger is supported and does not belong here:\n%s", got)
	}
}

func TestParams(t *testing.T) {
	tests := []struct {
		in   []sqlmapper.Parameter
		want string
	}{
		{nil, ""},
		{[]sqlmapper.Parameter{{Name: "a", DataType: "INT"}}, "a INT"},
		{[]sqlmapper.Parameter{{Name: "a", DataType: "INT"}, {Name: "b", DataType: "TEXT"}}, "a INT, b TEXT"},
		{[]sqlmapper.Parameter{{Name: "a", DataType: "INT", Direction: "IN"}}, "IN a INT"},
	}

	for _, tt := range tests {
		if got := Params(tt.in); got != tt.want {
			t.Errorf("Params(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
