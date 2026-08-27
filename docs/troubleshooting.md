# Troubleshooting

## The CLI

The whole flag set is four options. Anything else you read elsewhere does not exist.

```bash
sqlmapper --file=<path> --to=<target> [--from=<source>] [--out=<path>]
```

| Flag     | Meaning                                                                         |
| -------- | ------------------------------------------------------------------------------- |
| `--file` | Input SQL dump. Required.                                                       |
| `--to`   | Target dialect: `mysql`, `postgres`, `sqlite`, `oracle`, `sqlserver`. Required. |
| `--from` | Source dialect. Detected from the dump when omitted.                            |
| `--out`  | Output path. Defaults to `<input>_<target>.sql` beside the input.               |

Single and double dashes are equivalent, so `-file=dump.sql` and `--file=dump.sql` both work.

There is no debug flag, no config file, no environment variable, and no dry-run mode. If you need any of those, open an issue.

## Common failures

### "could not detect the source database type; pass --from explicitly"

Detection scores the dump against per-dialect markers and needs at least one hit. Short fragments and hand-written schemas frequently have none.

```bash
sqlmapper --file=schema.sql --from=postgres --to=mysql
```

Always pass `--from` in scripts. Detection is a convenience for interactive use, not something to depend on.

### "parse error: error parsing tables: ..."

The parsers are regular-expression based rather than a full SQL grammar. The usual causes:

- A routine body containing the statement delimiter. `CREATE FUNCTION ... BEGIN ... ; ... END;` gets split at the inner semicolon. Extract those statements into their own file.
- Vendor syntax the pattern does not cover. Reduce the dump to the failing statement to confirm, then open an issue with that fragment.

### The output is missing objects

`Generate` emits tables, columns, constraints, indexes and views. Functions, procedures and triggers are parsed into the schema but not written out, because their bodies are procedural code rather than DDL. Table data is not carried across at all. See [what is not converted](../README.md#what-is-not-converted).

### An expression came out unchanged when it should have been translated

Check constraints, column defaults and a view's `WHERE` clause are parsed and rewritten for the target. Anything the expression parser cannot read is passed through exactly as it arrived, on the principle that a conversion that used to work should keep working. If you find an expression that survives untranslated and should not, the fragment itself is the bug report.

The rest of a view body, and function, procedure and trigger bodies, are copied verbatim by design.

### The generated SQL will not run

Read it before running it. The conversion is best-effort, and the cases below need a human.

## Type conversion notes

### PostgreSQL arrays to MySQL

MySQL has no array type, so an array column becomes `JSON`:

```sql
-- PostgreSQL source
CREATE TABLE items (id bigserial PRIMARY KEY, tags text[]);

-- What SQLMapper produces
CREATE TABLE items (id BIGINT AUTO_INCREMENT PRIMARY KEY, tags JSON);
```

The schema converts; the data does not. Moving the rows means turning each array into a JSON array yourself.

### MySQL ENUM to PostgreSQL

An ENUM column becomes a named type declared ahead of the table:

```sql
-- MySQL source
status enum('active','banned') NOT NULL DEFAULT 'active'

-- What SQLMapper produces
CREATE TYPE users_status_enum AS ENUM ('active', 'banned');
...
status users_status_enum NOT NULL DEFAULT 'active'
```

The type is named `<table>_<column>_enum`. Two tables with an identically named ENUM column get two distinct types, which is intentional: their value sets may differ.

### JSON

MySQL `json` becomes PostgreSQL `jsonb` and back. SQLite has no JSON type, so it becomes `TEXT`.

MySQL rejects a literal default on a JSON, TEXT or BLOB column, so a default arriving from another dialect on such a column is dropped rather than emitted as invalid SQL.

### Character sets and collations

Collation and character set clauses are not carried across. `utf8mb4` is close enough to PostgreSQL's UTF8 that nothing needs doing, but if the source database is not UTF-8, convert it before dumping.

### Oracle ROWID

There is no equivalent anywhere else. If your application depends on ROWID, add a surrogate key before migrating:

```sql
-- MySQL
ALTER TABLE employees ADD COLUMN row_identifier BIGINT AUTO_INCREMENT UNIQUE;

-- PostgreSQL
ALTER TABLE employees ADD COLUMN row_identifier BIGSERIAL;
```

## Loading the output

### Foreign keys fail because of table order

They should not: tables are sorted so that a table follows the ones its foreign
keys point at, and a key that closes a reference cycle is emitted as a trailing
`ALTER TABLE` instead of inline. If you still hit an ordering failure, that is a
bug worth reporting.

If you need to load a hand-edited script that is out of order, defer the checks
around it:

```sql
-- MySQL
SET FOREIGN_KEY_CHECKS = 0;
-- ... load ...
SET FOREIGN_KEY_CHECKS = 1;
```

```sql
-- PostgreSQL, inside a transaction
SET CONSTRAINTS ALL DEFERRED;
-- ... load ...
```

### Checking for truncation before you migrate

Type mapping can narrow a column. Check the real widths in the source first:

```sql
SELECT MAX(LENGTH(email)) FROM users;
```

## Large dumps

There is no chunking or memory-tuning flag. For a dump too large to hold in memory, use the stream parsers from the library rather than the CLI: see [stream_processing.md](stream_processing.md). The CLI reads the whole file at once.

## Reporting a bug

The most useful report is the smallest fragment of SQL that reproduces the problem, the exact command, the source and target dialects, and what you expected to come out.
