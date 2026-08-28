package integration

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/oracle"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/sqlite"
	"github.com/mstgnz/sqlmapper/sqlserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// targets lists every generator, so a rule about routines is checked against
// all of them rather than the one that happened to be edited.
var targets = []struct {
	name string
	db   func() sqlmapper.Database
}{
	{"mysql", mysql.NewMySQL},
	{"postgres", postgres.NewPostgreSQL},
	{"sqlite", sqlite.NewSQLite},
	{"oracle", oracle.NewOracle},
	{"sqlserver", sqlserver.NewSQLServer},
}

const mysqlWithTrigger = "CREATE TABLE `users` (\n" +
	"  `id` int NOT NULL AUTO_INCREMENT,\n" +
	"  `n` int,\n" +
	"  PRIMARY KEY (`id`)\n" +
	");\n" +
	"DELIMITER ;;\n" +
	"CREATE TRIGGER `bump` BEFORE INSERT ON `users` FOR EACH ROW BEGIN\n" +
	"  SET NEW.n = 1;\n" +
	"  SET NEW.n = NEW.n + 1;\n" +
	"END ;;\n" +
	"DELIMITER ;\n"

// A routine is never dropped without saying so. The file generators used to
// discard every trigger and function silently, so a schema converted through
// the CLI lost them with nothing in the output to show for it.
func TestRoutineIsNeverDroppedSilently(t *testing.T) {
	schema, err := mysql.NewMySQL().Parse(mysqlWithTrigger)
	require.NoError(t, err)
	require.Len(t, schema.Triggers, 1)

	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			out, err := target.db().Generate(schema)
			require.NoError(t, err)
			assert.Contains(t, out, "bump", "the trigger has to appear in the output somewhere")
		})
	}
}

// A body that came from another database is commented out, because it is
// procedural code that this library does not translate. Writing it as it stands
// produces a file that fails to load at that statement.
func TestForeignRoutineIsCommentedOut(t *testing.T) {
	schema, err := mysql.NewMySQL().Parse(mysqlWithTrigger)
	require.NoError(t, err)

	for _, target := range targets {
		if target.name == "mysql" {
			continue // native, covered below
		}
		t.Run(target.name, func(t *testing.T) {
			out, err := target.db().Generate(schema)
			require.NoError(t, err)

			assert.Contains(t, out, "Defined by the mysql source")

			// Every line of the routine has to stay commented, or the loader
			// reads the body as SQL.
			var sawTrigger bool
			for _, line := range strings.Split(out, "\n") {
				if !strings.Contains(line, "CREATE TRIGGER") && !strings.Contains(line, "SET NEW.n") {
					continue
				}
				sawTrigger = true
				assert.True(t, strings.HasPrefix(strings.TrimSpace(line), "--"),
					"uncommented line from the foreign routine: %q", line)
			}
			assert.True(t, sawTrigger, "the routine has to appear at all")
		})
	}
}

// Converting MySQL to MySQL keeps the trigger executable: the body's semicolons
// are protected by a DELIMITER block, FOR EACH ROW is present, and the BEGIN and
// END the parser stripped are put back.
func TestNativeRoutineStaysExecutable(t *testing.T) {
	schema, err := mysql.NewMySQL().Parse(mysqlWithTrigger)
	require.NoError(t, err)

	out, err := mysql.NewMySQL().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "DELIMITER ;;")
	assert.Contains(t, out, "DELIMITER ;")
	assert.Contains(t, out, "FOR EACH ROW")
	assert.Contains(t, out, "BEGIN")
	assert.Contains(t, out, "END ;;")
	assert.NotContains(t, out, "-- CREATE TRIGGER", "a native routine is not commented out")
}

// A PostgreSQL trigger names the function it runs; it does not carry a body.
// Rendering one into the field a procedural body would occupy produced
// "EXECUTE FUNCTION BEGIN".
func TestPostgreSQLTriggerRunsAFunction(t *testing.T) {
	const ddl = `CREATE TABLE users (id bigint PRIMARY KEY, updated_at timestamp);
CREATE FUNCTION touch() RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER touch_users BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION touch();`

	schema, err := postgres.NewPostgreSQL().Parse(ddl)
	require.NoError(t, err)

	out, err := postgres.NewPostgreSQL().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "EXECUTE FUNCTION touch()")
	assert.NotContains(t, out, "EXECUTE FUNCTION BEGIN")
	assert.Contains(t, out, "LANGUAGE plpgsql")
	assert.NotContains(t, out, "LANGUAGE ;", "an empty language is not valid SQL")
}

// Generate and GenerateStream render routines with the same code, because they
// used to disagree: one dropped them and the other wrote a broken form.
func TestFileAndStreamOutputAgreeOnRoutines(t *testing.T) {
	schema, err := mysql.NewMySQL().Parse(mysqlWithTrigger)
	require.NoError(t, err)

	fileOut, err := mysql.NewMySQL().Generate(schema)
	require.NoError(t, err)

	var streamOut strings.Builder
	require.NoError(t, mysql.NewMySQLStreamParser().GenerateStream(schema, &streamOut))

	for _, want := range []string{"DELIMITER ;;", "CREATE TRIGGER bump", "FOR EACH ROW", "END ;;"} {
		assert.Contains(t, fileOut, want, "file output")
		assert.Contains(t, streamOut.String(), want, "stream output")
	}
}

// A statement never ends in two terminators. Some generateTableSQL
// implementations already appended the semicolon and every stream call site
// appended another.
func TestNoDoubledTerminator(t *testing.T) {
	schema := &sqlmapper.Schema{Tables: []sqlmapper.Table{{
		Name:    "users",
		Columns: []sqlmapper.Column{{Name: "id", DataType: "int"}},
	}}}

	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			out, err := target.db().Generate(schema)
			require.NoError(t, err)
			assert.NotContains(t, out, ");;")
		})
	}

	streams := map[string]func(*sqlmapper.Schema, *strings.Builder) error{
		"mysql": func(s *sqlmapper.Schema, w *strings.Builder) error {
			return mysql.NewMySQLStreamParser().GenerateStream(s, w)
		},
		"postgres": func(s *sqlmapper.Schema, w *strings.Builder) error {
			return postgres.NewPostgreSQLStreamParser().GenerateStream(s, w)
		},
		"sqlite": func(s *sqlmapper.Schema, w *strings.Builder) error {
			return sqlite.NewSQLiteStreamParser().GenerateStream(s, w)
		},
		"oracle": func(s *sqlmapper.Schema, w *strings.Builder) error {
			return oracle.NewOracleStreamParser().GenerateStream(s, w)
		},
		"sqlserver": func(s *sqlmapper.Schema, w *strings.Builder) error {
			return sqlserver.NewSQLServerStreamParser().GenerateStream(s, w)
		},
	}

	for name, gen := range streams {
		t.Run("stream/"+name, func(t *testing.T) {
			var sb strings.Builder
			require.NoError(t, gen(schema, &sb))
			assert.NotContains(t, sb.String(), ");;")
		})
	}
}
