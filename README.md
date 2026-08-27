# SQLMapper

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

As a CLI:

```bash
go install github.com/mstgnz/sqlmapper/cmd/sqlmapper@latest
```

## Usage

### CLI

```bash
# Source dialect detected from the dump
sqlmapper --file=dump.sql --to=postgres

# Or state it explicitly
sqlmapper --file=dump.sql --from=mysql --to=postgres --out=schema.pg.sql
```

Supported values for `--from` and `--to`: `mysql`, `postgres`, `sqlite`, `oracle`, `sqlserver`.

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

| Dialect | Constructor |
| --- | --- |
| MySQL | `mysql.NewMySQL()` |
| PostgreSQL | `postgres.NewPostgreSQL()` |
| SQLite | `sqlite.NewSQLite()` |
| Oracle | `oracle.NewOracle()` |
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

| Source | Target | Result |
| --- | --- | --- |
| PostgreSQL `character varying(255)` | MySQL | `VARCHAR(255)` |
| PostgreSQL `text[]` | MySQL | `JSON` |
| PostgreSQL `bigint` + `nextval(...)` | MySQL | `BIGINT AUTO_INCREMENT` |
| PostgreSQL `boolean DEFAULT true` | MySQL | `TINYINT(1) DEFAULT 1` |
| MySQL `ENUM('a','b')` | PostgreSQL | `CREATE TYPE ... AS ENUM` |
| MySQL `bigint AUTO_INCREMENT` | PostgreSQL | `BIGSERIAL` |
| MySQL `json` | PostgreSQL | `JSONB` |

## What is not converted

Be aware of these before you trust the output:

- **Function, procedure and trigger bodies.** They are parsed into the schema but not translated, because they are procedural code, not DDL. PL/pgSQL and MySQL's `BEGIN ... END` are different languages.
- **View bodies.** The `CREATE VIEW` wrapper is generated for the target dialect, but the `SELECT` is copied verbatim. A view that calls dialect-specific functions will need editing.
- **Table data.** `INSERT` statements and `COPY` blocks are skipped. This is a schema tool.
- **Storage and tuning clauses.** Tablespaces, storage parameters, partitioning and engine options survive only where the target has an equivalent.

The parsers are regular-expression based rather than a full SQL grammar. They are built against the output of `mysqldump` and `pg_dump`, but unusual syntax can still slip through. **Read the generated SQL before running it on anything you care about.**

## Dialect and version coverage

Every cell below was verified by taking a schema out of the source server with
its own tool (`pg_dump`, `mysqldump`, `mariadb-dump`, `DBMS_METADATA.GET_DDL`,
an SSMS-shaped script both SQL Server versions accept), converting it, and
loading the result into the target server.

| Source \ Target | PG 13 | PG 17 | MySQL 5.7 | MySQL 8.4 | MariaDB 11 | Oracle 21 | Oracle 23ai | MSSQL 2019 | MSSQL 2022 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| PostgreSQL 13 | - | pass | pass | pass | pass | tables[^v] | pass | tables[^v] | tables[^v] |
| PostgreSQL 17 | pass | - | pass | pass | pass | tables[^v] | pass | tables[^v] | tables[^v] |
| MySQL 5.7 | pass | pass | - | pass | pass | pass | pass | pass | pass |
| MySQL 8.4 | pass | pass | pass | - | pass | pass | pass | pass | pass |
| MariaDB 11 | pass | pass | pass | pass | - | pass | pass | pass | pass |
| Oracle 21 XE | pass | pass | pass | pass | pass | - | pass | pass | pass |
| Oracle 23ai | pass | pass | pass | pass | pass | pass | - | pass | pass |
| SQL Server[^s] | pass | pass | pass | pass | pass | pass | pass | pass | pass |

[^s]: SQL Server has no dump CLI in the box. The source script is in the shape
SSMS "Generate Scripts" produces, and both 2019 and 2022 accept it verbatim,
which is what makes it a real fixture rather than a convenient one.

[^v]: Tables, columns, keys, constraints and indexes all load. The **view** does
not, and the reason is worth understanding: its body says `WHERE is_active`,
PostgreSQL's shorthand for a boolean column. Oracle 21 and SQL Server have no
boolean type, so they reject the expression. View bodies are copied verbatim
(see [what is not converted](#what-is-not-converted)); translating query syntax
is out of scope. Oracle 23ai accepts it because it added a native boolean.

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

That matrix is not a promise about every version of every engine. It is the range
that was actually exercised. Untested: MySQL 9, PostgreSQL 18, Oracle 19c and
below, SQL Server 2016 and below, partitioned tables, generated columns, and
spatial types.

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

## Development

```bash
go test ./...          # unit and integration tests
go test -race ./...    # the stream parsers are exercised concurrently
go test -cover ./...   # every package is kept above 85%
go run ./examples      # end-to-end conversions over the sample dumps
```

## Contributing

Issues and pull requests are welcome. If you hit a dump that converts wrongly, the most useful bug report is the smallest fragment of SQL that reproduces it plus what you expected to come out.

## License

Apache License 2.0. See [LICENSE](LICENSE).
