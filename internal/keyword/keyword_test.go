package keyword

import "testing"

func TestHasPrefix(t *testing.T) {
	tests := []struct {
		stmt string
		kw   string
		want bool
	}{
		{"CREATE TABLE users (id INT)", "CREATE TABLE", true},
		{"create table users (id INT)", "CREATE TABLE", true},
		{"CREATE TABLE", "CREATE TABLE", true},
		{"CREATE TABLE\n(id INT)", "CREATE TABLE", true},
		{"CREATE TABLE(id INT)", "CREATE TABLE", true},

		// The reason this package exists.
		{"CREATE TABLESPACE example_data DATAFILE 'x.dbf'", "CREATE TABLE", false},
		{"CREATE TABLES", "CREATE TABLE", false},
		{"CREATE TABLE_2", "CREATE TABLE", false},

		{"CREATE VIEW v AS SELECT 1", "CREATE TABLE", false},
		{"CREATE", "CREATE TABLE", false},
		{"", "CREATE TABLE", false},
	}

	for _, tt := range tests {
		if got := HasPrefix(tt.stmt, tt.kw); got != tt.want {
			t.Errorf("HasPrefix(%q, %q) = %v, want %v", tt.stmt, tt.kw, got, tt.want)
		}
		if got := HasPrefixBytes([]byte(tt.stmt), tt.kw); got != tt.want {
			t.Errorf("HasPrefixBytes(%q, %q) = %v, want %v", tt.stmt, tt.kw, got, tt.want)
		}
	}
}

func TestUpperASCII(t *testing.T) {
	tests := map[string]string{
		"create table": "CREATE TABLE",
		"CREATE":       "CREATE",
		"":             "",
		"id_1":         "ID_1",
		// The reason this exists: strings.ToUpper changes the length of some
		// characters, and an offset found in the folded copy then misses in the
		// original. This one folds to a different byte length under ToUpper.
		"ß":          "ß",
		"aßz":        "AßZ",
		"0\xfe0 def": "0\xfe0 DEF",
	}

	for in, want := range tests {
		if got := UpperASCII(in); got != want {
			t.Errorf("UpperASCII(%q) = %q, want %q", in, got, want)
		}
		if len(UpperASCII(in)) != len(in) {
			t.Errorf("UpperASCII(%q) changed the length", in)
		}
		if got := string(UpperASCIIBytes([]byte(in))); got != want {
			t.Errorf("UpperASCIIBytes(%q) = %q, want %q", in, got, want)
		}
	}
}
