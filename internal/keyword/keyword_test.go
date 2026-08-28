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
