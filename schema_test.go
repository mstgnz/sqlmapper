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
