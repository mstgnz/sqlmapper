package sqlmapper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fkTable(name string, refs ...string) Table {
	t := Table{Name: name}
	for _, ref := range refs {
		t.Constraints = append(t.Constraints, Constraint{
			Name:       "fk_" + name + "_" + ref,
			Type:       "FOREIGN KEY",
			Columns:    []string{ref + "_id"},
			RefTable:   ref,
			RefColumns: []string{"id"},
		})
	}
	return t
}

func names(tables []Table) []string {
	out := make([]string, len(tables))
	for i, t := range tables {
		out[i] = t.Name
	}
	return out
}

func TestOrderTablesByDependency(t *testing.T) {
	t.Run("a child sorted before its parent is moved after it", func(t *testing.T) {
		// This is the mysqldump case: tables come out alphabetically, so orders
		// precedes users and the generated SQL used to fail to load with
		// "relation \"users\" does not exist".
		ordered, deferred := OrderTablesByDependency([]Table{
			fkTable("orders", "users"),
			fkTable("users"),
		})

		assert.Equal(t, []string{"users", "orders"}, names(ordered))
		assert.Empty(t, deferred)
	})

	t.Run("a chain is fully ordered", func(t *testing.T) {
		ordered, deferred := OrderTablesByDependency([]Table{
			fkTable("c", "b"),
			fkTable("b", "a"),
			fkTable("a"),
		})

		assert.Equal(t, []string{"a", "b", "c"}, names(ordered))
		assert.Empty(t, deferred)
	})

	t.Run("independent tables keep their original order", func(t *testing.T) {
		ordered, deferred := OrderTablesByDependency([]Table{
			fkTable("z"), fkTable("m"), fkTable("a"),
		})

		assert.Equal(t, []string{"z", "m", "a"}, names(ordered))
		assert.Empty(t, deferred)
	})

	t.Run("a table with two parents follows both", func(t *testing.T) {
		ordered, _ := OrderTablesByDependency([]Table{
			fkTable("line_items", "orders", "products"),
			fkTable("orders", "users"),
			fkTable("products"),
			fkTable("users"),
		})

		got := names(ordered)
		pos := map[string]int{}
		for i, n := range got {
			pos[n] = i
		}
		assert.Less(t, pos["users"], pos["orders"])
		assert.Less(t, pos["orders"], pos["line_items"])
		assert.Less(t, pos["products"], pos["line_items"])
	})

	t.Run("a self reference is not a dependency", func(t *testing.T) {
		// A tree table pointing at its own parent column is valid inline and
		// must not be treated as something to order around.
		ordered, deferred := OrderTablesByDependency([]Table{fkTable("nodes", "nodes")})

		assert.Equal(t, []string{"nodes"}, names(ordered))
		assert.Empty(t, deferred, "a self reference can always stay inline")
	})

	t.Run("a cycle defers the constraint that closes it", func(t *testing.T) {
		// No ordering satisfies a mutual reference, so one side has to be
		// emitted as a trailing ALTER TABLE.
		ordered, deferred := OrderTablesByDependency([]Table{
			fkTable("a", "b"),
			fkTable("b", "a"),
		})

		require.Len(t, ordered, 2)
		require.Len(t, deferred, 1, "exactly one direction should be deferred")

		for table, cs := range deferred {
			assert.Contains(t, []string{"a", "b"}, table)
			require.Len(t, cs, 1)
			assert.Equal(t, "FOREIGN KEY", cs[0].Type)
		}
	})

	t.Run("a foreign key to a table outside the set is ignored", func(t *testing.T) {
		// Partial dumps reference tables they do not define.
		ordered, deferred := OrderTablesByDependency([]Table{fkTable("orders", "elsewhere")})

		assert.Equal(t, []string{"orders"}, names(ordered))
		assert.Empty(t, deferred)
	})

	t.Run("no tables", func(t *testing.T) {
		ordered, deferred := OrderTablesByDependency(nil)
		assert.Empty(t, ordered)
		assert.Empty(t, deferred)
	})

	t.Run("every input table is returned exactly once", func(t *testing.T) {
		in := []Table{
			fkTable("d", "c"), fkTable("c", "b"), fkTable("b", "a"), fkTable("a"),
			fkTable("x", "y"), fkTable("y", "x"), fkTable("lonely"),
		}
		ordered, _ := OrderTablesByDependency(in)

		require.Len(t, ordered, len(in))
		seen := map[string]int{}
		for _, tbl := range ordered {
			seen[tbl.Name]++
		}
		for _, tbl := range in {
			assert.Equal(t, 1, seen[tbl.Name], "table %q", tbl.Name)
		}
	})
}

