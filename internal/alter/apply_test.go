package alter

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
)

// readColumn is a reader just rich enough to exercise Apply: name, type and a
// length, which is what the mutations turn on. Each dialect supplies a real one.
func readColumn(def string) (sqlmapper.Column, error) {
	fields := strings.Fields(def)
	if len(fields) < 2 {
		return sqlmapper.Column{}, errNotAColumn
	}
	col := sqlmapper.Column{Name: Unquote(fields[0]), DataType: fields[1], IsNullable: true}
	if i := strings.Index(col.DataType, "("); i >= 0 {
		size := strings.Trim(col.DataType[i:], "()")
		col.DataType = col.DataType[:i]
		col.Length = len(size) // stands in for a real width; only change matters
	}
	if strings.Contains(strings.ToUpper(def), "NOT NULL") {
		col.IsNullable = false
	}
	return col, nil
}

var errNotAColumn = errString("not a column definition")

type errString string

func (e errString) Error() string { return string(e) }

func testSchema() *sqlmapper.Schema {
	return &sqlmapper.Schema{Tables: []sqlmapper.Table{{
		Name: "customers",
		Columns: []sqlmapper.Column{
			{Name: "id", DataType: "int"},
			{Name: "email", DataType: "varchar", Length: 50, IsNullable: true},
		},
		Constraints: []sqlmapper.Constraint{
			{Name: "uq_email", Type: "UNIQUE", Columns: []string{"email"}},
			{Name: "pk", Type: "PRIMARY KEY", Columns: []string{"id"}},
		},
		Indexes: []sqlmapper.Index{
			{Name: "ix_email", Columns: []string{"email"}},
			{Name: "ix_id", Columns: []string{"id"}},
		},
	}, {
		Name:    "orders",
		Columns: []sqlmapper.Column{{Name: "customer_id", DataType: "int"}},
		Constraints: []sqlmapper.Constraint{
			{Name: "fk", Type: "FOREIGN KEY", Columns: []string{"customer_id"}, RefTable: "customers"},
		},
	}}}
}

func run(t *testing.T, schema *sqlmapper.Schema, stmt string) {
	t.Helper()
	st, ok := Parse(stmt)
	if !ok {
		t.Fatalf("not recognised: %s", stmt)
	}
	Apply(schema, st, Reader{Column: readColumn})
}

func columnNames(cols []sqlmapper.Column) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		out = append(out, c.Name)
	}
	return out
}

func TestApplyAddColumn(t *testing.T) {
	s := testSchema()
	run(t, s, "ALTER TABLE customers ADD COLUMN note varchar(20);")
	if got := strings.Join(columnNames(s.Tables[0].Columns), ","); got != "id,email,note" {
		t.Errorf("columns = %s", got)
	}

	// Adding a column that is already there replaces it rather than doubling it.
	run(t, s, "ALTER TABLE customers ADD COLUMN note varchar(999) NOT NULL;")
	if got := strings.Join(columnNames(s.Tables[0].Columns), ","); got != "id,email,note" {
		t.Errorf("a repeated add doubled the column: %s", got)
	}
	if s.Tables[0].Columns[2].IsNullable {
		t.Error("the second definition did not take")
	}
}

// TestApplyDropColumnTakesItsConstraints holds the part that is easy to miss.
// A constraint or an index over a column that no longer exists cannot be built,
// so leaving it behind produces a schema that fails to load.
func TestApplyDropColumnTakesItsConstraints(t *testing.T) {
	s := testSchema()
	run(t, s, "ALTER TABLE customers DROP COLUMN email;")

	if hasColumn(s.Tables[0], "email") {
		t.Error("the column is still there")
	}
	for _, c := range s.Tables[0].Constraints {
		if c.Name == "uq_email" {
			t.Error("a constraint over the dropped column survived")
		}
	}
	for _, i := range s.Tables[0].Indexes {
		if i.Name == "ix_email" {
			t.Error("an index over the dropped column survived")
		}
	}
	// What did not name the column is untouched.
	if len(s.Tables[0].Constraints) != 1 || len(s.Tables[0].Indexes) != 1 {
		t.Errorf("too much was removed: %d constraints, %d indexes",
			len(s.Tables[0].Constraints), len(s.Tables[0].Indexes))
	}
}

// TestApplyRenameColumnMovesEveryUse checks the name moves everywhere, not only
// on the column: a constraint still naming the old one cannot be built.
func TestApplyRenameColumnMovesEveryUse(t *testing.T) {
	s := testSchema()
	run(t, s, "ALTER TABLE customers RENAME COLUMN email TO mail;")

	if !hasColumn(s.Tables[0], "mail") || hasColumn(s.Tables[0], "email") {
		t.Fatalf("not renamed: %v", columnNames(s.Tables[0].Columns))
	}
	if s.Tables[0].Constraints[0].Columns[0] != "mail" {
		t.Errorf("the constraint still names the old column: %v", s.Tables[0].Constraints[0].Columns)
	}
	if s.Tables[0].Indexes[0].Columns[0] != "mail" {
		t.Errorf("the index still names the old column: %v", s.Tables[0].Indexes[0].Columns)
	}
}

// TestApplyRenameTableMovesForeignKeys checks a key pointing at the old name is
// carried over, or it would reference a table that no longer exists.
func TestApplyRenameTableMovesForeignKeys(t *testing.T) {
	s := testSchema()
	run(t, s, "ALTER TABLE customers RENAME TO clients;")

	if s.Tables[0].Name != "clients" {
		t.Errorf("table = %s", s.Tables[0].Name)
	}
	if got := s.Tables[1].Constraints[0].RefTable; got != "clients" {
		t.Errorf("the foreign key still points at %q", got)
	}
}

