package alter

import (
	"regexp"
	"strings"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/internal/keyword"
)

// Reader is what a dialect supplies so its own syntax is read by its own code.
// Every dialect writes a definition its own way, and this package would
// otherwise grow five readers that drift.
type Reader struct {
	// Column reads one column definition. Required.
	Column func(def string) (sqlmapper.Column, error)

	// Default applies a default literal to a column. It is optional, and a
	// dialect that writes nextval() or a ::type cast needs its own reading of
	// the value: taking the literal as it stands stored
	// "'draft'::character varying" as the default and every conversion carried
	// the cast onwards.
	Default func(col *sqlmapper.Column, raw string)
}

// Apply replays one ALTER onto the schema.
//
// A schema file is read top to bottom and the result is the state it leaves
// behind, which is the only coherent reading of a file that both creates a
// table and then changes it. It is also what the dialects already did for the
// few ALTER forms they recognised; this makes the rest behave the same rather
// than vanish.
//
// A statement naming a table that is not there is ignored: a migration file
// often alters a table created in an earlier one, and refusing the whole file
// over it would be worse than reading what is present.
func Apply(schema *sqlmapper.Schema, st Statement, read Reader) {
	if schema == nil || read.Column == nil {
		return
	}
	// The schema-level drops name an object rather than a table's part, so they
	// are answered before a table is looked up.
	switch st.Action {
	case DropTable, DropIndex, DropView, DropSequence, DropType:
		if len(st.Names) > 0 {
			applyDrop(schema, st.Action, st.Names[0])
		}
		return
	}

	table := findTable(schema, st.Table)
	if table == nil {
		return
	}

	switch st.Action {
	case AddColumn:
		for _, def := range st.Definitions {
			col, err := read.Column(def)
			if err != nil || col.Name == "" {
				continue
			}
			if i := columnIndex(table, col.Name); i >= 0 {
				table.Columns[i] = col
				continue
			}
			table.Columns = append(table.Columns, col)
		}

	case DropColumn:
		for _, name := range st.Names {
			dropColumn(table, name)
		}

	case RenameColumn:
		if len(st.Names) == 0 || st.NewName == "" {
			return
		}
		renameColumn(table, st.Names[0], st.NewName)
		// MySQL's CHANGE renames and restates the column in one statement, so
		// the new definition is applied on top of the rename.
		for _, def := range st.Definitions {
			if col, err := read.Column(def); err == nil && col.Name != "" {
				if i := columnIndex(table, col.Name); i >= 0 {
					table.Columns[i] = col
				}
			}
		}

	case RenameTable:
		if st.NewName == "" {
			return
		}
		old := table.Name
		table.Name = st.NewName
		// A foreign key pointing at the old name would otherwise reference a
		// table that no longer exists.
		for i := range schema.Tables {
			for j := range schema.Tables[i].Constraints {
				c := &schema.Tables[i].Constraints[j]
				if strings.EqualFold(c.RefTable, old) {
					c.RefTable = st.NewName
				}
			}
		}

	case ModifyColumn:
		applyModify(table, st, read)

	case DropConstraint:
		if len(st.Names) == 0 {
			return
		}
		dropNamed(table, st.Names[0])
	}
}

// applyModify changes one column. A dialect that restates the whole definition
// replaces it; PostgreSQL states one attribute and only that attribute moves,
// because reading its statement as a whole definition dropped the rest.
func applyModify(table *sqlmapper.Table, st Statement, read Reader) {
	if len(st.Names) == 0 {
		return
	}

	if st.Property == WholeColumn {
		for _, def := range st.Definitions {
			col, err := read.Column(def)
			if err != nil || col.Name == "" {
				continue
			}
			if i := columnIndex(table, col.Name); i >= 0 {
				table.Columns[i] = col
			}
		}
		return
	}

	i := columnIndex(table, st.Names[0])
	if i < 0 {
		return
	}
	col := &table.Columns[i]

	switch st.Property {
	case SetType:
		if len(st.Definitions) == 0 {
			return
		}
		// The type is read through the dialect's own reader, by handing it a
		// definition it recognises: the name plus the new type.
		typed, err := read.Column(col.Name + " " + st.Definitions[0])
		if err != nil {
			return
		}
		col.DataType = typed.DataType
		col.Length = typed.Length
		col.Scale = typed.Scale
		col.IsArray = typed.IsArray
		col.EnumValues = typed.EnumValues
	case SetNotNull:
		col.IsNullable = false
	case DropNotNull:
		col.IsNullable = true
	case SetDefault:
		if len(st.Definitions) == 0 {
			return
		}
		if read.Default != nil {
			read.Default(col, st.Definitions[0])
			return
		}
		col.DefaultValue = unquoteLiteral(st.Definitions[0])
	case DropDefault:
		col.DefaultValue = ""
	}
}

