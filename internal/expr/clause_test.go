package expr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTranslateViewBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		dialect Dialect
		want    string
	}{
		{
			// This is the view the version matrix could not load into Oracle 21
			// or SQL Server: a bare boolean column used as a condition.
			name:    "bare boolean condition for a dialect without booleans",
			body:    "SELECT id, email FROM customers WHERE is_active",
			dialect: Oracle,
			want:    "SELECT id, email FROM customers WHERE is_active <> 0",
		},
		{
			name:    "same body needs no change for postgres",
			body:    "SELECT id, email FROM customers WHERE is_active",
			dialect: PostgreSQL,
			want:    "SELECT id, email FROM customers WHERE is_active",
		},
		{
			name:    "an existing comparison is left alone",
			body:    "SELECT id FROM customers WHERE is_active = 1",
			dialect: Oracle,
			want:    "SELECT id FROM customers WHERE is_active = 1",
		},
		{
			name:    "a cast in the clause is dropped for a target that has none",
			body:    "SELECT id FROM t WHERE amount >= (0)::numeric",
			dialect: MySQL,
			want:    "SELECT id FROM t WHERE amount >= 0",
		},
		{
			name:    "the clause ends at GROUP BY",
			body:    "SELECT a, count(*) FROM t WHERE is_active GROUP BY a",
			dialect: SQLServer,
			want:    "SELECT a, count(*) FROM t WHERE is_active <> 0 GROUP BY a",
		},
		{
			name:    "the clause ends at ORDER BY",
			body:    "SELECT a FROM t WHERE is_active ORDER BY a",
			dialect: Oracle,
			want:    "SELECT a FROM t WHERE is_active <> 0 ORDER BY a",
		},
		{
			name:    "no where clause",
			body:    "SELECT id, email FROM customers",
			dialect: Oracle,
			want:    "SELECT id, email FROM customers",
		},
		{
			// The scanner knows a string literal from syntax, which is the whole
			// reason this is not a regular expression.
			name:    "a WHERE inside a string is not the clause",
			body:    "SELECT 'WHERE is_active' AS note FROM t",
			dialect: Oracle,
			want:    "SELECT 'WHERE is_active' AS note FROM t",
		},
		{
			name:    "a WHERE inside a subquery is not the top-level clause",
			body:    "SELECT id FROM (SELECT id FROM t WHERE is_active) x",
			dialect: Oracle,
			want:    "SELECT id FROM (SELECT id FROM t WHERE is_active) x",
		},
		{
			name:    "the select list is passed through untouched",
			body:    "SELECT u.*, COUNT(p.id) AS n FROM users u LEFT JOIN posts p ON u.id = p.user_id WHERE u.active",
			dialect: Oracle,
			want:    "SELECT u.*, COUNT(p.id) AS n FROM users u LEFT JOIN posts p ON u.id = p.user_id WHERE u.active <> 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TranslateViewBody(tt.body, tt.dialect))
		})
	}
}

func TestTranslateViewBodyLeavesUnreadableInputAlone(t *testing.T) {
	// A clause this package cannot parse must come back exactly as it arrived.
	body := "SELECT id FROM t WHERE a @> b[1:2]"
	assert.Equal(t, body, TranslateViewBody(body, Oracle))

	// So must a body that does not scan at all.
	broken := "SELECT id FROM t WHERE 'unterminated"
	assert.Equal(t, broken, TranslateViewBody(broken, Oracle))
}

func TestFindWhereClause(t *testing.T) {
	body := "SELECT id FROM t WHERE a = 1 ORDER BY id"
	start, end, ok := findWhereClause(body)
	assert.True(t, ok)
	assert.Equal(t, "a = 1", body[start:end][1:len(body[start:end])-1])
}

func TestStripDefaultSchemaQualifiers(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			"from clause",
			"SELECT id FROM public.customers",
			"SELECT id FROM customers",
		},
		{
			"sql server schema",
			"SELECT id FROM dbo.customers WHERE dbo.customers.id > 0",
			"SELECT id FROM customers WHERE customers.id > 0",
		},
		{
			"several occurrences",
			"SELECT public.a.x FROM public.a JOIN public.b ON public.a.id = public.b.a_id",
			"SELECT a.x FROM a JOIN b ON a.id = b.a_id",
		},
		{
			// The scanner knows a string from syntax, which a pattern over the
			// raw text did not.
			"inside a string literal",
			"SELECT 'public.customers' AS note FROM t",
			"SELECT 'public.customers' AS note FROM t",
		},
		{
			"a column whose name merely starts the same way",
			"SELECT public_id FROM t",
			"SELECT public_id FROM t",
		},
		{
			"a real schema name is not touched",
			"SELECT id FROM app.customers",
			"SELECT id FROM app.customers",
		},
		{
			"not a qualifier when it is itself qualified",
			"SELECT a.public.b FROM t",
			"SELECT a.public.b FROM t",
		},
		{
			"nothing to strip",
			"SELECT id FROM customers",
			"SELECT id FROM customers",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripDefaultSchemaQualifiers(tt.body))
		})
	}
}

func TestStripDefaultSchemaLeavesUnscannableInputAlone(t *testing.T) {
	broken := "SELECT public.a FROM 'unterminated"
	assert.Equal(t, broken, stripDefaultSchemaQualifiers(broken))
}
