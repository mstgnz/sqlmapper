package expr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// generic renders an expression the way it was structured, without any dialect
// rules applied, which makes the tree's shape visible in a test.
func generic(t *testing.T, src string) string {
	t.Helper()
	e, err := Parse(src)
	require.NoError(t, err, "parsing %q", src)
	return SQL(e, Generic)
}

func TestParsePrecedence(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"and binds tighter than or", "a = 1 OR b = 2 AND c = 3", "a = 1 OR b = 2 AND c = 3"},
		{"explicit grouping survives", "(a = 1 OR b = 2) AND c = 3", "(a = 1 OR b = 2) AND c = 3"},
		{"comparison binds tighter than and", "a + 1 = 2 AND b = 3", "a + 1 = 2 AND b = 3"},
		{"multiplication binds tighter than addition", "a + b * c", "a + b * c"},
		{"grouping around multiplication", "(a + b) * c", "(a + b) * c"},
		{"left associative subtraction", "a - b - c", "a - b - c"},
		{"right grouping is preserved", "a - (b - c)", "a - (b - c)"},
		{"not applies to the comparison", "NOT a = 1", "NOT a = 1"},
		{"unary minus", "-a + b", "-a + b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, generic(t, tt.src))
		})
	}
}

func TestParseDropsRedundantParentheses(t *testing.T) {
	// Dump tools wrap an expression in one or two more layers than it needs.
	// The tree has no node for a parenthesis, so they simply do not survive.
	tests := []struct {
		src  string
		want string
	}{
		{"(amount >= 0)", "amount >= 0"},
		{"((amount >= 0))", "amount >= 0"},
		{"(((amount)))", "amount"},
		{"([amount]>=(0))", "amount >= 0"},
		{"((amount >= (0)))", "amount >= 0"},
	}

	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			assert.Equal(t, tt.want, generic(t, tt.src))
		})
	}
}

func TestParseIdentifiers(t *testing.T) {
	e, err := Parse("public.customers.id")
	require.NoError(t, err)

	id, ok := e.(*Ident)
	require.True(t, ok, "got %T", e)
	assert.Equal(t, []string{"public", "customers"}, id.Qualifier)
	assert.Equal(t, "id", id.Name)
	assert.Equal(t, "public.customers.id", id.FullName())
}

func TestParseCalls(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		wantName string
		wantArgs int
	}{
		{"no arguments", "now()", "now", 0},
		{"one argument", "json_valid(meta)", "json_valid", 1},
		{"several arguments", "coalesce(a, b, 0)", "coalesce", 3},
		{"nested", "upper(trim(name))", "upper", 1},
		{"qualified", "app.add_one(n)", "app.add_one", 1},
		{"star argument", "count(*)", "count", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := Parse(tt.src)
			require.NoError(t, err)
			call, ok := e.(*Call)
			require.True(t, ok, "got %T", e)
			assert.Equal(t, tt.wantName, call.Name)
			assert.Len(t, call.Args, tt.wantArgs)
		})
	}
}

func TestParsePredicates(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"in list", "status IN ('a','b')", "status IN ('a', 'b')"},
		{"not in list", "status NOT IN ('a')", "status NOT IN ('a')"},
		{"is null", "meta IS NULL", "meta IS NULL"},
		{"is not null", "meta IS NOT NULL", "meta IS NOT NULL"},
		{"like", "email LIKE '%@%'", "email LIKE '%@%'"},
		{"not like", "email NOT LIKE '%@%'", "email NOT LIKE '%@%'"},
		{"between", "amount BETWEEN 1 AND 10", "amount BETWEEN 1 AND 10"},
		// The AND inside BETWEEN binds tighter than a logical AND.
		{"between inside and", "a BETWEEN 1 AND 10 AND b = 2", "a BETWEEN 1 AND 10 AND b = 2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, generic(t, tt.src))
		})
	}
}

