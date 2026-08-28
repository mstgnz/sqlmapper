package sqlmapper

import (
	"strings"

	"github.com/mstgnz/sqlmapper/internal/expr"
)

// DatabaseType represents the supported database types
type DatabaseType string

// Database represents the common interface for all database implementations
type Database interface {
	Parse(content string) (*Schema, error)
	Generate(schema *Schema) (string, error)
}

const (
	MySQL      DatabaseType = "mysql"
	PostgreSQL DatabaseType = "postgresql"
	SQLServer  DatabaseType = "sqlserver"
	Oracle     DatabaseType = "oracle"
	SQLite     DatabaseType = "sqlite"
)

// Schema represents a database schema
type Schema struct {
	// SourceDialect names the database the schema was parsed from. Parse sets
	// it; a schema built by hand leaves it empty.
	//
	// It exists so a generator can tell its own routines from someone else's.
	// A function or trigger body is procedural code, not DDL, and is carried
	// across verbatim: emitting a MySQL body into PostgreSQL produces a file
	// that fails to load. An empty value is treated as native, so a schema
	// assembled in Go is written out as the caller wrote it.
	SourceDialect DatabaseType

	Name             string
	Tables           []Table
	Procedures       []Procedure
	Functions        []Function
	Triggers         []Trigger
	Views            []View
	Sequences        []Sequence
	Extensions       []Extension
	Permissions      []Permission
	UserDefinedTypes []UserDefinedType
	Partitions       map[string][]Partition // table_name -> partitions
	DatabaseLinks    []DatabaseLink
	Tablespaces      []Tablespace
	Roles            []Role
	Users            []User
	Clusters         []Cluster
	MaterializedLogs []MaterializedViewLog
	Types            []Type
}

// RoutinesAreNativeTo reports whether the schema's functions, procedures and
// triggers came from the given dialect, so their bodies can be written out as
// they stand.
//
// A body is procedural code and is carried across verbatim, so emitting one
// into a different database produces a file that fails to load. A schema with
// no source dialect was built by the caller rather than parsed, and is taken at
// face value.
func (s *Schema) RoutinesAreNativeTo(dialect DatabaseType) bool {
	return s.SourceDialect == "" || s.SourceDialect == dialect
}

// KeyColumns returns the columns a table indexes, whether through a primary
// key, a unique constraint or an index.
//
// It exists because the strict databases refuse to index an unbounded text
// column: MySQL wants a prefix length, SQL Server rejects NVARCHAR(MAX)
// outright, and Oracle cannot index a CLOB at all. A source with no length to
// give, SQLite for instance, produces exactly that, so a generator has to know
// which columns it must bound.
func KeyColumns(table Table) map[string]bool {
	out := make(map[string]bool)

	for _, c := range table.Constraints {
		switch c.Type {
		case "PRIMARY KEY", "UNIQUE":
			for _, col := range c.Columns {
				out[col] = true
			}
		}
	}
	for _, idx := range table.Indexes {
		for _, col := range idx.Columns {
			out[col] = true
		}
	}
	for _, col := range table.Columns {
		if col.IsPrimaryKey || col.IsUnique {
			out[col.Name] = true
		}
	}

	return out
}

// HasUniqueConstraint reports whether a table constraint already covers the
// named column on its own.
//
// A column marked unique and a UNIQUE constraint over the same column are the
// same key, and writing both declares it twice: Oracle answers ORA-02261.
func HasUniqueConstraint(constraints []Constraint, column string) bool {
	for _, c := range constraints {
		if c.Type == "UNIQUE" && len(c.Columns) == 1 && strings.EqualFold(c.Columns[0], column) {
			return true
		}
	}
	return false
}

// Comment describes one comment a generator has to write out.
type Comment struct {
	Object  string // TABLE or COLUMN
	Name    string // the table, or table.column
	Comment string
}

// UntypedGeneratedColumns lists the computed columns a typed target cannot
// declare. It is the half of SplitUntypedGenerated the generators need when
// they are writing the note rather than the table.
func UntypedGeneratedColumns(table Table) []Column {
	_, untyped := SplitUntypedGenerated(table.Columns)
	return untyped
}

