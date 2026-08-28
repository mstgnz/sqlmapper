// Command sqlmapper converts a SQL dump from one database dialect to another.
//
// Usage:
//
//	sqlmapper --file=dump.sql --to=postgres
//	sqlmapper --file=dump.sql --from=mysql --to=postgres --out=result.sql
//	mysqldump app | sqlmapper --from=mysql --to=postgres > app.pg.sql
//
// It reads standard input when --file is omitted and writes standard output
// when there is no file to derive an output name from, so it works in a pipe.
//
// The source dialect is detected from the dump when --from is omitted. Detection
// is a heuristic; pass --from explicitly when the dump is small or unusual.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/oracle"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/sqlite"
	"github.com/mstgnz/sqlmapper/sqlserver"
)

// version is stamped in at build time with
// -ldflags="-X main.version=v1.2.3". Left unset it falls back to the module
// version the binary was built from, which is what go install records.
var version string

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, errUsage) && !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

// errUsage marks the case where the usage text has already been written, so
// main does not print a second message on top of it.
var errUsage = errors.New("missing required flags")

// run holds everything main does, with its inputs and outputs passed in so the
// conversion can be exercised without spawning a process.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sqlmapper", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		// Nothing useful can be done if writing the usage text fails, which is
		// also why flag.PrintDefaults below discards its own error.
		_, _ = fmt.Fprintln(stderr, "Usage: sqlmapper --to=<target_db> [--file=<path>] [--from=<source_db>] [--out=<path>]")
		_, _ = fmt.Fprintln(stderr, "Reads standard input when --file is omitted, and writes standard output when --out is \"-\".")
		_, _ = fmt.Fprintln(stderr, "Example: sqlmapper --file=postgres.sql --to=mysql")
		_, _ = fmt.Fprintln(stderr, "Example: mysqldump app | sqlmapper --from=mysql --to=postgres > app.pg.sql")
		fs.PrintDefaults()
	}

	filePath := fs.String("file", "", "path to the SQL dump file; \"-\" or omitted reads standard input")
	sourceDB := fs.String("from", "", "source database type; detected from the dump when omitted")
	targetDB := fs.String("to", "", "target database type (mysql, postgres, sqlite, oracle, sqlserver)")
	outPath := fs.String("out", "", "output file; \"-\" writes standard output. Defaults to <input>_<target>.sql, or standard output when reading standard input")
	showVersion := fs.Bool("version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		_, err := fmt.Fprintln(stdout, buildVersion())
		return err
	}

	if *targetDB == "" {
		fs.Usage()
		return errUsage
	}

	content, err := readInput(*filePath, stdin)
	if err != nil {
		return err
	}

	sourceType := strings.ToLower(strings.TrimSpace(*sourceDB))
	if sourceType == "" {
		sourceType = detectSourceType(string(content))
		if sourceType == "" {
			return errors.New("could not detect the source database type; pass --from explicitly")
		}
	}

	sourceParser := createParser(sourceType)
	if sourceParser == nil {
		return fmt.Errorf("unsupported source database type: %s", sourceType)
	}

	targetParser := createParser(*targetDB)
	if targetParser == nil {
		return fmt.Errorf("unsupported target database type: %s", *targetDB)
	}

	schema, err := sourceParser.Parse(string(content))
	if err != nil {
		return fmt.Errorf("parse error: %w", err)
	}

	result, err := targetParser.Generate(schema)
	if err != nil {
		return fmt.Errorf("generate error: %w", err)
	}

	outputPath := outputTarget(*outPath, *filePath, *targetDB)
	if outputPath == "" {
		_, err := io.WriteString(stdout, result)
		return err
	}

	if err := os.WriteFile(outputPath, []byte(result), 0644); err != nil {
		return fmt.Errorf("cannot write output file: %w", err)
	}

	// The summary goes to standard error so that standard output carries the
	// converted SQL and nothing else when the command is used in a pipe.
	_, err = fmt.Fprintf(stderr, "Converted %s (%s) to %s: %s\n", inputName(*filePath), sourceType, *targetDB, outputPath)
	return err
}

