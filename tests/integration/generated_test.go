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

// TestUnsignedKeepsItsRange holds the one integer property MySQL states and the
// others do not. An unsigned int holds up to 4294967295 and a signed one half
// that, so mapping it straight across left a column that silently rejected the
// top half of its own range. The keyword was read and never written.
func TestUnsignedKeepsItsRange(t *testing.T) {
	schema, err := alterParsers["mysql"]().Parse(
		"CREATE TABLE t (a tinyint unsigned, b smallint unsigned, c int unsigned, d bigint unsigned, e int);")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, col := range schema.Tables[0].Columns {
		if col.Name == "e" {
			if col.IsUnsigned {
				t.Error("a signed column was read as unsigned")
			}
			continue
		}
		if !col.IsUnsigned {
			t.Errorf("%s was not read as unsigned", col.Name)
		}
	}

	// MySQL states it again.
	out, err := alterParsers["mysql"]().Generate(schema)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, want := range []string{"a TINYINT UNSIGNED", "c INT UNSIGNED", "d BIGINT UNSIGNED"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing:\n%s", want, out)
		}
	}
	if strings.Contains(out, "e INT UNSIGNED") {
		t.Error("a signed column was written unsigned")
	}

	// The four with no unsigned widen instead, or the column rejects values the
	// source allowed. Each spells the wider type its own way, so the check is
	// that the narrow one is gone.
	narrower := map[string][]string{
		"postgres":  {"a SMALLINT", "b INTEGER", "c BIGINT", "d NUMERIC(20)"},
		"sqlserver": {"a SMALLINT", "b INT", "c BIGINT", "d DECIMAL(20)"},
		"oracle":    {"a NUMBER(5)", "b NUMBER(10)", "c NUMBER(19)", "d NUMBER(20)"},
	}
	for target, wants := range narrower {
		t.Run(target, func(t *testing.T) {
			out, err := alterParsers[target]().Generate(schema)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			for _, want := range wants {
				if !strings.Contains(out, want) {
					t.Errorf("%q is missing, so the range was narrowed:\n%s", want, out)
				}
			}
			if strings.Contains(strings.ToUpper(out), "UNSIGNED") {
				t.Errorf("%s has no unsigned and wrote the keyword anyway:\n%s", target, out)
			}
		})
	}
}

// TestAutoIncrementIsNotWidened pins the exception. A counter carried by the
// target's own serial or identity type has no unsigned form anywhere, and
// widening it took the identity with it: a bigint unsigned AUTO_INCREMENT came
// out as NUMERIC(20) rather than BIGSERIAL.
func TestAutoIncrementIsNotWidened(t *testing.T) {
	schema, err := alterParsers["mysql"]().Parse(
		"CREATE TABLE t (`id` bigint unsigned NOT NULL AUTO_INCREMENT, PRIMARY KEY (`id`));")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	out, err := alterParsers["postgres"]().Generate(schema)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(out, "BIGSERIAL") {
		t.Errorf("the identity was widened away:\n%s", out)
	}
}

// TestStandaloneSequenceSurvives holds the sequence no column backs. PostgreSQL
// read three out of its own dump and wrote none: the two behind a serial column
// came back because the column declares its own, which is what hid the third
// going missing.
func TestStandaloneSequenceSurvives(t *testing.T) {
	schema, err := alterParsers["postgres"]().Parse(
		"CREATE SEQUENCE ticket_seq START WITH 100 INCREMENT BY 5 MAXVALUE 999 CYCLE;\n" +
			"CREATE TABLE t (id integer NOT NULL);\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	carries := map[string]string{
		"postgres":  "CREATE SEQUENCE ticket_seq",
		"oracle":    "CREATE SEQUENCE ticket_seq",
		"sqlserver": "CREATE SEQUENCE ticket_seq",
	}
	for target, want := range carries {
		t.Run(target, func(t *testing.T) {
			out, err := alterParsers[target]().Generate(schema)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !strings.Contains(out, want) {
				t.Errorf("the sequence was lost:\n%s", out)
			}
			if !strings.Contains(strings.ToUpper(out), "CYCLE") {
				t.Errorf("CYCLE was dropped, which changes what the sequence does:\n%s", out)
			}
		})
	}

	// MySQL and SQLite have no sequence and say so rather than dropping it.
	for _, target := range []string{"mysql", "sqlite"} {
		t.Run(target+"_states_it", func(t *testing.T) {
			out, err := alterParsers[target]().Generate(schema)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !strings.Contains(out, "has no sequence: ticket_seq") {
				t.Errorf("the sequence went without a word:\n%s", out)
			}
		})
	}
}

// TestMaterializedViewStaysMaterialized checks the keyword survives. A
// materialized view read as a plain one is a different object: it stops holding
// its own rows.
func TestMaterializedViewStaysMaterialized(t *testing.T) {
	for _, dump := range []string{
		"CREATE TABLE t (a integer);\nCREATE MATERIALIZED VIEW mv AS SELECT a FROM t;",
		"CREATE TABLE t (a integer);\nCREATE MATERIALIZED VIEW mv AS SELECT a FROM t WITH NO DATA;",
	} {
		schema, err := alterParsers["postgres"]().Parse(dump)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(schema.Views) != 1 || !schema.Views[0].IsMaterialized {
			t.Fatalf("not read as materialized: %+v", schema.Views)
		}
		out, err := alterParsers["postgres"]().Generate(schema)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if !strings.Contains(out, "CREATE MATERIALIZED VIEW mv") {
			t.Errorf("it came back as a plain view:\n%s", out)
		}
	}
}
