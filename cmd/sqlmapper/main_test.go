package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectSourceType(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "mysql",
			content: "CREATE TABLE test (id INT) ENGINE=INNODB;",
			want:    "mysql",
		},
		{
			name:    "sqlite",
			content: "CREATE TABLE test (id INTEGER AUTOINCREMENT);",
			want:    "sqlite",
		},
		{
			name:    "sqlserver",
			content: "CREATE TABLE test (id INT IDENTITY(1,1));",
			want:    "sqlserver",
		},
		{
			name:    "postgres",
			content: "CREATE TABLE test (id SERIAL PRIMARY KEY);",
			want:    "postgres",
		},
		{
			name:    "oracle",
			content: "CREATE TABLE test (id NUMBER(10));",
			want:    "oracle",
		},
		{
			name:    "unknown dialect",
			content: "CREATE TABLE test (id INT);",
			want:    "",
		},
		{
			// A modern pg_dump never writes the word SERIAL: the column is a plain
			// bigint wired to a sequence afterwards. Detection has to survive that.
			name: "real pg_dump without SERIAL",
			content: `--
-- PostgreSQL database dump
--
SET statement_timeout = 0;
SELECT pg_catalog.set_config('search_path', '', false);

CREATE TABLE public.customers (
    id bigint NOT NULL,
    email character varying(255) NOT NULL
);
ALTER TABLE public.customers OWNER TO postgres;
ALTER TABLE ONLY public.customers ALTER COLUMN id SET DEFAULT nextval('public.customers_id_seq'::regclass);`,
			want: "postgres",
		},
		{
			name: "real mysqldump header",
			content: "-- MySQL dump 10.13  Distrib 8.0.35\n" +
				"/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;\n" +
				"CREATE TABLE `users` (\n" +
				"  `id` bigint unsigned NOT NULL AUTO_INCREMENT,\n" +
				"  PRIMARY KEY (`id`)\n" +
				") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n" +
				"UNLOCK TABLES;",
			want: "mysql",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectSourceType(tt.content); got != tt.want {
				t.Errorf("detectSourceType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateOutputPath(t *testing.T) {
	tests := []struct {
		name      string
		inputPath string
		targetDB  string
		want      string
	}{
		{
			name:      "bare filename",
			inputPath: "test.sql",
			targetDB:  "mysql",
			want:      "test_mysql.sql",
		},
		{
			name:      "path with directory",
			inputPath: "/path/to/test.sql",
			targetDB:  "postgres",
			want:      filepath.Join("/path/to", "test_postgres.sql"),
		},
		{
			name:      "different extension",
			inputPath: "dump.txt",
			targetDB:  "sqlite",
			want:      "dump_sqlite.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := createOutputPath(tt.inputPath, tt.targetDB); got != tt.want {
				t.Errorf("createOutputPath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCreateParser(t *testing.T) {
	tests := []struct {
		name    string
		dbType  string
		wantNil bool
	}{
		{name: "mysql", dbType: "mysql", wantNil: false},
		{name: "mariadb alias", dbType: "mariadb", wantNil: false},
		{name: "postgres", dbType: "postgres", wantNil: false},
		{name: "postgresql alias", dbType: "postgresql", wantNil: false},
		{name: "sqlite", dbType: "sqlite", wantNil: false},
		{name: "oracle", dbType: "oracle", wantNil: false},
		{name: "sqlserver", dbType: "sqlserver", wantNil: false},
		{name: "mssql alias", dbType: "mssql", wantNil: false},
		{name: "unknown dialect", dbType: "unknown", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := createParser(tt.dbType)
			if (got == nil) != tt.wantNil {
				t.Errorf("createParser() returned nil: %v, want nil: %v", got == nil, tt.wantNil)
			}
		})
	}
}

func TestIntegration(t *testing.T) {
	testSQL := `
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(255) UNIQUE
);
`
	tmpDir := t.TempDir()
	inputPath := filepath.Join(tmpDir, "test.sql")
	if err := os.WriteFile(inputPath, []byte(testSQL), 0644); err != nil {
		t.Fatalf("could not create the test file: %v", err)
	}

	tests := []struct {
		name     string
		targetDB string
		wantErr  bool
	}{
		{name: "convert to mysql", targetDB: "mysql", wantErr: false},
		{name: "convert to sqlite", targetDB: "sqlite", wantErr: false},
		{name: "invalid target", targetDB: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatalf("could not read the test file: %v", err)
			}

			sourceType := detectSourceType(string(content))
			if sourceType != "postgres" {
				t.Errorf("expected source type postgres, got %s", sourceType)
			}

			sourceParser := createParser(sourceType)
			targetParser := createParser(tt.targetDB)

			if tt.wantErr {
				if targetParser != nil {
					t.Errorf("expected a nil parser for an invalid target")
				}
				return
			}

			schema, err := sourceParser.Parse(string(content))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}

			result, err := targetParser.Generate(schema)
			if err != nil {
				t.Fatalf("generate failed: %v", err)
			}

			outputPath := createOutputPath(inputPath, tt.targetDB)
			if err := os.WriteFile(outputPath, []byte(result), 0644); err != nil {
				t.Fatalf("could not write the output file: %v", err)
			}

			if _, err := os.Stat(outputPath); os.IsNotExist(err) {
				t.Errorf("output file was not created: %s", outputPath)
			}
		})
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "dump.sql")
	const mysqlDump = "CREATE TABLE `users` (\n" +
		"  `id` int NOT NULL AUTO_INCREMENT,\n" +
		"  `email` varchar(255) NOT NULL,\n" +
		"  PRIMARY KEY (`id`)\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;\n"
	if err := os.WriteFile(inputPath, []byte(mysqlDump), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("detects the source and writes the default output path", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		err := run([]string{"--file=" + inputPath, "--to=postgres"}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("run() = %v, stderr: %s", err, stderr.String())
		}

		outPath := filepath.Join(dir, "dump_postgres.sql")
		got, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("expected output at %s: %v", outPath, err)
		}
		if !strings.Contains(string(got), "id SERIAL PRIMARY KEY") {
			t.Errorf("converted SQL missing the serial column:\n%s", got)
		}
		if !strings.Contains(stdout.String(), "(mysql) to postgres") {
			t.Errorf("stdout did not name the detected dialect: %q", stdout.String())
		}
	})

	t.Run("honours an explicit source and output path", func(t *testing.T) {
		outPath := filepath.Join(dir, "explicit.sql")
		var stdout, stderr bytes.Buffer
		err := run([]string{"--file=" + inputPath, "--from=mysql", "--to=sqlite", "--out=" + outPath}, &stdout, &stderr)
		if err != nil {
			t.Fatalf("run() = %v, stderr: %s", err, stderr.String())
		}
		if _, err := os.Stat(outPath); err != nil {
			t.Errorf("expected output at %s: %v", outPath, err)
		}
	})
}

