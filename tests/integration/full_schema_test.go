package integration

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/oracle"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/sqlite"
	"github.com/mstgnz/sqlmapper/sqlserver"
	"github.com/mstgnz/sqlmapper/stream"
)

// The fixtures under testdata/schemas are one schema written five ways, each
// taken from the server's own dump tool after loading it: pg_dump, mysqldump,
// DBMS_METADATA, sqlite3 .schema, and an SSMS-shaped script for SQL Server,
// which has no dump CLI.
//
// They exist because the earlier fixtures were two tables, one view, one
// foreign key and one index, which touched about a third of what the schema
// model carries. Everything below was missing, and building a fixture that
// reached it turned up eight defects at once: a comma inside a column comment
// split the definition and the tail read as a UNIQUE constraint the table never
// had, a real MySQL function was never found because its return type carries
// parentheses and its characteristics sit before the body, neither MySQL
// comment form was read, Oracle read no COMMENT ON at all and only recognised
// CASCADE among the delete rules, SQL Server missed a procedure whose
// parameters have no parentheses, and SQLite missed a foreign key written on
// the column.
var fullSchemas = []struct {
	name   string
	file   string
	parser func() sqlmapper.Database
	stream func() stream.StreamParser
}{
	{"postgres", "postgres.sql", postgres.NewPostgreSQL,
		func() stream.StreamParser { return postgres.NewPostgreSQLStreamParser() }},
	{"mysql", "mysql.sql", mysql.NewMySQL,
		func() stream.StreamParser { return mysql.NewMySQLStreamParser() }},
	{"oracle", "oracle.sql", oracle.NewOracle,
		func() stream.StreamParser { return oracle.NewOracleStreamParser() }},
	{"sqlserver", "sqlserver.sql", sqlserver.NewSQLServer,
		func() stream.StreamParser { return sqlserver.NewSQLServerStreamParser() }},
	{"sqlite", "sqlite.sql", sqlite.NewSQLite,
		func() stream.StreamParser { return sqlite.NewSQLiteStreamParser() }},
}