// unquoteLiteral takes the value out of a default literal. The schema holds the
// value, not the literal: every generator quotes it again for its own dialect,
// and keeping the quotes here corrupted the default in every conversion.
func unquoteLiteral(v string) string {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), ";"))
	if len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

func findTable(schema *sqlmapper.Schema, name string) *sqlmapper.Table {
	for i := range schema.Tables {
		if strings.EqualFold(schema.Tables[i].Name, name) {
			return &schema.Tables[i]
		}
	}
	return nil
}

func columnIndex(table *sqlmapper.Table, name string) int {
	for i := range table.Columns {
		if strings.EqualFold(table.Columns[i].Name, name) {
			return i
		}
	}
	return -1
}

// dropColumn removes the column and everything that named it. A constraint or
// index over a column that no longer exists cannot be built, so leaving it
// behind produces a schema that fails to load.
func dropColumn(table *sqlmapper.Table, name string) {
	i := columnIndex(table, name)
	if i < 0 {
		return
	}
	table.Columns = append(table.Columns[:i], table.Columns[i+1:]...)

	kept := table.Constraints[:0]
	for _, c := range table.Constraints {
		if !namesColumn(c.Columns, name) {
			kept = append(kept, c)
		}
	}
	table.Constraints = kept

	keptIdx := table.Indexes[:0]
	for _, idx := range table.Indexes {
		if !namesColumn(idx.Columns, name) {
			keptIdx = append(keptIdx, idx)
		}
	}
	table.Indexes = keptIdx
}

// renameColumn moves the name everywhere it is used, not only on the column
// itself: a constraint or index still naming the old one cannot be built.
func renameColumn(table *sqlmapper.Table, from, to string) {
	i := columnIndex(table, from)
	if i < 0 {
		return
	}
	table.Columns[i].Name = to

	for j := range table.Constraints {
		replaceName(table.Constraints[j].Columns, from, to)
	}
	for j := range table.Indexes {
		replaceName(table.Indexes[j].Columns, from, to)
	}
}

// dropNamed removes a constraint or an index by name. The two share a namespace
// closely enough that a dialect's DROP CONSTRAINT, DROP INDEX and DROP FOREIGN
// KEY all arrive here, and only one of them will match.
func dropNamed(table *sqlmapper.Table, name string) {
	kept := table.Constraints[:0]
	for _, c := range table.Constraints {
		if !strings.EqualFold(c.Name, name) {
			kept = append(kept, c)
		}
	}
	table.Constraints = kept

	keptIdx := table.Indexes[:0]
	for _, idx := range table.Indexes {
		if !strings.EqualFold(idx.Name, name) {
			keptIdx = append(keptIdx, idx)
		}
	}
	table.Indexes = keptIdx
}

func namesColumn(columns []string, name string) bool {
	for _, c := range columns {
		if strings.EqualFold(Unquote(c), name) {
			return true
		}
	}
	return false
}

func replaceName(columns []string, from, to string) {
	for i, c := range columns {
		if strings.EqualFold(Unquote(c), from) {
			columns[i] = to
		}
	}
}