// SplitUntypedGenerated separates the computed columns a typed target cannot
// declare from the rest.
//
// SQL Server states no data type on a computed column: it infers one from the
// expression. PostgreSQL, MySQL and Oracle all require one, and "a * 2" is an
// integer or a decimal depending on what a is, so there is no honest way to
// work it out here. The column is left out of the table and stated instead,
// rather than emitted with an empty type, which is what it used to do: the
// whole file then failed to load.
func SplitUntypedGenerated(columns []Column) (usable, untyped []Column) {
	for _, c := range columns {
		if c.GeneratedExpression != "" && strings.TrimSpace(c.DataType) == "" {
			untyped = append(untyped, c)
			continue
		}
		usable = append(usable, c)
	}
	return usable, untyped
}

// TypeIsPortable reports whether a user-defined type can be built on the given
// target.
//
// An enumeration is a value list and every dialect that has one can build it
// from that list. Everything else is dialect-specific text, the same way a
// routine body is, so it only goes out as it stands when the schema came from
// that dialect. Writing an Oracle OBJECT into PostgreSQL produced
// "CREATE TYPE addr_t AS (OBJECT (...))", which does not load.
func (s *Schema) TypeIsPortable(t Type, dialect DatabaseType) bool {
	if strings.EqualFold(t.Kind, "ENUM") {
		return true
	}
	return s.SourceDialect == "" || s.SourceDialect == dialect
}

// StripSchemaPrefix reduces a possibly qualified name to its bare form.
//
// A grant, a reference or a table name carries the source's schema, and that
// qualifier does not survive the hop: "public." is meaningless to MySQL, and an
// Oracle owner prefix is meaningless everywhere else. The bare name is what each
// dialect resolves through its own default search path, which is also how the
// table itself is written.
func StripSchemaPrefix(name string) string {
	name = strings.TrimSpace(strings.Trim(name, `"`))
	if parts := strings.Split(name, "."); len(parts) > 1 {
		return strings.Trim(parts[len(parts)-1], `"`)
	}
	return name
}

// GranteeParts splits a grantee into its user and host halves.
//
// MySQL names a grantee as user@host and the other four dialects have no host
// at all, so a grant read from one and written to another has to be told which
// half it may keep. Without this a grant converted out of MySQL read
// "TO reader@%", which no other dialect accepts.
func GranteeParts(grantee string) (user, host string) {
	if i := strings.LastIndex(grantee, "@"); i >= 0 {
		return strings.TrimSpace(grantee[:i]), strings.TrimSpace(grantee[i+1:])
	}
	return strings.TrimSpace(grantee), ""
}

// SplitPrivileges reads a grant's privilege clause back into a list. It is the
// inverse of PrivilegeList: "SELECT, INSERT" becomes two entries, and
// "ALL PRIVILEGES" stays one because it is a single privilege, not two.
func SplitPrivileges(clause string) []string {
	var out []string
	for _, p := range strings.Split(clause, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.ToUpper(p))
		}
	}
	return out
}

// PrivilegeList renders a permission's privileges as one clause.
//
// An empty list would produce "GRANT  ON t", which is a syntax error in every
// dialect, so it falls back to the widest privilege rather than writing a
// statement that cannot load.
func PrivilegeList(privs []string) string {
	parts := make([]string, 0, len(privs))
	for _, p := range privs {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "ALL PRIVILEGES"
	}
	return strings.Join(parts, ", ")
}

// CommentStatements lists the comments a table carries, in the order they are
// written: the table first, then its columns.
func CommentStatements(table Table) []Comment {
	var out []Comment
	if table.Comment != "" {
		out = append(out, Comment{Object: "TABLE", Name: table.Name, Comment: table.Comment})
	}
	for _, col := range table.Columns {
		if col.Comment != "" {
			out = append(out, Comment{
				Object: "COLUMN", Name: table.Name + "." + col.Name, Comment: col.Comment,
			})
		}
	}
	return out
}

// Table represents a database table
type Table struct {
	Name        string
	Schema      string
	Columns     []Column
	Indexes     []Index
	Constraints []Constraint
	Data        []Row
	TableSpace  string
	Storage     *StorageClause
	Temporary   bool
	Comment     string
	Options     string // Storage engine options (e.g., ENGINE=InnoDB, CHARSET=utf8mb4)
}

