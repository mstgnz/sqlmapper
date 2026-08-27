# SQLMapper API Documentation

## Overview
SQLMapper provides a comprehensive API for converting SQL schemas between different database systems. This document outlines the main components and their usage.

## Table of Contents
- [Parser API](#parser-api)
- [Converter API](#converter-api)
- [Schema API](#schema-api)
- [Error Handling](#error-handling)
- [Examples](#examples)

## Parser API

### Creating a Parser

```go
parser := mysql.NewMySQL()    // For MySQL
parser := postgres.NewPostgreSQL()  // For PostgreSQL
parser := sqlite.NewSQLite()  // For SQLite
parser := oracle.NewOracle()  // For Oracle
parser := sqlserver.NewSQLServer()  // For SQL Server
```

Each parser implements the `Parser` interface:

```go
type Parser interface {
    Parse(content string) (*Schema, error)
    Generate(schema *Schema) (string, error)
}
```

### Parsing SQL

```go
// Parse SQL content into a schema
schema, err := parser.Parse(sqlContent)
if err != nil {
    log.Fatal(err)
}
```

### Generating SQL

```go
// Generate SQL from a schema
sql, err := parser.Generate(schema)
if err != nil {
    log.Fatal(err)
}
```

## Schema API

The Schema structure represents a complete database schema:

```go
type Schema struct {
    Name        string
    Tables      []Table
    Views       []View
    Triggers    []Trigger
    Sequences   []Sequence
    Functions   []Function
    Procedures  []Procedure
    // ... other fields
}
```

### Table Structure

```go
type Table struct {
    Name        string
    Schema      string
    Columns     []Column
    Indexes     []Index
    Constraints []Constraint
    Comment     string
}
```

### Column Structure

```go
type Column struct {
    Name            string
    DataType        string
    Length          int
    Scale           int
    Precision       int
    IsNullable      bool
    DefaultValue    string
    AutoIncrement   bool
    IsPrimaryKey    bool
    IsUnique        bool
    IsUnsigned      bool
    Comment         string
    Order           int
    CheckExpression string
    EnumValues      []string // for ENUM/SET types
    IsArray         bool     // PostgreSQL array type (e.g. text[])
}
```

## Error Handling

`Parse` and `Generate` return a plain `error`; there are no exported sentinel values to compare against yet, so match on the message or simply propagate:

```go
schema, err := parser.Parse(content)
if err != nil {
    return fmt.Errorf("parsing %s dump: %w", "mysql", err)
}
```

Both methods reject empty input with `"empty content"` / `"empty schema"`. Parse errors carry the parsing stage that failed, for example `"error parsing tables: ..."`.

## Examples

### Basic Usage

```go
package main

import (
    "fmt"
    "github.com/mstgnz/sqlmapper/mysql"
    "github.com/mstgnz/sqlmapper/postgres"
)

func main() {
    // MySQL input
    mysqlSQL := `
    CREATE TABLE users (
        id INT AUTO_INCREMENT PRIMARY KEY,
        name VARCHAR(100) NOT NULL,
        email VARCHAR(255) UNIQUE,
        created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    );`

    // Create parsers
    mysqlParser := mysql.NewMySQL()
    pgParser := postgres.NewPostgreSQL()

    // Parse MySQL
    schema, err := mysqlParser.Parse(mysqlSQL)
    if err != nil {
        panic(err)
    }

    // Generate PostgreSQL
    pgSQL, err := pgParser.Generate(schema)
    if err != nil {
        panic(err)
    }

    fmt.Println(pgSQL)
}
```

### CLI Usage

```bash
# Convert MySQL to PostgreSQL
sqlmapper --file=dump.sql --to=postgres

# Convert PostgreSQL to SQLite
sqlmapper --file=schema.sql --to=sqlite
```

### Advanced Usage

```go
package main

import (
    "github.com/mstgnz/sqlmapper"
    "github.com/mstgnz/sqlmapper/mysql"
)

func main() {
    parser := mysql.NewMySQL()
    
    // Parse complex schema
    schema, err := parser.Parse(complexSQL)
    if err != nil {
        panic(err)
    }

    // Modify schema
    for i, table := range schema.Tables {
        // Add timestamps to all tables
        schema.Tables[i].Columns = append(table.Columns,
            sqlmapper.Column{
                Name: "created_at",
                DataType: "TIMESTAMP",
                DefaultValue: "CURRENT_TIMESTAMP",
            },
            sqlmapper.Column{
                Name: "updated_at",
                DataType: "TIMESTAMP",
                DefaultValue: "CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP",
            },
        )
    }

    // Generate modified SQL
    sql, err := parser.Generate(schema)
    if err != nil {
        panic(err)
    }
}
```

## Best Practices

1. Always check for errors when parsing and generating SQL
2. Use appropriate parsers for source and target databases
3. Validate schema modifications before generating SQL
4. Handle database-specific features carefully
5. Test conversions with sample data

## Limitations

- Some database-specific features may not be perfectly converted
- Complex stored procedures might need manual adjustment
- Custom data types may require special handling
- Performance may vary with large schemas