func TestIsJSONEmulationCheck(t *testing.T) {
	// MariaDB attaches this to the LONGTEXT it uses in place of a JSON type.
	assert.True(t, IsJSONEmulationCheck("json_valid(`meta`)"))
	assert.True(t, IsJSONEmulationCheck("JSON_VALID (meta)"))
	assert.True(t, IsJSONEmulationCheck("(json_valid(`payload`))"))

	assert.False(t, IsJSONEmulationCheck("amount >= 0"))
	assert.False(t, IsJSONEmulationCheck("status IN ('a','b')"))
	assert.False(t, IsJSONEmulationCheck(""))
	assert.False(t, IsJSONEmulationCheck("json_validated = 1"),
		"a column that merely starts with the same letters is not the guard")
}

func TestRoutinesAreNativeTo(t *testing.T) {
	parsed := &Schema{SourceDialect: MySQL}
	assert.True(t, parsed.RoutinesAreNativeTo(MySQL))
	assert.False(t, parsed.RoutinesAreNativeTo(PostgreSQL))

	// A schema assembled in Go is taken at face value rather than commented out.
	handBuilt := &Schema{}
	assert.True(t, handBuilt.RoutinesAreNativeTo(MySQL))
	assert.True(t, handBuilt.RoutinesAreNativeTo(Oracle))
}

func TestKeyColumns(t *testing.T) {
	table := Table{
		Columns: []Column{
			{Name: "id", IsPrimaryKey: true},
			{Name: "email"},
			{Name: "slug", IsUnique: true},
			{Name: "note"},
		},
		Constraints: []Constraint{
			{Type: "UNIQUE", Columns: []string{"email"}},
			{Type: "FOREIGN KEY", Columns: []string{"note"}, RefTable: "notes"},
			{Type: "CHECK", CheckExpression: "note <> ''"},
		},
		Indexes: []Index{{Name: "idx", Columns: []string{"note"}}},
	}

	got := KeyColumns(table)

	for _, name := range []string{"id", "email", "slug", "note"} {
		assert.True(t, got[name], "%s is part of a key or an index", name)
	}
	assert.Len(t, got, 4)

	// A foreign key or a check alone does not make a column indexed.
	only := Table{Constraints: []Constraint{
		{Type: "FOREIGN KEY", Columns: []string{"a"}},
		{Type: "CHECK", CheckExpression: "a > 0"},
	}}
	assert.Empty(t, KeyColumns(only))
}

