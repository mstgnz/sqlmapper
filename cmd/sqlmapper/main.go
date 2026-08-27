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
		os.Exit(1)
	}
}

// run holds everything main does, with its inputs and outputs passed in so the
// conversion can be exercised without spawning a process.
func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sqlmapper", flag.ContinueOnError)
	fs.SetOutput(stderr)

	filePath := fs.String("file", "", "path to the SQL dump file")
	sourceDB := fs.String("from", "", "source database type; detected from the dump when omitted")
	targetDB := fs.String("to", "", "target database type (mysql, postgres, sqlite, oracle, sqlserver)")
	outPath := fs.String("out", "", "output file; defaults to <input>_<target>.sql next to the input")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *filePath == "" || *targetDB == "" {
		fmt.Fprintln(stderr, "Usage: sqlmapper --file=<path> --to=<target_db> [--from=<source_db>] [--out=<path>]")
		fmt.Fprintln(stderr, "Example: sqlmapper --file=postgres.sql --to=mysql")
		fs.PrintDefaults()
		return errors.New("missing required flags")
	}

	content, err := os.ReadFile(*filePath)
	if err != nil {
		fmt.Fprintf(stderr, "cannot read input file: %v\n", err)
		return err
	}

	sourceType := strings.ToLower(strings.TrimSpace(*sourceDB))
	if sourceType == "" {
		sourceType = detectSourceType(string(content))
		if sourceType == "" {
			err := errors.New("could not detect the source database type; pass --from explicitly")
			fmt.Fprintln(stderr, err)
			return err
		}
	}

	sourceParser := createParser(sourceType)
	if sourceParser == nil {
		err := fmt.Errorf("unsupported source database type: %s", sourceType)
		fmt.Fprintln(stderr, err)
		return err
	}

	targetParser := createParser(*targetDB)
	if targetParser == nil {
		err := fmt.Errorf("unsupported target database type: %s", *targetDB)
		fmt.Fprintln(stderr, err)
		return err
	}

	schema, err := sourceParser.Parse(string(content))
	if err != nil {
		fmt.Fprintf(stderr, "parse error: %v\n", err)
		return err
	}

	result, err := targetParser.Generate(schema)
	if err != nil {
		fmt.Fprintf(stderr, "generate error: %v\n", err)
		return err
	}

	outputPath := *outPath
	if outputPath == "" {
		outputPath = createOutputPath(*filePath, *targetDB)
	}
	if err := os.WriteFile(outputPath, []byte(result), 0644); err != nil {
		fmt.Fprintf(stderr, "cannot write output file: %v\n", err)
		return err
	}

	fmt.Fprintf(stdout, "Converted %s (%s) to %s: %s\n", *filePath, sourceType, *targetDB, outputPath)
	return nil
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