// readInput returns the dump to convert. An empty path or "-" means standard
// input, which is how the command is used in a pipe.
func readInput(path string, stdin io.Reader) ([]byte, error) {
	if isStdio(path) {
		content, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("cannot read standard input: %w", err)
		}
		if len(content) == 0 {
			return nil, errors.New("no input: pass --file or pipe a dump into the command")
		}
		return content, nil
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read input file: %w", err)
	}
	return content, nil
}

// outputTarget returns the file to write, or an empty string for standard
// output. Standard output is the answer when it was asked for with "-", and
// when the input came from a pipe and so has no name to derive one from.
func outputTarget(outPath, filePath, targetDB string) string {
	if outPath != "" {
		if isStdio(outPath) {
			return ""
		}
		return outPath
	}
	if isStdio(filePath) {
		return ""
	}
	return createOutputPath(filePath, targetDB)
}

func isStdio(path string) bool {
	return path == "" || path == "-"
}

func inputName(path string) string {
	if isStdio(path) {
		return "standard input"
	}
	return path
}

// buildVersion reports the version stamped in at build time, falling back to
// what the toolchain recorded in the binary. A binary built from a checkout
// rather than installed from the module proxy has no module version, so the
// commit it was built from is reported instead of nothing.
func buildVersion() string {
	if version != "" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return "devel-" + shortRevision(setting.Value)
		}
	}
	return "unknown"
}

func shortRevision(rev string) string {
	const short = 12
	if len(rev) > short {
		return rev[:short]
	}
	return rev
}

// dialectMarkers lists the substrings that identify each dialect in a dump.
// Detection scores every dialect and picks the highest, so a marker that also
// appears in another dialect's dumps does not decide the answer on its own.
var dialectMarkers = []struct {
	dialect string
	markers []string
}{
	{"mysql", []string{
		"ENGINE=", "AUTO_INCREMENT", "/*!40", "UNLOCK TABLES", "DELIMITER ;;",
		"utf8mb4", "LONGTEXT", "TINYINT(",
	}},
	{"postgres", []string{
		"POSTGRESQL DATABASE DUMP", "::REGCLASS", "OWNER TO", "SET SEARCH_PATH",
		"NEXTVAL(", "SERIAL", "BYTEA", "WITHOUT TIME ZONE", "CHARACTER VARYING",
		"JSONB", "CREATE EXTENSION",
	}},
	{"sqlserver", []string{
		"IDENTITY(", "NVARCHAR", "[DBO]", "UNIQUEIDENTIFIER", "NEWID()",
		"DATETIME2", "IDENTITY_INSERT",
	}},
	{"oracle", []string{
		"VARCHAR2", "NUMBER(", "CREATE OR REPLACE PACKAGE", "NOCACHE",
		"TABLESPACE ", "CLOB", "SYSDATE",
	}},
	{"sqlite", []string{
		"AUTOINCREMENT", "PRAGMA ", "WITHOUT ROWID",
	}},
}

// detectSourceType guesses the dialect a dump was produced by. It returns an
// empty string when nothing matches, which the caller reports rather than
// guessing on the user's behalf.
func detectSourceType(content string) string {
	upper := strings.ToUpper(content)

	best := ""
	bestScore := 0
	for _, entry := range dialectMarkers {
		score := 0
		for _, marker := range entry.markers {
			if strings.Contains(upper, marker) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = entry.dialect, score
		}
	}
	return best
}

func createParser(dbType string) sqlmapper.Parser {
	switch strings.ToLower(dbType) {
	case "mysql", "mariadb":
		return mysql.NewMySQL()
	case "postgres", "postgresql", "pgsql":
		return postgres.NewPostgreSQL()
	case "sqlite", "sqlite3":
		return sqlite.NewSQLite()
	case "oracle":
		return oracle.NewOracle()
	case "sqlserver", "mssql":
		return sqlserver.NewSQLServer()
	default:
		return nil
	}
}

func createOutputPath(inputPath, targetDB string) string {
	dir := filepath.Dir(inputPath)
	filename := filepath.Base(inputPath)
	ext := filepath.Ext(filename)
	name := strings.TrimSuffix(filename, ext)
	return filepath.Join(dir, fmt.Sprintf("%s_%s%s", name, targetDB, ext))
}
