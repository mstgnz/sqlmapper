package alter

import (
	"strings"
	"testing"
)

func TestParseReadsEveryDialectsForms(t *testing.T) {
	// Named fields, because a positional literal here has to restate every
	// column of every case and drifts the moment the struct grows one.
	type want struct {
		action Action
		table  string
		names  []string
		newTo  string
		defs   []string
		prop   Property
	}
	cases := []struct {
		name string
		stmt string
		want want
	}{
		// ADD COLUMN, which four of the five dialects used to discard.
		{"postgres add", "ALTER TABLE ONLY public.t ADD COLUMN note varchar(20) NOT NULL;",
			want{action: AddColumn, table: "t", defs: []string{"note varchar(20) NOT NULL"}}},
		{"mysql add", "ALTER TABLE `t` ADD COLUMN `note` varchar(20);",
			want{action: AddColumn, table: "t", defs: []string{"`note` varchar(20)"}}},
		{"sqlserver add", "ALTER TABLE [dbo].[t] ADD note VARCHAR(20);",
			want{action: AddColumn, table: "t", defs: []string{"note VARCHAR(20)"}}},
		{"sqlite add", "ALTER TABLE t ADD COLUMN note TEXT;",
			want{action: AddColumn, table: "t", defs: []string{"note TEXT"}}},
		// Oracle states several columns in one statement, inside parentheses.
		{"oracle add several", `ALTER TABLE "APP"."T" ADD (note VARCHAR2(20), score NUMBER(10,2))`,
			want{action: AddColumn, table: "T", defs: []string{"note VARCHAR2(20)", "score NUMBER(10,2)"}}},

		{"drop column", "ALTER TABLE t DROP COLUMN email;",
			want{action: DropColumn, table: "t", names: []string{"email"}}},
		{"drop column bare", "ALTER TABLE t DROP email;",
			want{action: DropColumn, table: "t", names: []string{"email"}}},
		{"drop column cascade", "ALTER TABLE t DROP COLUMN email CASCADE;",
			want{action: DropColumn, table: "t", names: []string{"email"}}},
		{"oracle drop several", "ALTER TABLE t DROP (a, b)",
			want{action: DropColumn, table: "t", names: []string{"a", "b"}}},

		{"rename column", "ALTER TABLE t RENAME COLUMN email TO mail;",
			want{action: RenameColumn, table: "t", names: []string{"email"}, newTo: "mail"}},
		{"rename table", "ALTER TABLE t RENAME TO t2;",
			want{action: RenameTable, table: "t", newTo: "t2"}},
		{"rename table as", "ALTER TABLE t RENAME AS t2;",
			want{action: RenameTable, table: "t", newTo: "t2"}},

		// MySQL CHANGE renames and retypes at once, so it is read as a rename
		// that also carries the new definition.
		{"mysql change", "ALTER TABLE t CHANGE COLUMN email mail varchar(200) NOT NULL;",
			want{action: RenameColumn, table: "t", names: []string{"email"}, newTo: "mail",
				defs: []string{"mail varchar(200) NOT NULL"}}},

		{"mysql modify", "ALTER TABLE t MODIFY COLUMN email varchar(200) NOT NULL;",
			want{action: ModifyColumn, table: "t", names: []string{"email"},
				defs: []string{"email varchar(200) NOT NULL"}}},
		{"oracle modify", "ALTER TABLE t MODIFY (email VARCHAR2(200))",
			want{action: ModifyColumn, table: "t", names: []string{"email"},
				defs: []string{"email VARCHAR2(200)"}}},
		{"sqlserver alter column", "ALTER TABLE t ALTER COLUMN email VARCHAR(200) NOT NULL;",
			want{action: ModifyColumn, table: "t", names: []string{"email"},
				defs: []string{"email VARCHAR(200) NOT NULL"}}},

		// PostgreSQL states one attribute at a time rather than the whole
		// column, so each carries which attribute it is: reading one as a whole
		// definition would drop the column's type and default.
		{"pg set type", "ALTER TABLE t ALTER COLUMN email TYPE varchar(200);",
			want{action: ModifyColumn, prop: SetType, table: "t", names: []string{"email"},
				defs: []string{"varchar(200)"}}},
		{"pg set data type", "ALTER TABLE t ALTER COLUMN email SET DATA TYPE varchar(200);",
			want{action: ModifyColumn, prop: SetType, table: "t", names: []string{"email"},
				defs: []string{"varchar(200)"}}},
		{"pg set not null", "ALTER TABLE t ALTER COLUMN email SET NOT NULL;",
			want{action: ModifyColumn, prop: SetNotNull, table: "t", names: []string{"email"}}},
		{"pg drop not null", "ALTER TABLE t ALTER COLUMN email DROP NOT NULL;",
			want{action: ModifyColumn, prop: DropNotNull, table: "t", names: []string{"email"}}},
		{"pg set default", "ALTER TABLE t ALTER COLUMN email SET DEFAULT 'none';",
			want{action: ModifyColumn, prop: SetDefault, table: "t", names: []string{"email"},
				defs: []string{"'none'"}}},
		{"pg drop default", "ALTER TABLE t ALTER COLUMN email DROP DEFAULT;",
			want{action: ModifyColumn, prop: DropDefault, table: "t", names: []string{"email"}}},

		{"drop constraint", "ALTER TABLE t DROP CONSTRAINT uq_email;",
			want{action: DropConstraint, table: "t", names: []string{"uq_email"}}},
		{"mysql drop fk", "ALTER TABLE t DROP FOREIGN KEY fk_a;",
			want{action: DropConstraint, table: "t", names: []string{"fk_a"}}},
		{"mysql drop index", "ALTER TABLE t DROP INDEX idx_a;",
			want{action: DropConstraint, table: "t", names: []string{"idx_a"}}},

		// SQL Server has no RENAME and scripts one as a procedure call, which is
		// why a rename never looked like an ALTER at all.
		{"sp_rename column", "EXEC sp_rename 't.email', 'mail', 'COLUMN';",
			want{action: RenameColumn, table: "t", names: []string{"email"}, newTo: "mail"}},
		{"sp_rename table", "EXEC sys.sp_rename 'dbo.t', 't2';",
			want{action: RenameTable, table: "t", newTo: "t2"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Parse(c.stmt)
			if !ok {
				t.Fatalf("not recognised: %s", c.stmt)
			}
			if got.Action != c.want.action {
				t.Errorf("action = %v, want %v", got.Action, c.want.action)
			}
			if got.Table != c.want.table {
				t.Errorf("table = %q, want %q", got.Table, c.want.table)
			}
			if strings.Join(got.Names, ",") != strings.Join(c.want.names, ",") {
				t.Errorf("names = %v, want %v", got.Names, c.want.names)
			}
			if got.NewName != c.want.newTo {
				t.Errorf("new name = %q, want %q", got.NewName, c.want.newTo)
			}
			if strings.Join(got.Definitions, "|") != strings.Join(c.want.defs, "|") {
				t.Errorf("definitions = %v, want %v", got.Definitions, c.want.defs)
			}
			if got.Property != c.want.prop {
				t.Errorf("property = %v, want %v", got.Property, c.want.prop)
			}
		})
	}
}

