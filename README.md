# SQLMapper

[![CI](https://github.com/mstgnz/sqlmapper/actions/workflows/ci.yml/badge.svg)](https://github.com/mstgnz/sqlmapper/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mstgnz/sqlmapper.svg)](https://pkg.go.dev/github.com/mstgnz/sqlmapper)
[![Go Report Card](https://goreportcard.com/badge/github.com/mstgnz/sqlmapper)](https://goreportcard.com/report/github.com/mstgnz/sqlmapper)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

Convert a SQL schema from one database dialect to another, as a Go library or a CLI.

SQLMapper reads a dump file, parses the DDL into a dialect-neutral schema, and writes that schema back out as SQL for a different database. It never connects to a database, which makes it usable in CI, in a build step, or on a dump someone emailed you. No runtime dependencies: the only module requirement is testify, and that is test-only.

```bash
go install github.com/mstgnz/sqlmapper/cmd/sqlmapper@latest
sqlmapper --file=dump.sql --to=postgres
```

## Why another one

`pgloader`, `ora2pg` and `pg_chameleon` are mature and free, but they all move data into a live PostgreSQL and need a connection to the source. SQLMapper does something narrower: file in, file out, any supported dialect to any other. The closest tool in spirit is [sqlglot](https://github.com/tobymao/sqlglot), which is Python; this is the Go answer to the same problem, scoped to DDL.

If you need to migrate data, not just schema, use pgloader or ora2pg. This tool complements them; it does not replace them.

## Install

As a library:

```bash
go get github.com/mstgnz/sqlmapper
```

As a CLI, whichever suits the machine:

```bash
# With Go
go install github.com/mstgnz/sqlmapper/cmd/sqlmapper@latest

# Or a prebuilt binary, no toolchain required on the server
curl -fsSL -o /usr/local/bin/sqlmapper \
  https://github.com/mstgnz/sqlmapper/releases/latest/download/sqlmapper-linux-amd64
chmod +x /usr/local/bin/sqlmapper

# Or the image
docker run --rm -v "$PWD:/work" mstgnz/sqlmapper --file=dump.sql --to=postgres
```

Release assets are named `sqlmapper-<os>-<arch>` and cover Linux, macOS and Windows on amd64 and arm64.

## Usage

### CLI

```bash
# Source dialect detected from the dump, output beside the input
sqlmapper --file=dump.sql --to=postgres

# Or state it explicitly
sqlmapper --file=dump.sql --from=mysql --to=postgres --out=schema.pg.sql

# In a pipe: standard input to standard output
mysqldump app | sqlmapper --from=mysql --to=postgres > app.pg.sql

# Straight into the target database
mysqldump --no-data app | sqlmapper --from=mysql --to=postgres | psql app
```

Standard output carries the converted SQL and nothing else; the summary line goes to standard error, and only when the result was written to a file.

Supported values for `--from` and `--to`: `mysql`, `postgres`, `sqlite`, `oracle`, `sqlserver`. Full flag reference and more recipes are in [docs/cli.md](docs/cli.md).

### Library

```go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/postgres"
)

func main() {
	dump, err := os.ReadFile("mysql_dump.sql")
	if err != nil {
		log.Fatal(err)
	}

	// Parse the MySQL dump into a dialect-neutral schema.
	schema, err := mysql.NewMySQL().Parse(string(dump))
	if err != nil {
		log.Fatal(err)
	}

	// Render that schema as PostgreSQL.
	pgSQL, err := postgres.NewPostgreSQL().Generate(schema)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(pgSQL)
}
```

Every dialect package exposes the same two methods, so any pair works the same way:

| Dialect    | Constructor                |
| ---------- | -------------------------- |
| MySQL      | `mysql.NewMySQL()`         |
| PostgreSQL | `postgres.NewPostgreSQL()` |
| SQLite     | `sqlite.NewSQLite()`       |
| Oracle     | `oracle.NewOracle()`       |
| SQL Server | `sqlserver.NewSQLServer()` |

### Streaming large dumps

For dumps too large to hold in memory:

```go
parser := mysql.NewMySQLStreamParser()
err := parser.ParseStream(file, func(obj stream.SchemaObject) error {
	fmt.Printf("%v\n", obj.Type)
	return nil
})
```

## What is converted

- Tables, columns, and data types
- Primary keys, foreign keys with ON DELETE / ON UPDATE rules
- Unique and check constraints
- Indexes, including unique indexes
- Views

Type mapping is dialect-aware, not a lookup table with a default. Some cases it handles:

| Source                               | Target     | Result                    |
| ------------------------------------ | ---------- | ------------------------- |
| PostgreSQL `character varying(255)`  | MySQL      | `VARCHAR(255)`            |
| PostgreSQL `text[]`                  | MySQL      | `JSON`                    |
| PostgreSQL `bigint` + `nextval(...)` | MySQL      | `BIGINT AUTO_INCREMENT`   |
| PostgreSQL `boolean DEFAULT true`    | MySQL      | `TINYINT(1) DEFAULT 1`    |
| MySQL `ENUM('a','b')`                | PostgreSQL | `CREATE TYPE ... AS ENUM` |
| MySQL `bigint AUTO_INCREMENT`        | PostgreSQL | `BIGSERIAL`               |
| MySQL `json`                         | PostgreSQL | `JSONB`                   |

## What is not converted

Be aware of these before you trust the output:

- **Function, procedure and trigger bodies.** They are procedural code, not DDL, and PL/pgSQL and MySQL's `BEGIN ... END` are different languages, so a body is never rewritten. Converting a database to itself keeps its routines executable; converting to a different one writes each routine out commented, with a note saying where it came from. The output still loads, and nothing is lost silently.
- **Most of a view body.** The `CREATE VIEW` wrapper and the `WHERE` clause are converted; the select list, the joins and anything else are copied verbatim. A view that calls dialect-specific functions outside the `WHERE` clause will need editing.
- **Table data.** `INSERT` statements and `COPY` blocks are skipped. This is a schema tool.
- **Storage and tuning clauses.** Tablespaces, storage parameters, partitioning and engine options survive only where the target has an equivalent.

The parsers are regular-expression based rather than a full SQL grammar. They are built against the output of `mysqldump` and `pg_dump`, but unusual syntax can still slip through. **Read the generated SQL before running it on anything you care about.**

A parser may reject anything it cannot read, but it never panics: this is a library, and a panic takes the caller's process down with it. `FuzzParseNeverPanics` holds that line, and `make fuzz` keeps looking for a way past it.

## Dialect and version coverage

Every cell below was verified by taking a schema out of the source server with
its own tool (`pg_dump`, `mysqldump`, `mariadb-dump`, `DBMS_METADATA.GET_DDL`,
an SSMS-shaped script both SQL Server versions accept), converting it, and
loading the result into the target server.

| Source \ Target | PG 13 | PG 17 | MySQL 5.7 | MySQL 8.4 | MariaDB 11 | Oracle 21 | Oracle 23ai | MSSQL 2019 | MSSQL 2022 | SQLite 3 |
| --------------- | ----- | ----- | --------- | --------- | ---------- | --------- | ----------- | ---------- | ---------- | -------- |
| PostgreSQL 13   | -     | pass  | pass      | pass      | pass       | pass      | pass        | pass       | pass       | pass     |
| PostgreSQL 17   | pass  | -     | pass      | pass      | pass       | pass      | pass        | pass       | pass       | pass     |
| MySQL 5.7       | pass  | pass  | -         | pass      | pass       | pass      | pass        | pass       | pass       | pass     |
| MySQL 8.4       | pass  | pass  | pass      | -         | pass       | pass      | pass        | pass       | pass       | pass     |
| MariaDB 11      | pass  | pass  | pass      | pass      | -          | pass      | pass        | pass       | pass       | pass     |
| Oracle 21 XE    | pass  | pass  | pass      | pass      | pass       | -         | pass        | pass       | pass       | pass     |
| Oracle 23ai     | pass  | pass  | pass      | pass      | pass       | pass      | -           | pass       | pass       | pass     |
| SQL Server[^s]  | pass  | pass  | pass      | pass      | pass       | pass      | pass        | pass       | pass       | pass     |
| SQLite 3[^l]    | pass  | pass  | pass      | pass      | pass       | pass      | pass        | pass       | pass       | -        |

[^s]:
    SQL Server has no dump CLI in the box. The source script is in the shape
    SSMS "Generate Scripts" produces, and both 2019 and 2022 accept it verbatim,
    which is what makes it a real fixture rather than a convenient one.

[^l]:
    The SQLite fixture is `sqlite3 .schema` output, including the
    `sqlite_sequence` table SQLite writes for itself. Its row was verified
    against the newer release in each target family; the SQL a SQLite source
    produces carries no version-specific syntax.

Version differences the converter has to handle, all of them found by running
this matrix rather than by reading documentation:

- `pg_dump` stopped writing `SERIAL` long ago; a column is a plain `bigint` wired
  to a sequence in a trailing `ALTER TABLE`.
- `mysqldump` emits tables alphabetically, so a child table routinely precedes
  its parent and the foreign key cannot resolve.
- MariaDB has no JSON type: it stores JSON in a `LONGTEXT` guarded by
  `CHECK (json_valid(col))`, a function no other dialect has.
- `DBMS_METADATA.GET_DDL` quotes and schema-qualifies every identifier, folds
  names to upper case, spells identity columns out over a full line of options,
  and appends `ENABLE` and `USING INDEX` to constraints.
- SSMS brackets every identifier, attaches a `WITH (...)` option block and an
  `ON [PRIMARY]` filegroup to each key and index, writes `IDENTITY(1,1)` with a
  comma inside the column definition, and states defaults, foreign keys and
  checks as separate `ALTER TABLE` statements.
- A PostgreSQL view body says `WHERE is_active`, which Oracle 21 and SQL Server
  reject because they have no boolean type. Expressions are parsed rather than
  copied, so that becomes `WHERE is_active <> 0` on the way out.
- `mysqldump` wraps its routines, and the real body of every view, in
  `/*!50003 ... */` version blocks. Those are not comments: MySQL runs what is
  inside them. It also writes each view twice, a stand-in of `SELECT 1 AS col`
  first so that anything referring to it can be created, then the real
  definition at the end, and the last one is the one that counts.
- SQLite has no length to give: its `TEXT` is unbounded. No other database will
  index that. MySQL asks for a prefix length, SQL Server refuses
  `NVARCHAR(MAX)` as a key column, and Oracle cannot index a `CLOB` at all, so a
  column a key touches is bounded on the way out. It also has no boolean and no
  date type, so `WHERE is_active` on an integer column becomes
  `WHERE is_active <> 0` for PostgreSQL, and a text column defaulting to
  `CURRENT_TIMESTAMP` becomes `TO_CHAR(SYSTIMESTAMP)` for Oracle, which will not
  assign a timestamp to a character column.
- `pg_dump` puts a function's attributes between `RETURNS` and the body,
  `CREATE FUNCTION f() RETURNS trigger LANGUAGE plpgsql AS $$ ... $$`, where
  hand-written SQL usually puts `LANGUAGE` after it.

- `DBMS_METADATA` writes `EDITIONABLE` between `REPLACE` and the object keyword,
  leaves a constraint unnamed when the schema did, and hangs the whole storage
  clause off `USING INDEX`. SSMS brackets a routine name and qualifies it,
  `[dbo].[bump]`.

Routines were verified the same way, in all four dialects that have them: a
trigger and a function taken out of MySQL 8.4, PostgreSQL 17, Oracle 23ai and
SQL Server 2022 with that server's own tool, converted back to their own
dialect, loaded into a fresh database, and then fired by an `INSERT` and called
directly to confirm they still run. Converted to a different dialect, the same
routines come out commented and the file still loads.

## What a real schema exercises

The matrix above is a narrow schema, and a narrow schema hides things. The
fixtures under `tests/integration/testdata/schemas` are a wide one: the same
schema written five ways, each taken from the server's own dump tool after
loading it. Between them they carry table and column comments, composite and
self-referencing keys, every referential action, unique and partial and
composite indexes, checks written both on the column and at the table, views,
functions, procedures, triggers, sequences, an enum type, an array column, an
extension and grants.

All twenty-five conversions of those five load into a real server. Eleven of
them did not when the fixtures were first written, and the fourteen defects
behind that are what `tests/integration/full_schema_test.go` now holds a line
against: it records how much of each feature every parser finds and fails if one
of them starts finding less.

Comments travel with the schema. Each dialect states one its own way, which is
also how it is written back: `COMMENT ON` for PostgreSQL and Oracle, the column
and table options for MySQL, an extended property for SQL Server. SQLite has
nowhere to put one, so it keeps them as comments on the file rather than
dropping them.

Partitioning is the one thing a source can state that nothing carries. It is not
a small gap: every dialect spells it differently enough that there is little to
carry between them.

That matrix is not a promise about every version of every engine. It is the range
that was actually exercised. Untested: MySQL 9, PostgreSQL 18, Oracle 19c and
below, SQL Server 2016 and below, partitioned tables, generated columns, and
spatial types.

## How expressions are converted

Check constraints, column defaults and the `WHERE` clause of a view are parsed
into a small syntax tree and written back out for the target, rather than copied
across as text. That is what lets these survive a hop between dialects:

| Source                        | Target             | Result                 |
| ----------------------------- | ------------------ | ---------------------- |
| `((amount >= (0)::numeric))`  | MySQL              | `amount >= 0`          |
| `(([amount]>=(0)))`           | PostgreSQL         | `amount >= 0`          |
| `WHERE is_active`             | Oracle, SQL Server | `WHERE is_active <> 0` |
| `now()`                       | Oracle             | `SYSTIMESTAMP`         |
| `sysutcdatetime()`            | PostgreSQL         | `now()`                |
| `SELECT id FROM public.users` | MySQL              | `SELECT id FROM users` |
| `CHECK (json_valid(meta))`    | anywhere but MySQL | dropped                |

The tree has no node for a parenthesis, which is why the redundant ones dump
tools emit do not survive the trip.

An expression the parser cannot read is passed through unchanged, so a schema
that converted before this existed still converts.

What is _not_ parsed: the rest of a view body, and function, procedure and
trigger bodies. Those are statements and procedural code rather than
expressions, and they are carried over as they were written.

## Type mapping choices

Two mappings are deliberate rather than obvious, and both trade theoretical
fidelity for a schema that actually loads:

- **Oracle `NUMBER` with no precision becomes an integer**, not `numeric`. An
  unqualified `NUMBER` is Oracle's idiomatic surrogate key, and mapping it to
  `numeric` makes every foreign key onto an identity column fail to build. A
  column that genuinely needs scale declares it, and `DBMS_METADATA` writes it.
- **SQL Server `bit` becomes a small integer**, not `boolean`. SQL Server code
  compares a bit to `1`, and view bodies and check expressions are carried over
  verbatim, so a real boolean target fails with
  `operator does not exist: boolean = integer`.

Both choices are safe rather than reversible, and that shows if a converted
schema is converted again: a type with no faithful equivalent widens each time.
Oracle `NUMBER(5)` holds five digits, more than a `smallint`, so it becomes an
int and returns as `NUMBER(10)`; SQL Server `BIT` has no boolean to return to
and comes back as `SMALLINT`. Everything else lands on a fixed point, which
`tests/integration/fixed_point_test.go` checks for every pair.

## Documentation

| Page                                                                                                                                                        | What is in it                                           |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| [cli.md](docs/cli.md)                                                                                                                                       | Installing and running the command, flags, pipe recipes |
| [api.md](docs/api.md)                                                                                                                                       | The library API: parsers, generators, the schema types  |
| [stream_processing.md](docs/stream_processing.md)                                                                                                           | Streaming dumps too large to hold in memory             |
| [troubleshooting.md](docs/troubleshooting.md)                                                                                                               | What to do when a conversion comes out wrong            |
| [mysql.md](docs/mysql.md), [postgresql.md](docs/postgresql.md), [sqlite.md](docs/sqlite.md), [oracle.md](docs/oracle.md), [sqlserver.md](docs/sqlserver.md) | Per-dialect notes and type tables                       |

## Development

```bash
make check     # everything CI runs, in the same order
make bench     # parse, generate, convert, stream, and how each scales
make test      # unit and integration tests
make test-race # the stream parsers are exercised concurrently
make cover     # per-package coverage, failing below the 85% floor
make examples  # end-to-end conversions over the sample dumps
make lint      # the check set is pinned in .golangci.yml
make build     # the command, into ./bin
```

`make bench` reports MB/s for schemas of 10, 100 and 500 tables. That figure is
the one to watch: it has to stay flat. Two parsers looked for comments over the
whole file once per table, which made the cost grow with the square of the table
count, and a 500-table dump took two seconds instead of forty milliseconds.

`make` on its own lists every target. `make check` is the whole CI pipeline, so
a red build can be reproduced with one command. The linter version is pinned so
a new release cannot turn a pull request red on its own.

## Contributing

Issues and pull requests are welcome. If you hit a dump that converts wrongly,
the most useful bug report is the smallest fragment of SQL that reproduces it,
which dialect and version produced it, and what you expected to come out.

Oracle and SQL Server have the fewest regression tests behind them, and the gaps
there are worth more than polish elsewhere. Every fix in this repository
carries a regression test built from real dump tool output rather than
hand-shaped SQL; see `tests/integration` and the `*_real_ddl_test.go` files for
the shape to follow.

## License

Apache License 2.0. See [LICENSE](LICENSE).