func loadFullSchema(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "schemas", file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// want is what each dialect has to yield from its own fixture. A zero means the
// dialect cannot express the feature, which is stated rather than skipped so
// that a dialect gaining it is noticed.
var fullSchemaWant = map[string]map[string]int{
	"postgres": {
		"tables": 3, "columns": 19, "defaults": 6, "auto increment": 2,
		"table comments": 2, "column comments": 2,
		"primary keys": 3, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 3, "fk on update": 2, "self referencing fk": 1,
		"unique constraints": 2, "composite unique": 1, "check constraints": 2,
		"indexes": 4, "unique indexes": 1, "composite indexes": 1, "partial indexes": 1,
		"views": 1, "routines": 3, "procedures": 1, "routine parameters": 2, "triggers": 1,
		"sequences": 3, "types": 1, "array columns": 1, "extensions": 1, "permissions": 2,
	},
	"mysql": {
		"tables": 3, "columns": 18, "defaults": 6, "auto increment": 2,
		"table comments": 2, "column comments": 2,
		"primary keys": 3, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 3, "fk on update": 2, "self referencing fk": 1,
		"unique constraints": 2, "composite unique": 1, "check constraints": 2,
		"indexes": 2, "composite indexes": 1,
		"views": 1, "routines": 2, "procedures": 1, "routine parameters": 2, "triggers": 1,
		"enum columns": 1,
	},
	"oracle": {
		"tables": 3, "columns": 17, "defaults": 6, "auto increment": 2,
		"table comments": 1, "column comments": 1,
		"primary keys": 3, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 3, "self referencing fk": 1,
		"unique constraints": 2, "composite unique": 1, "check constraints": 2,
		"indexes": 3, "unique indexes": 1, "composite indexes": 2,
		"views": 1, "routines": 2, "procedures": 1, "routine parameters": 2, "triggers": 1,
		"sequences": 1,
	},
	"sqlserver": {
		"tables": 3, "columns": 17, "defaults": 6, "auto increment": 2,
		"primary keys": 3, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 2, "fk on update": 1, "self referencing fk": 1,
		"unique constraints": 2, "composite unique": 1, "check constraints": 2,
		"indexes": 3, "unique indexes": 1, "composite indexes": 1,
		"views": 1, "routines": 2, "procedures": 1, "routine parameters": 2, "triggers": 1,
	},
	"sqlite": {
		"tables": 3, "columns": 18, "defaults": 6, "auto increment": 2,
		"primary keys": 1, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 3, "fk on update": 1,
		"unique constraints": 1, "composite unique": 1,
		"check constraints": 1, "column checks": 2,
		"indexes": 4, "unique indexes": 1, "composite indexes": 2, "partial indexes": 1,
		"views": 1, "triggers": 1,
	},
}

// TestFullSchemaCoverage reads each fixture and counts what came out. It fails
// on anything below the recorded figure, so a parser that stops seeing a
// feature is caught even when nothing else notices.
func TestFullSchemaCoverage(t *testing.T) {
	for _, d := range fullSchemas {
		t.Run(d.name, func(t *testing.T) {
			schema, err := d.parser().Parse(loadFullSchema(t, d.file))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			got := schemaFeatures(schema)
			want := fullSchemaWant[d.name]

			var names []string
			for k := range want {
				names = append(names, k)
			}
			sort.Strings(names)

			for _, k := range names {
				if got[k] < want[k] {
					t.Errorf("%-24s got %d, want at least %d", k, got[k], want[k])
				}
			}
		})
	}
}

// TestFullSchemaConverts runs every fixture into every dialect, so a schema
// with all of this in it has to come out of all five generators.
func TestFullSchemaConverts(t *testing.T) {
	targets := map[string]func() sqlmapper.Database{
		"mysql": mysql.NewMySQL, "postgres": postgres.NewPostgreSQL,
		"sqlite": sqlite.NewSQLite, "oracle": oracle.NewOracle, "sqlserver": sqlserver.NewSQLServer,
	}

	for _, d := range fullSchemas {
		schema, err := d.parser().Parse(loadFullSchema(t, d.file))
		if err != nil {
			t.Fatalf("%s parse: %v", d.name, err)
		}

		for name, mk := range targets {
			t.Run(d.name+"_to_"+name, func(t *testing.T) {
				out, err := mk().Generate(schema)
				if err != nil {
					t.Fatalf("generate: %v", err)
				}

				// Every table reaches the other side.
				for _, table := range schema.Tables {
					if !strings.Contains(out, table.Name) {
						t.Errorf("table %q is missing from the output", table.Name)
					}
				}
				// And the output is readable again by the dialect that wrote it.
				if _, err := mk().Parse(out); err != nil {
					t.Errorf("the generator's own output does not parse: %v", err)
				}
			})
		}
	}
}

// TestFullSchemaReadersAgree holds the two readers to the same result on a
// schema that has one of everything, rather than on a fixture of two tables.
func TestFullSchemaReadersAgree(t *testing.T) {
	for _, d := range fullSchemas {
		t.Run(d.name, func(t *testing.T) {
			dump := loadFullSchema(t, d.file)

			fileSchema, err := d.parser().Parse(dump)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			streamSchema := &sqlmapper.Schema{}
			var loose []string
			err = d.stream().ParseStream(strings.NewReader(dump), func(obj stream.SchemaObject) error {
				switch v := obj.Data.(type) {
				case *sqlmapper.Table:
					streamSchema.Tables = append(streamSchema.Tables, *v)
				case *sqlmapper.View:
					streamSchema.Views = replaceView(streamSchema.Views, *v)
				case *sqlmapper.Index:
					if len(streamSchema.Tables) > 0 {
						streamSchema.Tables[0].Indexes = append(streamSchema.Tables[0].Indexes, *v)
					}
				case *sqlmapper.Constraint:
					loose = append(loose, constraintKey(*v))
				}
				return nil
			})
			if err != nil {
				t.Fatalf("stream: %v", err)
			}

			if got, want := schemaShape(streamSchema, loose), schemaShape(fileSchema, nil); got != want {
				t.Errorf("the readers disagree\nstream:\n%s\nfile:\n%s", got, want)
			}
		})
	}
}

// schemaFeatures counts what a parsed schema holds, by feature.
func schemaFeatures(s *sqlmapper.Schema) map[string]int {
	c := map[string]int{}
	add := func(k string, n int) { c[k] += n }

	add("tables", len(s.Tables))
	add("views", len(s.Views))
	add("routines", len(s.Functions)+len(s.Procedures))
	add("procedures", len(s.Procedures))
	add("triggers", len(s.Triggers))
	add("sequences", len(s.Sequences))
	add("types", len(s.Types))
	add("extensions", len(s.Extensions))
	add("permissions", len(s.Permissions))

	for _, f := range s.Functions {
		if f.IsProc {
			add("procedures", 1)
		}
		if len(f.Parameters) > 0 {
			add("routine parameters", 1)
		}
	}

	for _, t := range s.Tables {
		add("columns", len(t.Columns))
		add("indexes", len(t.Indexes))
		if t.Comment != "" {
			add("table comments", 1)
		}
		for _, col := range t.Columns {
			if col.Comment != "" {
				add("column comments", 1)
			}
			if col.DefaultValue != "" {
				add("defaults", 1)
			}
			if col.AutoIncrement {
				add("auto increment", 1)
			}
			if col.IsArray {
				add("array columns", 1)
			}
			if len(col.EnumValues) > 0 {
				add("enum columns", 1)
			}
			if col.CheckExpression != "" {
				add("column checks", 1)
			}
		}
		for _, con := range t.Constraints {
			switch con.Type {
			case "PRIMARY KEY":
				add("primary keys", 1)
				if len(con.Columns) > 1 {
					add("composite primary keys", 1)
				}
			case "FOREIGN KEY":
				add("foreign keys", 1)
				if con.DeleteRule != "" {
					add("fk on delete", 1)
				}
				if con.UpdateRule != "" {
					add("fk on update", 1)
				}
				if con.RefTable == t.Name {
					add("self referencing fk", 1)
				}
			case "UNIQUE":
				add("unique constraints", 1)
				if len(con.Columns) > 1 {
					add("composite unique", 1)
				}
			case "CHECK":
				add("check constraints", 1)
			}
		}
		for _, idx := range t.Indexes {
			if idx.IsUnique {
				add("unique indexes", 1)
			}
			if idx.Condition != "" {
				add("partial indexes", 1)
			}
			if len(idx.Columns) > 1 {
				add("composite indexes", 1)
			}
		}
	}
	return c
}