// TestConstraintFormsAreLeftAlone holds the boundary. Each dialect already
// reads ADD CONSTRAINT into its own constraint model, and claiming it here
// would take it away from them.
func TestConstraintFormsAreLeftAlone(t *testing.T) {
	notOurs := []string{
		"ALTER TABLE ONLY public.t ADD CONSTRAINT t_pkey PRIMARY KEY (id);",
		"ALTER TABLE t ADD PRIMARY KEY (id);",
		"ALTER TABLE t ADD FOREIGN KEY (a) REFERENCES b(id);",
		"ALTER TABLE t ADD UNIQUE (email);",
		"ALTER TABLE t ADD CHECK (id > 0);",
		"ALTER TABLE t ADD INDEX idx_a (a);",
		"ALTER TABLE t ADD KEY idx_a (a);",
		"ALTER TABLE t WITH CHECK ADD CONSTRAINT fk FOREIGN KEY (a) REFERENCES b(id);",
		"ALTER TABLE t OWNER TO postgres;",
		"CREATE TABLE t (id INT);",
		"",
		"EXEC sp_rename 'idx_old', 'idx_new', 'INDEX';",
	}
	for _, stmt := range notOurs {
		if got, ok := Parse(stmt); ok {
			t.Errorf("claimed a statement it should not: %q -> %+v", stmt, got)
		}
	}
}

func TestUnquoteHandlesEveryDialectsQuoting(t *testing.T) {
	for in, want := range map[string]string{
		"plain": "plain", "`mysql`": "mysql", `"ansi"`: "ansi",
		"[tsql]": "tsql", "  spaced  ": "spaced", "": "",
	} {
		if got := Unquote(in); got != want {
			t.Errorf("Unquote(%q) = %q, want %q", in, got, want)
		}
	}
	// A qualified name reduces to its last part, the way every table name in
	// this converter is written.
	for in, want := range map[string]string{
		"public.t": "t", `"APP"."T"`: "T", "[dbo].[t]": "t", "t": "t",
	} {
		if got := unqualify(in); got != want {
			t.Errorf("unqualify(%q) = %q, want %q", in, got, want)
		}
	}
}
