package expr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func translate(t *testing.T, src string, to Dialect, opts Options) string {
	t.Helper()
	out, err := Translate(src, Generic, to, opts)
	require.NoError(t, err, "translating %q", src)
	return out
}

// TestRealCheckExpressions runs the CHECK constraints that the version matrix
// actually produced through every target. Each source is the verbatim text a
// dump tool wrote.
func TestRealCheckExpressions(t *testing.T) {
	cond := Options{Condition: true}

	tests := []struct {
		name string
		src  string
		want map[Dialect]string
	}{
		{
			name: "sql server brackets and doubled parentheses",
			src:  "([amount]>=(0))",
			want: map[Dialect]string{
				PostgreSQL: "amount >= 0",
				MySQL:      "amount >= 0",
				Oracle:     "amount >= 0",
				SQLServer:  "amount >= 0",
			},
		},
		{
			name: "postgres cast",
			src:  "((amount >= (0)::numeric))",
			want: map[Dialect]string{
				// The cast is PostgreSQL's own notation. Everywhere else the
				// column already has the type the target declared for it.
				PostgreSQL: "amount >= 0::numeric",
				MySQL:      "amount >= 0",
				Oracle:     "amount >= 0",
				SQLServer:  "amount >= 0",
			},
		},
		{
			name: "oracle plain",
			src:  "amount >= 0",
			want: map[Dialect]string{
				PostgreSQL: "amount >= 0",
				MySQL:      "amount >= 0",
				Oracle:     "amount >= 0",
				SQLServer:  "amount >= 0",
			},
		},
		{
			name: "sqlite value list",
			src:  "status IN ('a','b')",
			want: map[Dialect]string{
				PostgreSQL: "status IN ('a', 'b')",
				MySQL:      "status IN ('a', 'b')",
				Oracle:     "status IN ('a', 'b')",
				SQLServer:  "status IN ('a', 'b')",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for d, want := range tt.want {
				t.Run(d.String(), func(t *testing.T) {
					assert.Equal(t, want, translate(t, tt.src, d, cond))
				})
			}
		})
	}
}

// TestBareBooleanCondition covers the single failure the version matrix was left
// with: a PostgreSQL view body says WHERE is_active, and Oracle 21 and SQL Server
// reject it because they have no boolean type.
func TestBareBooleanCondition(t *testing.T) {
	cond := Options{Condition: true}

	tests := []struct {
		dialect Dialect
		want    string
	}{
		{PostgreSQL, "is_active"},
		{MySQL, "is_active"},
		{SQLite, "is_active"},
		{Oracle, "is_active <> 0"},
		{SQLServer, "is_active <> 0"},
	}

	for _, tt := range tests {
		t.Run(tt.dialect.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, translate(t, "is_active", tt.dialect, cond))
		})
	}
}

func TestBareBooleanOnlyInConditionPosition(t *testing.T) {
	// The same identifier outside a condition is a value, not a predicate, and
	// must be left alone.
	assert.Equal(t, "is_active", translate(t, "is_active", Oracle, Options{}))

	// Inside a logical operator every operand is a condition.
	assert.Equal(t,
		"is_active <> 0 AND is_deleted <> 0",
		translate(t, "is_active AND is_deleted", Oracle, Options{Condition: true}))

	// An operand of a comparison is a value, so it is not rewritten.
	assert.Equal(t,
		"is_active = 1",
		translate(t, "is_active = 1", Oracle, Options{Condition: true}))

	// NOT takes a condition.
	assert.Equal(t,
		"NOT is_active <> 0",
		translate(t, "NOT is_active", Oracle, Options{Condition: true}))
}

func TestBooleanLiterals(t *testing.T) {
	tests := []struct {
		dialect Dialect
		want    string
	}{
		{PostgreSQL, "TRUE"},
		{MySQL, "TRUE"},
		{SQLite, "TRUE"},
		{Oracle, "1"},
		{SQLServer, "1"},
	}

	for _, tt := range tests {
		t.Run(tt.dialect.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, translate(t, "TRUE", tt.dialect, Options{}))
		})
	}
}

func TestTimestampFunctionPerDialect(t *testing.T) {
	// Every dialect spells "the current timestamp" differently, and each spells
	// it for all the others when it writes a dump.
	sources := []string{"now()", "CURRENT_TIMESTAMP", "getdate()", "sysutcdatetime()", "SYSTIMESTAMP", "sysdate()"}

	want := map[Dialect]string{
		PostgreSQL: "now()",
		MySQL:      "CURRENT_TIMESTAMP",
		SQLite:     "CURRENT_TIMESTAMP",
		Oracle:     "SYSTIMESTAMP",
		SQLServer:  "SYSUTCDATETIME()",
	}

	for _, src := range sources {
		for d, expected := range want {
			t.Run(src+"/"+d.String(), func(t *testing.T) {
				assert.Equal(t, expected, translate(t, src, d, Options{}))
			})
		}
	}
}