// Column represents a table column
type Column struct {
	Name            string
	DataType        string
	Length          int
	Scale           int
	Precision       int
	IsNullable      bool `default:"true"`
	DefaultValue    string
	AutoIncrement   bool
	IsPrimaryKey    bool
	IsUnique        bool
	IsUnsigned      bool
	Comment         string
	Order           int
	CheckExpression string
	EnumValues      []string // for ENUM/SET types
	IsArray         bool     // PostgreSQL array type (e.g. text[])

	// GeneratedExpression is what a computed column computes. Every dialect has
	// one and spells it differently, and none of them was read: a column
	// declared GENERATED ALWAYS AS (a * 2) arrived as a plain column that
	// computes nothing, and SQL Server's form, which states no type at all, was
	// read as though "AS (a * 2)" were the type and written into the output.
	GeneratedExpression string

	// GeneratedStored says the value is written to disk rather than computed on
	// read. Not every dialect offers the choice: PostgreSQL stores, Oracle
	// computes, and the rest do either.
	GeneratedStored bool
}

// Index represents a table index
type Index struct {
	Name        string
	Columns     []string
	IsUnique    bool
	IsBitmap    bool   // Oracle bitmap index support
	IsClustered bool   // SQL Server clustered index support
	Type        string // BTREE, HASH etc.
	Condition   string // WHERE clause
	TableSpace  string
	Storage     *StorageClause
	Compression bool
}

// Constraint represents a table constraint
type Constraint struct {
	Name            string
	Type            string // PRIMARY KEY, FOREIGN KEY, UNIQUE, CHECK
	Columns         []string
	RefTable        string
	RefColumns      []string
	UpdateRule      string
	DeleteRule      string
	CheckExpression string
	Deferrable      bool
	Initially       string // IMMEDIATE, DEFERRED
}

// Row represents table data
type Row struct {
	Values map[string]interface{}
}

// Procedure represents a stored procedure
type Procedure struct {
	Name          string
	Schema        string
	Parameters    []Parameter
	Body          string
	Language      string
	Security      string // DEFINER, INVOKER
	SQLSecurity   string
	Deterministic bool
	Comment       string
}

// Function represents a database function
type Function struct {
	Name       string
	Schema     string
	Parameters []Parameter
	Returns    string
	Body       string
	Language   string
	IsProc     bool

	// Attributes carries what a dialect states about a routine besides its
	// signature: DETERMINISTIC, READS SQL DATA, SQL SECURITY DEFINER. MySQL
	// refuses to create a function without one of the first three while binary
	// logging is on, so dropping them made a routine that came out of mysqldump
	// fail to go back in.
	Attributes []string
}

// Parameter represents a procedure or function parameter
type Parameter struct {
	Name      string
	DataType  string
	Direction string // IN, OUT, INOUT
	Default   string
}

// Trigger represents a database trigger
type Trigger struct {
	Name       string
	Schema     string
	Table      string
	Timing     string
	Event      string
	Body       string
	Condition  string
	ForEachRow bool
}

// View represents a database view
type View struct {
	Name           string
	Schema         string
	Definition     string
	IsMaterialized bool
}

// Sequence represents a database sequence
type Sequence struct {
	Name        string
	Schema      string
	IncrementBy int
	MinValue    int
	MaxValue    int
	StartValue  int
	Cache       int
	Cycle       bool
}

// Extension represents a database extension
type Extension struct {
	Name    string
	Version string
	Schema  string
}

// Permission represents a database permission
type Permission struct {
	Type       string // GRANT, REVOKE
	Privileges []string
	Object     string
	Grantee    string
	WithGrant  bool
}

// UserDefinedType represents custom data types
type UserDefinedType struct {
	Name       string
	Schema     string
	BaseType   string
	Properties map[string]interface{}
}

// Partition represents table partition information
type Partition struct {
	Name          string
	Type          string // RANGE, LIST, HASH
	SubPartitions []SubPartition
	Expression    string
	Values        []string
	TableSpace    string
	Storage       *StorageClause
}

// SubPartition represents table sub-partition information
type SubPartition struct {
	Name       string
	Type       string
	Expression string
	Values     []string
	TableSpace string
	Storage    *StorageClause
}

// MaterializedViewLog represents materialized view log information
type MaterializedViewLog struct {
	Name           string
	Schema         string
	TableName      string
	Columns        []string
	RowID          bool
	PrimaryKey     bool
	SequenceNumber bool
	CommitSCN      bool
	Storage        *StorageClause
}

// DatabaseLink represents database link information
type DatabaseLink struct {
	Name        string
	Owner       string
	ConnectInfo string
	Public      bool
}

// Tablespace represents tablespace information
type Tablespace struct {
	Name        string
	Type        string // PERMANENT, TEMPORARY
	Status      string
	Autoextend  bool
	MaxSize     int64
	InitialSize int64
	DataFile    string
	BlockSize   int
	Logging     bool
}

