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

MySQL and PostgreSQL are the best covered pair and the one the test suite
exercises against real database servers. The other three dialects are usable but
less complete.

# What is converted

Tables, columns and their types, primary keys, foreign keys, unique and check
constraints, indexes, and views. Type mapping is dialect-aware: a PostgreSQL
"character varying(255)" becomes a MySQL VARCHAR(255), a MySQL ENUM becomes a
PostgreSQL enum type, an array column becomes JSON on the way to MySQL, and a
column driven by a sequence becomes AUTO_INCREMENT or SERIAL as appropriate.

# What is not converted

Function, procedure and trigger bodies are parsed into the schema but are not
translated, because their contents are procedural code rather than DDL. View
bodies are carried over verbatim: the SELECT is emitted unchanged, so a view
written against dialect-specific functions may need editing by hand. Table data
(INSERT statements) is not carried across.

The parsers are regular-expression based. They handle the output of mysqldump
and pg_dump, but they are not full SQL grammars: deeply nested procedural bodies
and exotic syntax can be missed. Review the generated SQL before running it
against anything you care about.

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