func TestForeignDefaultSchemaIsDropped(t *testing.T) {
	// A qualifier naming another dialect's default schema does not resolve at
	// the target, and one naming the target's own carries no information.
	for _, d := range []Dialect{PostgreSQL, MySQL, Oracle, SQLServer} {
		t.Run(d.String(), func(t *testing.T) {
			assert.Equal(t, "customers.id", translate(t, "public.customers.id", d, Options{}))
			assert.Equal(t, "customers.id", translate(t, "dbo.customers.id", d, Options{}))
		})
	}

	// A qualifier that is not a default schema is a real name and stays.
	assert.Equal(t, "app.customers.id", translate(t, "app.customers.id", Oracle, Options{}))
	// An alias in a view body must survive too.
	assert.Equal(t, "u.id", translate(t, "u.id", PostgreSQL, Options{}))
}

func TestIdentifierQuoting(t *testing.T) {
	opts := Options{Quote: true}

	tests := []struct {
		dialect Dialect
		want    string
	}{
		{PostgreSQL, `"amount"`},
		{MySQL, "`amount`"},
		{SQLServer, "[amount]"},
		{Oracle, `"amount"`},
	}

	for _, tt := range tests {
		t.Run(tt.dialect.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, translate(t, "amount", tt.dialect, opts))
		})
	}

	// Quoting is off by default, because a dump is read by people too.
	assert.Equal(t, "amount", translate(t, "amount", MySQL, Options{}))
}

func TestIsJSONGuard(t *testing.T) {
	// MariaDB has no JSON type: it stores JSON in a LONGTEXT and polices it with
	// this check. No other dialect has the function.
	e, err := Parse("json_valid(`meta`)")
	require.NoError(t, err)
	assert.True(t, IsJSONGuard(e))

	e2, err := Parse("JSON_VALID(meta)")
	require.NoError(t, err)
	assert.True(t, IsJSONGuard(e2))

	e3, err := Parse("amount >= 0")
	require.NoError(t, err)
	assert.False(t, IsJSONGuard(e3))

	// A column whose name merely starts the same way is not the guard.
	e4, err := Parse("json_validated = 1")
	require.NoError(t, err)
	assert.False(t, IsJSONGuard(e4))
}

func TestPrecedenceIsPreservedAcrossDialects(t *testing.T) {
	// Rewriting a bare boolean inserts a comparison into the tree, which must
	// not change how the rest of the expression groups.
	assert.Equal(t,
		"(is_active <> 0 OR is_admin <> 0) AND amount >= 0",
		translate(t, "(is_active OR is_admin) AND amount >= 0", Oracle, Options{Condition: true}))

	assert.Equal(t,
		"is_active <> 0 OR is_admin <> 0 AND amount >= 0",
		translate(t, "is_active OR is_admin AND amount >= 0", Oracle, Options{Condition: true}))
}

func TestTranslateReportsParseErrors(t *testing.T) {
	_, err := Translate("a = = b", Generic, PostgreSQL, Options{})
	assert.Error(t, err)
}

func TestDialectOf(t *testing.T) {
	tests := map[string]Dialect{
		"mysql": MySQL, "mariadb": MySQL,
		"postgres": PostgreSQL, "postgresql": PostgreSQL, "pgsql": PostgreSQL,
		"sqlite": SQLite, "sqlite3": SQLite,
		"oracle":    Oracle,
		"sqlserver": SQLServer, "mssql": SQLServer,
		"  MySQL  ": MySQL,
		"db2":       Generic,
		"":          Generic,
	}

	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, want, DialectOf(name))
		})
	}
}

func TestConditionFallsBackOnUnparseableInput(t *testing.T) {
	// The layer must never make a conversion worse than it was. Anything it
	// cannot read comes back exactly as it arrived.
	weird := "a @> b[1:2]"
	assert.Equal(t, weird, Condition(weird, Oracle))
	assert.Equal(t, weird, Value(weird, Oracle))
	assert.False(t, IsJSONGuardSQL(weird))
}

func TestConditionAndValueDifferOnBareBoolean(t *testing.T) {
	// The same text means different things in the two positions.
	assert.Equal(t, "is_active <> 0", Condition("is_active", Oracle))
	assert.Equal(t, "is_active", Value("is_active", Oracle))
}