// Role represents database role information
type Role struct {
	Name        string
	Password    string
	Permissions []Permission
	Members     []string
	System      bool
}

// User represents database user information
type User struct {
	Name        string
	Password    string
	DefaultRole string
	Roles       []string
	Permissions []Permission
	Profile     string
	Status      string
	TableSpace  string
	TempSpace   string
}

// Cluster represents Oracle cluster information
type Cluster struct {
	Name       string
	Schema     string
	TableSpace string
	Key        []string
	Tables     []string
	Size       int
	HashKeys   int
	Storage    *StorageClause
}

// StorageClause represents storage properties
type StorageClause struct {
	Initial     int64
	Next        int64
	MinExtents  int
	MaxExtents  int
	Pctincrease int
	Buffer      int
	TableSpace  string
	Logging     bool
}

// Parser represents an interface for database dump operations
type Parser interface {
	Parse(content string) (*Schema, error)
	Generate(schema *Schema) (string, error)
}

// Type represents a database type
type Type struct {
	Name       string
	Schema     string
	Kind       string // ENUM, COMPOSITE, DOMAIN, etc.
	Definition string
}

// OrderTablesByDependency sorts tables so that a table appears after every table
// its foreign keys point at, and reports the constraints that ordering alone
// cannot satisfy.
//
// Dump tools do not emit tables in dependency order. mysqldump sorts them
// alphabetically, so a child table routinely precedes its parent and the
// generated SQL fails to load with "relation does not exist". Generators call
// this to fix the order before writing anything out.
//
// The second return value maps a table name to the foreign keys that still point
// forward after sorting, which happens only when tables reference each other in
// a cycle. Those must be emitted as trailing ALTER TABLE statements rather than
// inline, because no ordering can satisfy them.
func OrderTablesByDependency(tables []Table) ([]Table, map[string][]Constraint) {
	index := make(map[string]int, len(tables))
	for i, t := range tables {
		index[t.Name] = i
	}

	// parents[i] holds the indexes table i depends on. A self-reference is not a
	// dependency: a table can always point at itself in its own definition.
	parents := make([]map[int]bool, len(tables))
	inDegree := make([]int, len(tables))
	for i, t := range tables {
		parents[i] = map[int]bool{}
		for _, c := range t.Constraints {
			if c.Type != "FOREIGN KEY" || c.RefTable == "" {
				continue
			}
			ref, ok := index[c.RefTable]
			if !ok || ref == i || parents[i][ref] {
				continue
			}
			parents[i][ref] = true
			inDegree[i]++
		}
	}

	// Kahn's algorithm, scanning in the original order so the result is stable.
	ordered := make([]Table, 0, len(tables))
	emitted := make([]bool, len(tables))
	for len(ordered) < len(tables) {
		progressed := false
		for i := range tables {
			if emitted[i] || inDegree[i] != 0 {
				continue
			}
			emitted[i] = true
			progressed = true
			ordered = append(ordered, tables[i])
			for j := range tables {
				if !emitted[j] && parents[j][i] {
					inDegree[j]--
				}
			}
		}
		if !progressed {
			// Everything left is part of a cycle; keep the original order.
			for i := range tables {
				if !emitted[i] {
					emitted[i] = true
					ordered = append(ordered, tables[i])
				}
			}
		}
	}

	position := make(map[string]int, len(ordered))
	for i, t := range ordered {
		position[t.Name] = i
	}

	deferred := map[string][]Constraint{}
	for i, t := range ordered {
		for _, c := range t.Constraints {
			if c.Type != "FOREIGN KEY" || c.RefTable == "" {
				continue
			}
			if ref, ok := position[c.RefTable]; ok && ref > i {
				deferred[t.Name] = append(deferred[t.Name], c)
			}
		}
	}

	return ordered, deferred
}

// IsJSONEmulationCheck reports whether a CHECK constraint exists only to emulate
// a JSON column type.
//
// MariaDB has no JSON type: it stores JSON in a LONGTEXT and attaches
// CHECK (json_valid(col)) to police it. Every other dialect either has a real
// JSON type or has nothing, and none of them has a json_valid function, so
// carrying the check across turns a working schema into one that will not load.
//
// Deprecated: the generators decide this for themselves now, by parsing the
// expression rather than matching text against it. This is kept because it was
// exported in v1.1.0 and will be removed in the next major version.
func IsJSONEmulationCheck(expression string) bool {
	return expr.IsJSONGuardSQL(expression)
}
