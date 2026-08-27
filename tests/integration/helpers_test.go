package integration

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/require"
)

// columnByName returns the named column, failing the test when it is absent.
func columnByName(t *testing.T, table sqlmapper.Table, name string) sqlmapper.Column {
	t.Helper()
	for _, col := range table.Columns {
		if col.Name == name {
			return col
		}
	}
	require.Failf(t, "column not found", "table %q has no column %q", table.Name, name)
	return sqlmapper.Column{}
}

// constraintByType returns the first constraint of the given type, failing the
// test when the table carries none.
func constraintByType(t *testing.T, table sqlmapper.Table, kind string) sqlmapper.Constraint {
	t.Helper()
	for _, c := range table.Constraints {
		if c.Type == kind {
			return c
		}
	}
	require.Failf(t, "constraint not found", "table %q has no %s constraint", table.Name, kind)
	return sqlmapper.Constraint{}
}

// tableBody extracts the parenthesised body of a CREATE TABLE statement from
// generated SQL, so assertions can be scoped to one table.
func tableBody(t *testing.T, sql, tableName string) string {
	t.Helper()
	marker := "CREATE TABLE " + tableName + " ("
	start := strings.Index(sql, marker)
	require.NotEqual(t, -1, start, "no CREATE TABLE for %q in:\n%s", tableName, sql)

	depth := 0
	for i := start + len(marker) - 1; i < len(sql); i++ {
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[start : i+1]
			}
		}
	}
	require.Fail(t, "unbalanced parentheses", "in CREATE TABLE %q", tableName)
	return ""
}
