package expr

import "strings"

// Dialect identifies the SQL flavour an expression is written in or rendered
// for.
type Dialect int

const (
	// Generic renders without applying any dialect's quoting or naming rules.
	Generic Dialect = iota
	MySQL
	PostgreSQL
	SQLite
	Oracle
	SQLServer
)

// DialectOf maps the schema model's dialect names onto this package's. An
// unknown name is Generic, which renders a faithful but unopinionated form.
func DialectOf(name string) Dialect {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mysql", "mariadb":
		return MySQL
	case "postgres", "postgresql", "pgsql":
		return PostgreSQL
	case "sqlite", "sqlite3":
		return SQLite
	case "oracle":
		return Oracle
	case "sqlserver", "mssql":
		return SQLServer
	}
	return Generic
}

func (d Dialect) String() string {
	switch d {
	case MySQL:
		return "mysql"
	case PostgreSQL:
		return "postgres"
	case SQLite:
		return "sqlite"
	case Oracle:
		return "oracle"
	case SQLServer:
		return "sqlserver"
	}
	return "generic"
}

// quotes returns the pair a dialect wraps an identifier in.
func (d Dialect) quotes() (open, close string) {
	switch d {
	case MySQL:
		return "`", "`"
	case SQLServer:
		return "[", "]"
	}
	return `"`, `"`
}

// hasBoolean reports whether the dialect has a real boolean type. Where it does
// not, an expression used as a condition has to compare against a number, which
// is why a PostgreSQL view saying WHERE is_active fails to load anywhere else.
func (d Dialect) hasBoolean() bool {
	switch d {
	case Oracle, SQLServer:
		// Oracle gained one only in 23ai, and SQL Server still has none: a BIT
		// is a number, not a truth value, and cannot stand alone as a condition.
		return false
	}
	// PostgreSQL has a real boolean. MySQL and SQLite accept both the TRUE and
	// FALSE keywords and a bare column as a condition, because their integers
	// are truthy. Generic applies no dialect rewrite at all.
	return true
}

// defaultSchemas are the schema names a dialect qualifies everything with. A
// qualifier naming one of them carries no information: it does not resolve at
// another dialect, and at its own it is noise.
var defaultSchemas = map[string]bool{
	"public": true, // PostgreSQL
	"dbo":    true, // SQL Server
}

// now returns how the dialect spells the current timestamp.
func (d Dialect) now() string {
	switch d {
	case PostgreSQL:
		return "now()"
	case Oracle:
		return "SYSTIMESTAMP"
	case SQLServer:
		return "SYSUTCDATETIME()"
	}
	return "CURRENT_TIMESTAMP"
}

// nowAliases are every spelling of "the current timestamp" this package
// recognises, in lower case and without a trailing call.
var nowAliases = map[string]bool{
	"now":               true,
	"current_timestamp": true,
	"localtimestamp":    true,
	"getdate":           true,
	"getutcdate":        true,
	"sysdatetime":       true,
	"sysutcdatetime":    true,
	"systimestamp":      true,
	"sysdate":           true,
	"current_date":      true,
}

// nowInDefault is how a dialect spells the current timestamp where a column
// default is expected. Oracle and SQL Server have no CURRENT_TIMESTAMP to put
// there; everywhere else it is the standard spelling and the one a reader
// expects.
func (d Dialect) nowInDefault() string {
	switch d {
	case Oracle:
		return "SYSTIMESTAMP"
	case SQLServer:
		return "SYSUTCDATETIME()"
	}
	return "CURRENT_TIMESTAMP"
}