func TestApplyModify(t *testing.T) {
	// A dialect that restates the whole column replaces it.
	s := testSchema()
	run(t, s, "ALTER TABLE customers MODIFY COLUMN email varchar(200) NOT NULL;")
	col, _ := findColumn(s.Tables[0], "email")
	if col.IsNullable {
		t.Error("NOT NULL did not take")
	}

	// PostgreSQL states one attribute, and only that attribute moves: reading
	// its statement as a whole definition dropped the rest of the column.
	s = testSchema()
	run(t, s, "ALTER TABLE customers ALTER COLUMN email SET NOT NULL;")
	col, _ = findColumn(s.Tables[0], "email")
	if col.IsNullable {
		t.Error("SET NOT NULL did not take")
	}
	if col.DataType != "varchar" || col.Length != 50 {
		t.Errorf("the type was lost: %s(%d)", col.DataType, col.Length)
	}

	run(t, s, "ALTER TABLE customers ALTER COLUMN email DROP NOT NULL;")
	col, _ = findColumn(s.Tables[0], "email")
	if !col.IsNullable {
		t.Error("DROP NOT NULL did not take")
	}

	// The schema holds the value, not the literal: every generator quotes it
	// again for its own dialect.
	run(t, s, "ALTER TABLE customers ALTER COLUMN email SET DEFAULT 'none';")
	col, _ = findColumn(s.Tables[0], "email")
	if col.DefaultValue != "none" {
		t.Errorf("default = %q", col.DefaultValue)
	}

	run(t, s, "ALTER TABLE customers ALTER COLUMN email DROP DEFAULT;")
	col, _ = findColumn(s.Tables[0], "email")
	if col.DefaultValue != "" {
		t.Errorf("the default survived the drop: %q", col.DefaultValue)
	}
}

// TestApplyDefaultHook checks a dialect's own reading of a default wins. Taking
// the literal as it stands stored "'draft'::character varying" as the value and
// every conversion carried the cast onwards.
func TestApplyDefaultHook(t *testing.T) {
	s := testSchema()
	st, _ := Parse("ALTER TABLE customers ALTER COLUMN email SET DEFAULT 'draft'::character varying;")
	Apply(s, st, Reader{
		Column: readColumn,
		Default: func(col *sqlmapper.Column, raw string) {
			col.DefaultValue = strings.Trim(strings.SplitN(raw, "::", 2)[0], "'")
		},
	})
	col, _ := findColumn(s.Tables[0], "email")
	if col.DefaultValue != "draft" {
		t.Errorf("default = %q", col.DefaultValue)
	}
}

func TestApplyDropConstraint(t *testing.T) {
	s := testSchema()
	run(t, s, "ALTER TABLE customers DROP CONSTRAINT uq_email;")
	if len(s.Tables[0].Constraints) != 1 {
		t.Errorf("constraints = %d", len(s.Tables[0].Constraints))
	}
	// A dialect's DROP INDEX reaches the same place, and only one will match.
	run(t, s, "ALTER TABLE customers DROP INDEX ix_email;")
	if len(s.Tables[0].Indexes) != 1 {
		t.Errorf("indexes = %d", len(s.Tables[0].Indexes))
	}
}

// TestApplyIgnoresWhatItCannotFind holds the tolerance a migration needs: a file
// often alters a table created in an earlier one, and refusing over it would be
// worse than reading what is present.
func TestApplyIgnoresWhatItCannotFind(t *testing.T) {
	s := testSchema()
	before := len(s.Tables[0].Columns)

	for _, stmt := range []string{
		"ALTER TABLE nowhere ADD COLUMN note varchar(20);",
		"ALTER TABLE customers DROP COLUMN missing;",
		"ALTER TABLE customers RENAME COLUMN missing TO other;",
		"ALTER TABLE customers ALTER COLUMN missing SET NOT NULL;",
		"ALTER TABLE customers DROP CONSTRAINT missing;",
		"ALTER TABLE customers ADD COLUMN broken;",
	} {
		st, ok := Parse(stmt)
		if !ok {
			continue
		}
		Apply(s, st, Reader{Column: readColumn})
	}

	if len(s.Tables[0].Columns) != before {
		t.Errorf("columns changed: %v", columnNames(s.Tables[0].Columns))
	}

	// A reader with no Column function cannot read anything, and a nil schema is
	// not a panic.
	Apply(nil, Statement{Action: AddColumn}, Reader{Column: readColumn})
	Apply(s, Statement{Action: AddColumn, Table: "customers"}, Reader{})
}

func TestApplyAllSkipsWhatIsNotAnAlter(t *testing.T) {
	s := testSchema()
	ApplyAll(s, []string{
		"CREATE TABLE other (id INT);",
		"ALTER TABLE customers ADD COLUMN note varchar(20);",
		"-- a comment",
		"",
	}, Reader{Column: readColumn})

	if !hasColumn(s.Tables[0], "note") {
		t.Error("the ALTER in the middle was not applied")
	}
	if len(s.Tables) != 2 {
		t.Error("ApplyAll created or removed a table")
	}
}

func hasColumn(t sqlmapper.Table, name string) bool {
	_, ok := findColumn(t, name)
	return ok
}

func findColumn(t sqlmapper.Table, name string) (sqlmapper.Column, bool) {
	for _, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return sqlmapper.Column{}, false
}
