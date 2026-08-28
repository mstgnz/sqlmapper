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
		"sequences": 3, "types": 1, "array columns": 1, "extensions": 1, "permissions": 2, "generated columns": 1,
	},
	"mysql": {
		"tables": 3, "columns": 18, "defaults": 6, "auto increment": 2,
		"table comments": 2, "column comments": 2,
		"primary keys": 3, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 3, "fk on update": 2, "self referencing fk": 1,
		"unique constraints": 2, "composite unique": 1, "check constraints": 2,
		"indexes": 3, "unique indexes": 1, "composite indexes": 2,
		"views": 1, "routines": 2, "procedures": 1, "routine parameters": 2, "triggers": 1,
		"enum columns": 1, "permissions": 2, "generated columns": 1,
	},
	"oracle": {
		"tables": 3, "columns": 17, "defaults": 6, "auto increment": 2,
		"table comments": 1, "column comments": 1,
		"primary keys": 3, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 3, "self referencing fk": 1,
		"unique constraints": 2, "composite unique": 1, "check constraints": 2,
		"indexes": 3, "unique indexes": 1, "composite indexes": 2,
		"views": 1, "routines": 2, "procedures": 1, "routine parameters": 2, "triggers": 1,
		"sequences": 1, "types": 1, "permissions": 2, "generated columns": 1,
	},
	"sqlserver": {
		"tables": 3, "columns": 17, "defaults": 6, "auto increment": 2,
		"table comments": 1, "column comments": 1,
		"primary keys": 3, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 2, "fk on update": 1, "self referencing fk": 1,
		"unique constraints": 2, "composite unique": 1, "check constraints": 2,
		"indexes": 4, "unique indexes": 1, "composite indexes": 1, "partial indexes": 1,
		"views": 1, "routines": 2, "procedures": 1, "routine parameters": 2, "triggers": 1,
		"sequences": 1, "permissions": 2, "generated columns": 1,
	},
	"sqlite": {
		"tables": 3, "columns": 18, "defaults": 6, "auto increment": 2,
		"primary keys": 1, "composite primary keys": 1,
		"foreign keys": 3, "fk on delete": 3, "fk on update": 1,
		"unique constraints": 1, "composite unique": 1,
		"check constraints": 1, "column checks": 2,
		"indexes": 4, "unique indexes": 1, "composite indexes": 2, "partial indexes": 1,
		"views": 1, "triggers": 1, "generated columns": 1,
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
			if col.GeneratedExpression != "" {
				add("generated columns", 1)
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

// A comment is documentation its author wrote. Every parser read one and no
// generator wrote one, so it reached the schema and was dropped again on the
// way out, in every conversion.
func TestCommentsSurviveTheConversion(t *testing.T) {
	targets := []struct {
		name string
		db   func() sqlmapper.Database
	}{
		{"postgres", postgres.NewPostgreSQL},
		{"mysql", mysql.NewMySQL},
		{"oracle", oracle.NewOracle},
		{"sqlserver", sqlserver.NewSQLServer},
		// SQLite has nowhere to put one, so it keeps them as file comments.
		{"sqlite", sqlite.NewSQLite},
	}

	for _, src := range fullSchemas {
		schema, err := src.parser().Parse(loadFullSchema(t, src.file))
		if err != nil {
			t.Fatalf("%s parse: %v", src.name, err)
		}

		var want string
		for _, table := range schema.Tables {
			if table.Comment != "" {
				want = table.Comment
				break
			}
		}
		if want == "" {
			continue // the dialect states no comment this fixture can carry
		}

		for _, target := range targets {
			t.Run(src.name+"_to_"+target.name, func(t *testing.T) {
				out, err := target.db().Generate(schema)
				if err != nil {
					t.Fatalf("generate: %v", err)
				}
				if !strings.Contains(out, want) {
					t.Errorf("the comment %q did not survive", want)
				}
			})
		}
	}
}

// TestGrantsSurviveTheConversion holds the line on access control.
//
// Two dialects read a GRANT into the schema and all five dropped it again on
// the way out, so a converted schema quietly had different access than the one
// it came from. That is worse than a dropped comment: an application missing a
// SELECT grant fails closed, and one keeping a REVOKE it should have lost fails
// open. SQLite has no access control at all and states the grant as a comment,
// which is checked the same way.
func TestGrantsSurviveTheConversion(t *testing.T) {
	targets := []struct {
		name string
		db   func() sqlmapper.Database
	}{
		{"postgres", postgres.NewPostgreSQL},
		{"mysql", mysql.NewMySQL},
		{"oracle", oracle.NewOracle},
		{"sqlserver", sqlserver.NewSQLServer},
		{"sqlite", sqlite.NewSQLite},
	}

	for _, src := range fullSchemas {
		schema, err := src.parser().Parse(loadFullSchema(t, src.file))
		if err != nil {
			t.Fatalf("%s parse: %v", src.name, err)
		}
		if len(schema.Permissions) == 0 {
			continue // SQLite states none of its own
		}

		grantee, _ := sqlmapper.GranteeParts(schema.Permissions[0].Grantee)
		object := sqlmapper.StripSchemaPrefix(schema.Permissions[0].Object)

		for _, target := range targets {
			t.Run(src.name+"_to_"+target.name, func(t *testing.T) {
				out, err := target.db().Generate(schema)
				if err != nil {
					t.Fatalf("generate: %v", err)
				}
				if !strings.Contains(out, grantee) {
					t.Errorf("the grantee %q did not survive", grantee)
				}
				if !strings.Contains(out, object) {
					t.Errorf("the granted object %q did not survive", object)
				}
				// The source's schema qualifier does not survive the hop, and a
				// grant naming public.orders names nothing on any other target.
				for _, line := range strings.Split(out, "\n") {
					upper := strings.ToUpper(strings.TrimSpace(line))
					if !strings.HasPrefix(upper, "GRANT ") && !strings.HasPrefix(upper, "REVOKE ") {
						continue
					}
					for _, qualifier := range []string{" public.", " app.", " dbo."} {
						if strings.Contains(line, qualifier) {
							t.Errorf("a grant kept the source's schema qualifier: %s", line)
						}
					}
				}
			})
		}
	}
}

// TestPartialIndexesAreCarriedOrStated checks the one index feature two targets
// cannot express. PostgreSQL, SQLite and SQL Server all have a filtered index
// and must write the clause; MySQL and Oracle have none and must say so rather
// than widening the index in silence.
func TestPartialIndexesAreCarriedOrStated(t *testing.T) {
	schema := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
		Name: "customers",
		Columns: []sqlmapper.Column{
			{Name: "id", DataType: "int", IsPrimaryKey: true},
			{Name: "email", DataType: "varchar", Length: 255},
			{Name: "is_active", DataType: "boolean"},
		},
		Indexes: []sqlmapper.Index{
			{Name: "ix_active_email", Columns: []string{"email"}, IsUnique: true, Condition: "is_active"},
		},
	}}}

	carries := map[string]func() sqlmapper.Database{
		"postgres":  postgres.NewPostgreSQL,
		"sqlite":    sqlite.NewSQLite,
		"sqlserver": sqlserver.NewSQLServer,
	}
	for name, db := range carries {
		t.Run(name+"_carries_it", func(t *testing.T) {
			out, err := db().Generate(schema)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !strings.Contains(strings.ToUpper(out), "WHERE") {
				t.Errorf("the filter was dropped:\n%s", out)
			}
			if strings.Contains(out, "was partial in the source") {
				t.Errorf("%s can hold the filter and should not be excusing itself:\n%s", name, out)
			}
		})
	}

	states := map[string]func() sqlmapper.Database{
		"mysql":  mysql.NewMySQL,
		"oracle": oracle.NewOracle,
	}
	for name, db := range states {
		t.Run(name+"_states_it", func(t *testing.T) {
			out, err := db().Generate(schema)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			if !strings.Contains(out, "was partial in the source") {
				t.Errorf("%s widened a unique partial index without a word:\n%s", name, out)
			}
		})
	}
}
