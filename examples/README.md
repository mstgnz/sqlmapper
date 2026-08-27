# SQLMapper Examples

A single runnable program that exercises the library end to end.

```bash
go run ./examples
```

It does two things:

1. **Stream parsing.** Reads `files/mysql.sql` through the MySQL stream parser and prints each schema object as it is discovered, without loading the whole dump into memory.
2. **Conversion.** Converts each sample dump to another dialect and writes the result under `files/output/`.

## Files

| Path | Contents |
| --- | --- |
| `main.go` | The program described above |
| `files/mysql.sql` | Sample MySQL dump |
| `files/postgres.sql` | Sample PostgreSQL dump |
| `files/sqlite.sql` | Sample SQLite schema |
| `files/oracle.sql` | Sample Oracle schema |
| `files/sqlserver.sql` | Sample SQL Server schema |
| `files/output/` | Generated SQL, rewritten on every run |

The conversions it runs are PostgreSQL to MySQL, MySQL to PostgreSQL, Oracle to MySQL, SQL Server to PostgreSQL, and SQLite to Oracle.

## Using the library directly

The shortest useful program is in the [top-level README](../README.md#library). Both parse and generate are single method calls, so an example directory of one-liners would not add much beyond it.