func TestParseCasts(t *testing.T) {
	e, err := Parse("(0)::numeric")
	require.NoError(t, err)
	cast, ok := e.(*Cast)
	require.True(t, ok, "got %T", e)
	assert.Equal(t, "numeric", cast.Type)

	// CAST(x AS t) is the same node, so the renderer can pick either spelling.
	e2, err := Parse("CAST(amount AS numeric)")
	require.NoError(t, err)
	cast2, ok := e2.(*Cast)
	require.True(t, ok, "got %T", e2)
	assert.Equal(t, "numeric", cast2.Type)

	// Multi-word types and precisions are part of the type, not a call.
	for _, src := range []string{
		"x::character varying",
		"x::numeric(10,2)",
		"x::text[]",
	} {
		t.Run(src, func(t *testing.T) {
			e, err := Parse(src)
			require.NoError(t, err)
			_, ok := e.(*Cast)
			assert.True(t, ok, "got %T", e)
		})
	}
}

func TestParseLiterals(t *testing.T) {
	tests := []struct {
		src      string
		wantKind LiteralKind
		want     string
	}{
		{"42", NumberLit, "42"},
		{"0.00", NumberLit, "0.00"},
		{"'active'", StringLit, "'active'"},
		{"NULL", NullLit, "NULL"},
		{"TRUE", BoolLit, "TRUE"},
		{"false", BoolLit, "FALSE"},
	}

	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			e, err := Parse(tt.src)
			require.NoError(t, err)
			lit, ok := e.(*Literal)
			require.True(t, ok, "got %T", e)
			assert.Equal(t, tt.wantKind, lit.Kind)
			assert.Equal(t, tt.want, SQL(e, Generic))
		})
	}
}

// TestRoundTrip is the property that keeps the parser and the renderer honest:
// rendering a tree and parsing the result must produce the same text again.
// Anything the parser drops or the renderer invents shows up as a mismatch on
// the second pass.
func TestRoundTrip(t *testing.T) {
	sources := []string{
		"amount >= 0",
		"((amount >= (0)::numeric))",
		"([amount]>=(0))",
		"json_valid(`meta`)",
		"nextval('public.customers_id_seq'::regclass)",
		"status IN ('a','b')",
		"a = 1 AND b = 2 OR c = 3",
		"(a = 1 OR b = 2) AND NOT c = 3",
		"a - (b - c)",
		"upper(trim(name)) LIKE '%X%'",
		"amount BETWEEN 1 AND 10",
		"meta IS NOT NULL",
		"now()",
		"is_active",
		"-1",
		"'it''s'",
	}

	for _, d := range []Dialect{Generic, MySQL, PostgreSQL, SQLite, Oracle, SQLServer} {
		for _, src := range sources {
			t.Run(d.String()+"/"+src, func(t *testing.T) {
				e, err := Parse(src)
				require.NoError(t, err)

				once := SQL(e, d)
				reparsed, err := Parse(once)
				require.NoError(t, err, "rendered %q is not parseable", once)

				assert.Equal(t, once, SQL(reparsed, d),
					"rendering is not stable: %q then %q", once, SQL(reparsed, d))
			})
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{
		"",
		"(",
		"a =",
		"a = 1)",
		"a AND",
		"f(",
		"a IN",
		"a IS",
		"a BETWEEN 1",
	} {
		t.Run(src, func(t *testing.T) {
			_, err := Parse(src)
			assert.Error(t, err)
		})
	}
}

// TestParseNeverPanics is a cheap stand-in for a fuzz target: malformed input
// must come back as an error, never as a panic.
func TestParseNeverPanics(t *testing.T) {
	inputs := []string{
		"", " ", "(((", ")))", "''", "``", "[[", "::", "..", ",,",
		"a..b", "a.", ".a", "NOT", "AND", "1 1", "f(,)", "x::",
		"'unterminated", "a = = b", "CAST(x)", "CAST(x AS)",
	}
	for _, src := range inputs {
		t.Run(src, func(t *testing.T) {
			assert.NotPanics(t, func() { _, _ = Parse(src) })
		})
	}
}

func TestWalkVisitsEveryNode(t *testing.T) {
	e, err := Parse("a = 1 AND upper(b) IN ('X', 'Y')")
	require.NoError(t, err)

	var idents []string
	Walk(e, func(n Expr) bool {
		if id, ok := n.(*Ident); ok {
			idents = append(idents, id.Name)
		}
		return true
	})
	assert.Equal(t, []string{"a", "b"}, idents)
}
