# CLI

`sqlmapper` converts a SQL dump from one dialect to another. It reads a file or
standard input, writes a file or standard output, and never connects to a
database.

## Install

### Prebuilt binary

Every release publishes static binaries. Nothing else has to be on the server:
no Go toolchain, no database client.

```bash
curl -fsSL -o /usr/local/bin/sqlmapper \
  https://github.com/mstgnz/sqlmapper/releases/latest/download/sqlmapper-linux-amd64
chmod +x /usr/local/bin/sqlmapper
sqlmapper --version
```

Pin the version where a reproducible install matters, by replacing
`latest/download` with `download/v1.1.1` or whichever tag you want.

Assets are named `sqlmapper-<os>-<arch>`: `linux-amd64`, `linux-arm64`,
`darwin-amd64`, `darwin-arm64`, and `windows-amd64.exe`.

### With Go

```bash
go install github.com/mstgnz/sqlmapper/cmd/sqlmapper@latest
```

The binary lands in `$(go env GOPATH)/bin`, which has to be on `PATH`.

### Docker

```bash
docker run --rm -v "$PWD:/work" mstgnz/sqlmapper --file=dump.sql --to=postgres
```

The image sets `/work` as the working directory and runs as an unprivileged
user, so the dump is mounted there and the output is written beside it. For a
pipe, keep standard input open and let the output through:

```bash
mysqldump app | docker run --rm -i mstgnz/sqlmapper --from=mysql --to=postgres > app.pg.sql
```

## Usage

```bash
# A file in, a file out. The output path defaults to <input>_<target>.sql
sqlmapper --file=dump.sql --to=postgres

# State the source explicitly and name the output
sqlmapper --file=dump.sql --from=mysql --to=postgres --out=schema.pg.sql

# In a pipe: standard input to standard output
mysqldump app | sqlmapper --from=mysql --to=postgres > app.pg.sql

# Straight into the target database
mysqldump --no-data app | sqlmapper --from=mysql --to=postgres | psql app
```

## Flags

| Flag        | Meaning                                                                                                                          |
| ----------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `--file`    | Input dump. `-` or omitted reads standard input.                                                                                 |
| `--to`      | Target dialect. Required.                                                                                                        |
| `--from`    | Source dialect. Detected from the dump when omitted.                                                                             |
| `--out`     | Output file. `-` writes standard output. Defaults to `<input>_<target>.sql`, or standard output when the input came from a pipe. |
| `--version` | Print the version and exit.                                                                                                      |

Dialect names for `--from` and `--to`, with the aliases each one accepts:

| Dialect    | Accepted values                   |
| ---------- | --------------------------------- |
| MySQL      | `mysql`, `mariadb`                |
| PostgreSQL | `postgres`, `postgresql`, `pgsql` |
| SQLite     | `sqlite`, `sqlite3`               |
| Oracle     | `oracle`                          |
| SQL Server | `sqlserver`, `mssql`              |

## Output streams

Standard output carries the converted SQL and nothing else, which is what makes
the pipe usable. The summary line naming the detected dialect and the output
path goes to standard error, and only when the result was written to a file.

Exit status is 0 on success and 1 on any error, with the message on standard
error.

## Source detection

Omitting `--from` scores the dump against the markers each dump tool leaves
behind: `ENGINE=` and `AUTO_INCREMENT` for MySQL, `nextval(` and `OWNER TO` for
PostgreSQL, `IDENTITY(` and `NVARCHAR` for SQL Server, `VARCHAR2` and `SYSDATE`
for Oracle, `AUTOINCREMENT` and `PRAGMA` for SQLite. The highest score wins, and
nothing matching at all is reported rather than guessed.

It is a convenience for interactive use. Pass `--from` in scripts: a small or
hand-written dump carries few markers, and a dialect detected wrongly produces
output that looks plausible and is not.

## Automating a conversion

The command is a filter, so it fits wherever one does. Converting a schema in CI:

```bash
set -euo pipefail
sqlmapper --file=schema/mysql.sql --from=mysql --to=postgres --out=schema/postgres.sql
git diff --exit-code schema/postgres.sql
```

That fails the build when the generated file is out of date with its source.

Review the output before running it anywhere that matters. What is and is not
carried across is listed in the [README](../README.md#what-is-not-converted).
