package integration

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
)

// A computed column was read by nobody and written by nobody. Four dialects
// dropped the expression and kept a plain column that computes nothing, and SQL
// Server's form, which states no data type at all, was read as though
// "AS (a * 2)" were the type and written straight into the output, where it
// would not load.

// generatedSource is the same computed column written five ways. Each dialect
// spells the clause its own way and only some offer the choice of where the
// value lives.
var generatedSource = map[string]string{
	"postgres":  "CREATE TABLE t (a integer, b integer GENERATED ALWAYS AS (a * 2) STORED);",
	"mysql":     "CREATE TABLE t (a int, b int AS (a * 2) STORED);",
	"oracle":    "CREATE TABLE t (a NUMBER, b NUMBER GENERATED ALWAYS AS (a * 2) VIRTUAL)\n/",
	"sqlserver": "CREATE TABLE t (a INT, b AS (a * 2) PERSISTED);\nGO",
	"sqlite":    "CREATE TABLE t (a INTEGER, b INTEGER GENERATED ALWAYS AS (a * 2) VIRTUAL);",
}

func generatedColumn(s *sqlmapper.Schema) (sqlmapper.Column, bool) {
	for _, t := range s.Tables {
		for _, c := range t.Columns {
			if strings.EqualFold(c.Name, "b") {
				return c, true
			}
		}
	}
	return sqlmapper.Column{}, false
}

func TestGeneratedColumnIsRead(t *testing.T) {
	for _, d := range alterDialects {
		t.Run(d, func(t *testing.T) {
			schema, err := alterParsers[d]().Parse(generatedSource[d])
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			col, ok := generatedColumn(schema)
			if !ok {
				t.Fatal("the computed column is missing")
			}
			if col.GeneratedExpression != "a * 2" {
				t.Errorf("expression = %q, want %q", col.GeneratedExpression, "a * 2")
			}
			// SQL Server states no type on a computed column: it infers one.
			if d == "sqlserver" {
				if col.DataType != "" {
					t.Errorf("SQL Server states no type, got %q", col.DataType)
				}
				return
			}
			if col.DataType == "" {
				t.Error("the type was lost with the clause")
			}
			// A computed column has no default of its own.
			if col.DefaultValue != "" {
				t.Errorf("default = %q", col.DefaultValue)
			}
		})
	}
}

// TestGeneratedColumnSurvivesTheConversion checks the expression reaches every
// target in that target's own spelling. PostgreSQL only stores the value and
// Oracle only computes it, so those two are written whichever way the source
// said; SQL Server writes PERSISTED rather than STORED and states no type.
func TestGeneratedColumnSurvivesTheConversion(t *testing.T) {
	for _, src := range alterDialects {
		schema, err := alterParsers[src]().Parse(generatedSource[src])
		if err != nil {
			t.Fatalf("%s parse: %v", src, err)
		}

		for _, dst := range alterDialects {
			t.Run(src+"_to_"+dst, func(t *testing.T) {
				out, err := alterParsers[dst]().Generate(schema)
				if err != nil {
					t.Fatalf("generate: %v", err)
				}

				// A SQL Server source states no type, which the three typed
				// targets require and cannot work out, so the column is stated
				// rather than written with an empty one.
				if src == "sqlserver" && dst != "sqlserver" && dst != "sqlite" {
					if !strings.Contains(out, "states no type for it") {
						t.Errorf("an untyped computed column was written anyway:\n%s", out)
					}
					if strings.Contains(out, "b  ") {
						t.Errorf("a column was written with an empty type:\n%s", out)
					}
					return
				}

				if !strings.Contains(out, "a * 2") {
					t.Errorf("the expression did not survive:\n%s", out)
				}
				switch dst {
				case "sqlserver":
					if !strings.Contains(out, "AS (a * 2)") {
						t.Errorf("T-SQL writes AS, not GENERATED ALWAYS:\n%s", out)
					}
					if strings.Contains(out, "GENERATED ALWAYS") {
						t.Errorf("T-SQL has no GENERATED ALWAYS:\n%s", out)
					}
				case "postgres":
					// PostgreSQL has only the stored form through 17.
					if !strings.Contains(out, "GENERATED ALWAYS AS (a * 2) STORED") {
						t.Errorf("PostgreSQL only stores a computed column:\n%s", out)
					}
				case "oracle":
					// Oracle's virtual column is computed on read; there is no
					// stored form for an ordinary table.
					if !strings.Contains(out, "GENERATED ALWAYS AS (a * 2) VIRTUAL") {
						t.Errorf("Oracle only computes a virtual column:\n%s", out)
					}
				}
				// A computed column takes no default anywhere.
				if strings.Contains(out, "AS (a * 2) DEFAULT") {
					t.Errorf("a computed column was given a default:\n%s", out)
				}
			})
		}
	}
}
