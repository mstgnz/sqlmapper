package expr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// kinds and texts of every token except the trailing TokEOF, which every case has.
func scan(t *testing.T, src string) []Token {
	t.Helper()
	toks, err := Tokens(src)
	require.NoError(t, err)
	require.NotEmpty(t, toks)
	require.Equal(t, TokEOF, toks[len(toks)-1].Kind)
	return toks[:len(toks)-1]
}

func texts(toks []Token) []string {
	out := make([]string, len(toks))
	for i, tok := range toks {
		out[i] = tok.Text
	}
	return out
}

func TestScanIdentifiers(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		wantText   string
		wantQuoted bool
	}{
		{"bare", "amount", "amount", false},
		{"underscore and digits", "col_2", "col_2", false},
		{"double quoted", `"AMOUNT"`, "AMOUNT", true},
		{"backtick quoted", "`meta`", "meta", true},
		{"bracket quoted", "[is active]", "is active", true},
		{"doubled double quote", `"a""b"`, `a"b`, true},
		{"doubled bracket", "[a]]b]", "a]b", true},
		{"sql server variable", "@uid", "@uid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := scan(t, tt.src)
			require.Len(t, toks, 1)
			assert.Equal(t, TokIdent, toks[0].Kind)
			assert.Equal(t, tt.wantText, toks[0].Text)
			assert.Equal(t, tt.wantQuoted, toks[0].Quoted)
		})
	}
}

func TestScanStrings(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"plain", `'active'`, "active"},
		{"empty", `''`, ""},
		{"doubled quote escape", `'it''s'`, "it's"},
		{"backslash quote escape", `'it\'s'`, "it's"},
		{"a keyword inside a string is not a keyword", `'AND'`, "AND"},
		{"a comment marker inside a string is not a comment", `'-- not a comment'`, "-- not a comment"},
		{"a paren inside a string is not punctuation", `'f(x)'`, "f(x)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := scan(t, tt.src)
			require.Len(t, toks, 1)
			assert.Equal(t, TokString, toks[0].Kind)
			assert.Equal(t, tt.want, toks[0].Text)
		})
	}
}

func TestScanNumbers(t *testing.T) {
	for _, src := range []string{"0", "42", "0.00", "1.5", "1e6", "1E-6", ".5"} {
		t.Run(src, func(t *testing.T) {
			toks := scan(t, src)
			require.Len(t, toks, 1)
			assert.Equal(t, TokNumber, toks[0].Kind)
			// The raw text is kept so that 0.00 does not render back as 0.
			assert.Equal(t, src, toks[0].Raw)
		})
	}
}

func TestScanKeywordsAreFolded(t *testing.T) {
	toks := scan(t, "a and B or NOT c")
	assert.Equal(t, []string{"a", "AND", "B", "OR", "NOT", "c"}, texts(toks))

	assert.Equal(t, TokKeyword, toks[1].Kind)
	assert.Equal(t, TokKeyword, toks[3].Kind)
	assert.Equal(t, TokKeyword, toks[4].Kind)
	// Identifiers keep the case they were written in.
	assert.Equal(t, TokIdent, toks[2].Kind)
	assert.Equal(t, "B", toks[2].Text)
}

func TestScanOperatorsMatchLongestFirst(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{"a <= b", []string{"a", "<=", "b"}},
		{"a >= b", []string{"a", ">=", "b"}},
		{"a <> b", []string{"a", "<>", "b"}},
		{"a != b", []string{"a", "!=", "b"}},
		{"a < b", []string{"a", "<", "b"}},
		{"a::numeric", []string{"a", "::", "numeric"}},
		{"a || b", []string{"a", "||", "b"}},
	}

	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			assert.Equal(t, tt.want, texts(scan(t, tt.src)))
		})
	}
}

func TestScanSkipsComments(t *testing.T) {
	assert.Equal(t, []string{"a", "=", "1"}, texts(scan(t, "a = 1 -- trailing")))
	assert.Equal(t, []string{"a", "=", "1"}, texts(scan(t, "a /* inline */ = 1")))
	assert.Equal(t, []string{"a", "=", "1"}, texts(scan(t, "a =\n/* over\n   lines */ 1")))
}

func TestScanRealWorldExpressions(t *testing.T) {
	// Every one of these came out of a real dump during the version matrix run.
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			"sql server check",
			"([amount]>=(0))",
			[]string{"(", "amount", ">=", "(", "0", ")", ")"},
		},
		{
			"postgres check with a cast",
			"(amount >= (0)::numeric)",
			[]string{"(", "amount", ">=", "(", "0", ")", "::", "numeric", ")"},
		},
		{
			"mariadb json guard",
			"json_valid(`meta`)",
			[]string{"json_valid", "(", "meta", ")"},
		},
		{
			"postgres sequence default",
			"nextval('public.customers_id_seq'::regclass)",
			[]string{"nextval", "(", "public.customers_id_seq", "::", "regclass", ")"},
		},
		{
			"sqlite check list",
			"status IN ('a','b')",
			[]string{"status", "IN", "(", "a", ",", "b", ")"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, texts(scan(t, tt.src)))
		})
	}
}

func TestScanErrors(t *testing.T) {
	for _, src := range []string{`'unterminated`, `"unterminated`, "`unterminated", "a ~ b"} {
		t.Run(src, func(t *testing.T) {
			_, err := Tokens(src)
			assert.Error(t, err)
		})
	}
}

func TestScanEmptyInput(t *testing.T) {
	toks, err := Tokens("   \n\t ")
	require.NoError(t, err)
	require.Len(t, toks, 1)
	assert.Equal(t, TokEOF, toks[0].Kind)
}