func TestRunErrors(t *testing.T) {
	dir := t.TempDir()

	undetectable := filepath.Join(dir, "plain.sql")
	if err := os.WriteFile(undetectable, []byte("CREATE TABLE t (id INT);"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no flags", nil, "Usage:"},
		{"missing target", []string{"--file=" + undetectable}, "Usage:"},
		{"missing file", []string{"--to=mysql"}, "Usage:"},
		{"unreadable file", []string{"--file=" + filepath.Join(dir, "nope.sql"), "--to=mysql"}, "cannot read input file"},
		{"undetectable dialect", []string{"--file=" + undetectable, "--to=mysql"}, "could not detect"},
		{"unknown source", []string{"--file=" + undetectable, "--from=db2", "--to=mysql"}, "unsupported source"},
		{"unknown target", []string{"--file=" + undetectable, "--from=mysql", "--to=db2"}, "unsupported target"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(tt.args, &stdout, &stderr)
			if err == nil {
				t.Fatalf("expected an error, stderr: %s", stderr.String())
			}
			// Usage goes to stderr because the flag set writes it; everything
			// else is returned so main can print it exactly once.
			got := err.Error()
			if tt.want == "Usage:" {
				got = stderr.String()
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("got %q, want it to contain %q", got, tt.want)
			}
		})
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--nonsense"}, &stdout, &stderr); err == nil {
		t.Error("expected an error for an unknown flag")
	}
}