func TestStripSchemaPrefix(t *testing.T) {
	cases := map[string]string{
		"customers":         "customers",
		"public.customers":  "customers",
		`"APP"."CUSTOMERS"`: "CUSTOMERS",
		`"public"."orders"`: "orders",
		"  spaced.name  ":   "name",
		"a.b.c":             "c",
		"":                  "",
	}
	for in, want := range cases {
		if got := StripSchemaPrefix(in); got != want {
			t.Errorf("StripSchemaPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGranteeParts(t *testing.T) {
	// MySQL is the only dialect that names a host, and it is the last @ that
	// separates the two: a user name may contain one.
	cases := []struct {
		in         string
		user, host string
	}{
		{"reporting", "reporting", ""},
		{"reporting@%", "reporting", "%"},
		{"reporting@localhost", "reporting", "localhost"},
		{"a@b@c", "a@b", "c"},
		{"  padded @ % ", "padded", "%"},
	}
	for _, c := range cases {
		user, host := GranteeParts(c.in)
		if user != c.user || host != c.host {
			t.Errorf("GranteeParts(%q) = %q,%q, want %q,%q", c.in, user, host, c.user, c.host)
		}
	}
}

func TestPrivilegeRoundTrip(t *testing.T) {
	// An empty list would render "GRANT  ON t", which no dialect accepts, so it
	// falls back to the widest privilege rather than writing a statement that
	// cannot load.
	if got := PrivilegeList(nil); got != "ALL PRIVILEGES" {
		t.Errorf("PrivilegeList(nil) = %q", got)
	}
	if got := PrivilegeList([]string{"  ", ""}); got != "ALL PRIVILEGES" {
		t.Errorf("PrivilegeList(blanks) = %q", got)
	}
	if got := PrivilegeList([]string{"SELECT", " INSERT "}); got != "SELECT, INSERT" {
		t.Errorf("PrivilegeList = %q", got)
	}

	// ALL PRIVILEGES is one privilege, not two, so it survives the split whole.
	if got := SplitPrivileges("ALL PRIVILEGES"); len(got) != 1 || got[0] != "ALL PRIVILEGES" {
		t.Errorf("SplitPrivileges(ALL PRIVILEGES) = %v", got)
	}
	if got := SplitPrivileges("select , insert"); len(got) != 2 || got[0] != "SELECT" || got[1] != "INSERT" {
		t.Errorf("SplitPrivileges = %v", got)
	}
	if got := SplitPrivileges(" , "); got != nil {
		t.Errorf("SplitPrivileges(blank) = %v", got)
	}

	round := PrivilegeList(SplitPrivileges("SELECT, INSERT, UPDATE"))
	if round != "SELECT, INSERT, UPDATE" {
		t.Errorf("round trip = %q", round)
	}
}

func TestHasUniqueConstraint(t *testing.T) {
	// It answers for a single-column UNIQUE constraint only: a composite one
	// does not make either of its columns unique on its own.
	constraints := []Constraint{
		{Type: "UNIQUE", Columns: []string{"code"}},
		{Type: "UNIQUE", Columns: []string{"tenant_id", "slug"}},
		{Type: "PRIMARY KEY", Columns: []string{"id"}},
	}
	if !HasUniqueConstraint(constraints, "code") {
		t.Error("code carries a single-column UNIQUE")
	}
	if !HasUniqueConstraint(constraints, "CODE") {
		t.Error("the column name is matched without regard to case")
	}
	for _, name := range []string{"tenant_id", "slug", "id", "missing"} {
		if HasUniqueConstraint(constraints, name) {
			t.Errorf("%q is not uniquely constrained on its own", name)
		}
	}
}

func TestCommentStatementsCoversBothLevels(t *testing.T) {
	table := Table{
		Name:    "customers",
		Comment: "people who buy things",
		Columns: []Column{
			{Name: "email", Comment: "login address"},
			{Name: "note"},
		},
	}
	got := CommentStatements(table)
	if len(got) != 2 {
		t.Fatalf("want a table comment and one column comment, got %d: %+v", len(got), got)
	}
	if got[0].Object != "TABLE" || got[0].Name != "customers" {
		t.Errorf("first should be the table: %+v", got[0])
	}
	if got[1].Object != "COLUMN" || got[1].Name != "customers.email" {
		t.Errorf("second should be the column: %+v", got[1])
	}
	if len(CommentStatements(Table{Name: "bare"})) != 0 {
		t.Error("a table with no comments states none")
	}
}

// TestResolveAliasTypes checks a column naming a SQL Server alias is rewritten
// into the type the alias stands for. A target that cannot build an alias still
// has to declare the column, and "mobile dbo.phone" names nothing there.
func TestResolveAliasTypes(t *testing.T) {
	schema := &Schema{
		Types: []Type{
			{Name: "phone", Schema: "dbo", Kind: "ALIAS", Definition: "VARCHAR(20) NOT NULL"},
			{Name: "money2", Kind: "ALIAS", Definition: "DECIMAL(12,2)"},
			{Name: "order_status", Kind: "ENUM", Definition: "'a', 'b'"},
		},
		Tables: []Table{{
			Name: "contacts",
			Columns: []Column{
				{Name: "id", DataType: "int"},
				{Name: "mobile", DataType: "dbo.phone", IsNullable: true},
				{Name: "spare", DataType: "phone", IsNullable: true},
				{Name: "amount", DataType: "money2"},
				{Name: "status", DataType: "order_status"},
			},
		}},
	}

	got := ResolveAliasTypes(schema)

	cols := map[string]Column{}
	for _, c := range got.Tables[0].Columns {
		cols[c.Name] = c
	}
	// The name and the size are separated, or the target's type map never sees
	// the name and an alias over VARCHAR(20) reaches Oracle as VARCHAR, not
	// VARCHAR2.
	if cols["mobile"].DataType != "VARCHAR" || cols["mobile"].Length != 20 {
		t.Errorf("mobile = %q(%d)", cols["mobile"].DataType, cols["mobile"].Length)
	}
	// The base carries its own nullability, which belongs to the column.
	if cols["mobile"].IsNullable {
		t.Error("the alias said NOT NULL")
	}
	// The alias resolves whether or not the column qualified it.
	if cols["spare"].DataType != "VARCHAR" {
		t.Errorf("spare = %q", cols["spare"].DataType)
	}
	if cols["amount"].DataType != "DECIMAL" || cols["amount"].Length != 12 || cols["amount"].Scale != 2 {
		t.Errorf("amount = %q(%d,%d)", cols["amount"].DataType, cols["amount"].Length, cols["amount"].Scale)
	}
	// An enum is not an alias and is left where it is: the column names a type
	// the target does declare.
	if cols["status"].DataType != "order_status" {
		t.Errorf("status = %q", cols["status"].DataType)
	}

	// Once the columns carry the base type there is nothing left for the alias
	// to say, so it goes; the enum stays.
	if len(got.Types) != 1 || got.Types[0].Kind != "ENUM" {
		t.Errorf("types = %+v", got.Types)
	}

	// The original is untouched, or generating for two targets would have the
	// first rewrite the schema under the second.
	if schema.Tables[0].Columns[1].DataType != "dbo.phone" || len(schema.Types) != 3 {
		t.Error("the input schema was modified in place")
	}

	// A schema with no alias is handed back as it stands.
	plain := &Schema{Tables: []Table{{Name: "t"}}}
	if ResolveAliasTypes(plain) != plain {
		t.Error("a schema with no alias was copied for nothing")
	}
}