// ApplyAll replays every ALTER in a list of statements.
//
// It runs after the CREATE statements have been read rather than interleaved
// with them, which is not the same as replaying the file line by line but is
// the same result for every file that creates a table before altering it. A
// file that created a table, dropped it and created it again would need the
// interleaved form, and no schema dump or migration writes one.
func ApplyAll(schema *sqlmapper.Schema, statements []string, read Reader) {
	// A drop that something later recreates made room; it did not remove
	// anything. Every dump tool writes DROP TABLE IF EXISTS ahead of the CREATE
	// that replaces it, and replaying those after the CREATEs had been read
	// deleted the whole schema.
	created := lastCreated(statements)

	for i, stmt := range statements {
		st, ok := Parse(stmt)
		if !ok {
			continue
		}
		if isDrop(st.Action) && len(st.Names) > 0 && created[strings.ToLower(st.Names[0])] > i {
			continue
		}
		Apply(schema, st, read)
	}
}

func isDrop(a Action) bool {
	switch a {
	case DropTable, DropIndex, DropView, DropSequence, DropType:
		return true
	}
	return false
}

// createRe reads the name off a CREATE of a whole object, which is all that is
// needed to know whether a drop earlier in the file was making room.
//
// What sits between CREATE and the object keyword is not enumerated but skipped:
// OR REPLACE, UNIQUE, NONCLUSTERED, and the ALGORITHM, DEFINER and SQL SECURITY
// a mysqldump view carries, in whatever order the dump tool wrote them. Listing
// them missed the real view in a MySQL dump, whose CREATE and whose VIEW keyword
// sit in different version blocks with the attributes between them.
var createRe = regexp.MustCompile(`(?is)^\s*CREATE\b(?:.{0,300}?)\b(?:TABLE|INDEX|VIEW|SEQUENCE|TYPE)\s+` +
	`(?:IF\s+NOT\s+EXISTS\s+)?([\[\]"` + "`" + `\w.]+)`)

// lastCreated records, for each object name, the last statement that creates
// it. A drop before that is a drop of something that comes back.
func lastCreated(statements []string) map[string]int {
	out := make(map[string]int)
	for i, stmt := range statements {
		if !keyword.HasPrefix(strings.TrimSpace(stmt), "CREATE") {
			continue
		}
		if m := createRe.FindStringSubmatch(stmt); m != nil {
			out[strings.ToLower(unqualify(m[1]))] = i
		}
	}
	return out
}

// applyDrop removes a whole object.
//
// A file that creates a table and then drops it used to keep both, so the
// output declared something the source no longer had, and a foreign key onto
// the dropped table made the whole file fail to load.
func applyDrop(schema *sqlmapper.Schema, action Action, name string) {
	switch action {
	case DropTable:
		kept := schema.Tables[:0]
		for _, t := range schema.Tables {
			if !strings.EqualFold(t.Name, name) {
				kept = append(kept, t)
			}
		}
		schema.Tables = kept

		// A key onto a table that no longer exists cannot be built, so it goes
		// with it rather than being left to fail at load time.
		for i := range schema.Tables {
			constraints := schema.Tables[i].Constraints[:0]
			for _, c := range schema.Tables[i].Constraints {
				if !strings.EqualFold(c.RefTable, name) {
					constraints = append(constraints, c)
				}
			}
			schema.Tables[i].Constraints = constraints
		}

	case DropIndex:
		for i := range schema.Tables {
			indexes := schema.Tables[i].Indexes[:0]
			for _, idx := range schema.Tables[i].Indexes {
				if !strings.EqualFold(idx.Name, name) {
					indexes = append(indexes, idx)
				}
			}
			schema.Tables[i].Indexes = indexes
		}

	case DropView:
		kept := schema.Views[:0]
		for _, v := range schema.Views {
			if !strings.EqualFold(v.Name, name) {
				kept = append(kept, v)
			}
		}
		schema.Views = kept

	case DropSequence:
		kept := schema.Sequences[:0]
		for _, sq := range schema.Sequences {
			if !strings.EqualFold(sq.Name, name) {
				kept = append(kept, sq)
			}
		}
		schema.Sequences = kept

	case DropType:
		kept := schema.Types[:0]
		for _, ty := range schema.Types {
			if !strings.EqualFold(ty.Name, name) {
				kept = append(kept, ty)
			}
		}
		schema.Types = kept
	}
}
