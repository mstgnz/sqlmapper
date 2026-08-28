# Stream Processing

The stream parsers read a dump statement by statement and hand each schema object to a callback, so a dump larger than available memory can still be processed. Every dialect ships one.

## Constructors

| Dialect    | Constructor                            |
| ---------- | -------------------------------------- |
| MySQL      | `mysql.NewMySQLStreamParser()`         |
| PostgreSQL | `postgres.NewPostgreSQLStreamParser()` |
| SQLite     | `sqlite.NewSQLiteStreamParser()`       |
| Oracle     | `oracle.NewOracleStreamParser()`       |
| SQL Server | `sqlserver.NewSQLServerStreamParser()` |

All of them satisfy `stream.StreamParser`:

```go
type StreamParser interface {
	ParseStream(reader io.Reader, callback func(SchemaObject) error) error
	ParseStreamParallel(reader io.Reader, callback func(SchemaObject) error, workers int) error
	GenerateStream(schema *sqlmapper.Schema, writer io.Writer) error
}
```

Note that the input is an `io.Reader`, not a string. That is the point: the file is never fully materialised.

## Basic usage

```go
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/stream"
)

func main() {
	file, err := os.Open("dump.sql")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	parser := mysql.NewMySQLStreamParser()

	err = parser.ParseStream(file, func(obj stream.SchemaObject) error {
		switch obj.Type {
		case stream.TableObject:
			table := obj.Data.(*sqlmapper.Table)
			fmt.Printf("table: %s (%d columns)\n", table.Name, len(table.Columns))
		case stream.ViewObject:
			view := obj.Data.(*sqlmapper.View)
			fmt.Printf("view: %s\n", view.Name)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}
```

Returning an error from the callback aborts the parse and surfaces that error.

## Parallel parsing

`ParseStreamParallel` spreads statements across a worker pool. The worker count is the third argument, after the callback:

```go
err := parser.ParseStreamParallel(file, func(obj stream.SchemaObject) error {
	return handle(obj)
}, runtime.NumCPU())
```

Your callback runs on the goroutine that drains the results channel, not on the workers, so it does not need to be safe for concurrent use itself. Anything it closes over and mutates does, if you spawn work from inside it.

Object order is not preserved in parallel mode. If you care about the order objects appeared in the file, use `ParseStream`.

## Object types

`SchemaObject.Type` is one of `TableObject`, `ViewObject`, `FunctionObject`, `ProcedureObject`, `TriggerObject`, `IndexObject`, `ConstraintObject`, `SequenceObject`, `TypeObject` or `PermissionObject`. `SchemaObject.Data` holds a pointer to the matching struct from the root package.

Which of these a given dialect actually emits varies. MySQL and PostgreSQL cover tables, views, functions, procedures and triggers; the others cover less.

## Writing a dump back out

`GenerateStream` writes a schema to an `io.Writer` without building the whole string in memory:

```go
out, err := os.Create("converted.sql")
if err != nil {
	log.Fatal(err)
}
defer out.Close()

err = postgres.NewPostgreSQLStreamParser().GenerateStream(schema, out)
```

## Statement delimiters

Each dialect uses the delimiter its own dumps use, which matters when you build a fixture by hand:

| Dialect                   | Delimiter           |
| ------------------------- | ------------------- |
| MySQL, PostgreSQL, SQLite | `;`                 |
| Oracle                    | `/` on its own line |
| SQL Server                | `GO`                |

`CREATE OR REPLACE` is understood everywhere it is legal; the optional keywords are folded away before the statement is classified.

The reader knows that a delimiter is not always one. A semicolon inside a string
literal, a comment, a quoted identifier, a PostgreSQL `$$ ... $$` body or a
`BEGIN ... END` block does not end a statement, and a mysqldump `DELIMITER ;;`
directive is followed rather than parsed. Routine bodies therefore arrive whole,
including the multi-statement ones.

Nested blocks are the exception. A body whose inner block closes with its own
`END;` ends the statement there, so a routine written that way is still cut
short.

## The same output as Parse and Generate

`ParseStream` and `Parse` read a dump with the same code and produce the same
schema. Only the shape differs: the stream hands over one object at a time, and
an index or a constraint the dump tool wrote as its own statement arrives on its
own rather than attached to a table.

`GenerateStream` and `Generate` write the same SQL, statement for statement.

`tests/integration/stream_agreement_test.go` holds both pairs side by side for
every dialect, so one drifting from the other fails the build.

## Limitations

- **A parser instance holds state.** Use one per goroutine. `ParseStreamParallel` handles this internally, but do not share a parser across your own goroutines.
- **Errors abort the run.** There is no skip-and-continue mode. One unparseable statement ends the stream.
- **An index or a constraint arrives without its table.** `SchemaObject` has no field naming the table one belongs to, so a consumer that needs the association has to track it from the surrounding statements. This is how a key reaches you when the dump tool writes it separately, which `pg_dump` does for every primary key, unique and foreign key, and SSMS does for foreign keys and checks.
- **Order is not preserved in parallel mode.** Use `ParseStream` when it matters.
