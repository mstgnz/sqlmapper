// Command sqlmapper converts a SQL dump file from one database dialect to another.
//
// Usage:
//
//	sqlmapper --file=dump.sql --to=postgres
//	sqlmapper --file=dump.sql --from=mysql --to=postgres --out=result.sql
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
	"strings"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/oracle"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/sqlite"
	"github.com/mstgnz/sqlmapper/sqlserver"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
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
func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sqlmapper", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		// Nothing useful can be done if writing the usage text fails, which is
		// also why flag.PrintDefaults below discards its own error.
		_, _ = fmt.Fprintln(stderr, "Usage: sqlmapper --file=<path> --to=<target_db> [--from=<source_db>] [--out=<path>]")
		_, _ = fmt.Fprintln(stderr, "Example: sqlmapper --file=postgres.sql --to=mysql")
		fs.PrintDefaults()
	}

	filePath := fs.String("file", "", "path to the SQL dump file")
	sourceDB := fs.String("from", "", "source database type; detected from the dump when omitted")
	targetDB := fs.String("to", "", "target database type (mysql, postgres, sqlite, oracle, sqlserver)")
	outPath := fs.String("out", "", "output file; defaults to <input>_<target>.sql next to the input")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *filePath == "" || *targetDB == "" {
		fs.Usage()
		return errUsage
	}

	content, err := os.ReadFile(*filePath)
	if err != nil {
		return fmt.Errorf("cannot read input file: %w", err)
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

	outputPath := *outPath
	if outputPath == "" {
		outputPath = createOutputPath(*filePath, *targetDB)
	}
	if err := os.WriteFile(outputPath, []byte(result), 0644); err != nil {
		return fmt.Errorf("cannot write output file: %w", err)
	}

	_, err = fmt.Fprintf(stdout, "Converted %s (%s) to %s: %s\n", *filePath, sourceType, *targetDB, outputPath)
	return err
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
