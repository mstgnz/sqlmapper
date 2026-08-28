package sqlfmt

import (
	"reflect"
	"testing"
)

func TestSplitTopLevelCommas(t *testing.T) {
	tests := []struct {
		body string
		want []string
	}{
		{"a INT, b TEXT", []string{"a INT", " b TEXT"}},
		{"a DECIMAL(10,2), b INT", []string{"a DECIMAL(10,2)", " b INT"}},
		{"a INT CHECK (s IN ('x','y')), b INT", []string{"a INT CHECK (s IN ('x','y'))", " b INT"}},

		// The one that started this: a comma inside a comment.
		{"email varchar(255) COMMENT 'login address, unique', id INT",
			[]string{"email varchar(255) COMMENT 'login address, unique'", " id INT"}},

		{"a INT DEFAULT 'it''s, really', b INT",
			[]string{"a INT DEFAULT 'it''s, really'", " b INT"}},

		{"", nil},
		{"a INT", []string{"a INT"}},
		{"a INT,", []string{"a INT"}},
	}

	for _, tt := range tests {
		if got := SplitTopLevelCommas(tt.body); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitTopLevelCommas(%q) = %q, want %q", tt.body, got, tt.want)
		}
	}
}
