package sqlfmt

import "testing"

func TestTakeGenerated(t *testing.T) {
	cases := []struct {
		def    string
		rest   string
		expr   string
		stored bool
		ok     bool
	}{
		{"b int GENERATED ALWAYS AS (a * 2) STORED", "b int", "a * 2", true, true},
		{"b int AS (a * 2) VIRTUAL", "b int", "a * 2", false, true},
		// SQL Server states no type at all, so the definition is the name alone.
		{"b AS (a * 2)", "b", "a * 2", false, true},
		{"b AS (a * 2) PERSISTED", "b", "a * 2", true, true},
		{"b NUMBER GENERATED ALWAYS AS (round(a, 2)) VIRTUAL", "b NUMBER", "round(a, 2)", false, true},
		// What follows the clause stays on the definition.
		{"total numeric(12,2) GENERATED ALWAYS AS (qty * price) STORED NOT NULL",
			"total numeric(12,2) NOT NULL", "qty * price", true, true},
		// An ordinary column is left exactly as it was.
		{"b int NOT NULL", "b int NOT NULL", "", false, false},
		{"a int", "a int", "", false, false},
	}

	for _, c := range cases {
		rest, expr, stored, ok := TakeGenerated(c.def)
		if ok != c.ok {
			t.Errorf("%q: ok = %v, want %v", c.def, ok, c.ok)
			continue
		}
		if rest != c.rest {
			t.Errorf("%q: rest = %q, want %q", c.def, rest, c.rest)
		}
		if expr != c.expr {
			t.Errorf("%q: expression = %q, want %q", c.def, expr, c.expr)
		}
		if stored != c.stored {
			t.Errorf("%q: stored = %v, want %v", c.def, stored, c.stored)
		}
	}
}

// TestTakeGeneratedMatchesTheClosingParenthesis holds the part a regex alone
// gets wrong: the expression ends at the parenthesis closing the one the clause
// opened, not at the first one, and a call inside it has parentheses of its own.
func TestTakeGeneratedMatchesTheClosingParenthesis(t *testing.T) {
	_, expr, _, ok := TakeGenerated("b int GENERATED ALWAYS AS (coalesce(a, 0) * (c + 1)) STORED")
	if !ok {
		t.Fatal("not recognised")
	}
	if expr != "coalesce(a, 0) * (c + 1)" {
		t.Errorf("expression = %q", expr)
	}
}

// TestTakeGeneratedLeavesAStringAlone checks a default whose value happens to
// read like the clause is not mistaken for one.
func TestTakeGeneratedLeavesAStringAlone(t *testing.T) {
	def := "note varchar(20) DEFAULT 'as (x)'"
	rest, _, _, ok := TakeGenerated(def)
	if ok {
		t.Errorf("a string literal was read as a generated clause: rest=%q", rest)
	}
	if rest != def {
		t.Errorf("the definition was altered: %q", rest)
	}
}
