/*
Package sqlmapper converts SQL schema definitions between database dialects.

The library reads a SQL dump, parses the DDL into a dialect-neutral [Schema],
and renders that schema back out as SQL for a different database. It works on
files: nothing here connects to a database.

# Supported dialects

MySQL, PostgreSQL, SQLite, Oracle and SQL Server. Each dialect lives in its own
package and implements [Database]:

	import (
		"github.com/mstgnz/sqlmapper/mysql"
		"github.com/mstgnz/sqlmapper/postgres"
	)

	schema, err := mysql.NewMySQL().Parse(mysqlDump)
	if err != nil {
		return err
	}

	pgSQL, err := postgres.NewPostgreSQL().Generate(schema)
	if err != nil {
		return err
	}

Every pair is exercised against real database servers: a schema is taken out of
the source with its own dump tool, converted, and loaded into the target. MySQL
and PostgreSQL are the pair with the most regression tests behind them.

# What is converted

Tables, columns and their types, primary keys, foreign keys, unique and check
constraints, indexes, and views. Type mapping is dialect-aware: a PostgreSQL
"character varying(255)" becomes a MySQL VARCHAR(255), a MySQL ENUM becomes a
PostgreSQL enum type, an array column becomes JSON on the way to MySQL, and a
column driven by a sequence becomes AUTO_INCREMENT or SERIAL as appropriate.

# Expressions

Check constraints, column defaults and the WHERE clause of a view are parsed
into a syntax tree and written back out for the target rather than copied as
text. PostgreSQL's ((amount >= (0)::numeric)) and SQL Server's (([amount]>=(0)))
both become amount >= 0; a view saying WHERE is_active becomes
WHERE is_active <> 0 for Oracle and SQL Server, which have no boolean type; and
each dialect's spelling of the current timestamp becomes the target's. An
expression the parser cannot read is passed through unchanged.

# What is not converted

Function, procedure and trigger bodies are not translated, because their
contents are procedural code rather than DDL. Converting a database to itself
keeps its routines executable; converting to a different one writes each routine
out commented, naming the source it came from, so the output still loads and
nothing goes missing without a trace. The
parts of a view body outside the WHERE clause are carried over verbatim, so a
view written against dialect-specific functions may need editing by hand. Table
data (INSERT statements) is not carried across.

The parsers are regular-expression based. They handle the output of mysqldump
and pg_dump, but they are not full SQL grammars: deeply nested procedural bodies
and exotic syntax can be missed. Review the generated SQL before running it
against anything you care about.

# Command line

The same conversion is available as a command, which reads a file or standard
input and writes a file or standard output:

	go install github.com/mstgnz/sqlmapper/cmd/sqlmapper@latest

	sqlmapper --file=dump.sql --to=postgres
	mysqldump app | sqlmapper --from=mysql --to=postgres > app.pg.sql

# Streaming

For dumps too large to hold in memory, each dialect ships a stream parser that
reports schema objects through a callback as they are read:

	import "github.com/mstgnz/sqlmapper/stream"

	parser := mysql.NewMySQLStreamParser()
	err := parser.ParseStream(file, func(obj stream.SchemaObject) error {
		// obj.Type is TableObject, ViewObject, ...
		return nil
	})

# Concurrency

A parser instance holds the schema it is building, so a single instance must not
be shared across goroutines. Create one parser per goroutine. The stream
parsers' ParseStreamParallel does this internally and is safe to call.

For more information and examples, visit: https://github.com/mstgnz/sqlmapper
*/
package sqlmapper
