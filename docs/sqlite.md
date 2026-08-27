# SQLite Features and Usage

## Overview

SQLMapper provides comprehensive support for converting SQLite database schemas to other database systems. This document outlines SQLite-specific features and usage examples.

## Supported Features

### Data Types

SQLite's dynamic type system supports five basic data types:

- `NULL`: Null value
- `INTEGER`: Integer values
- `REAL`: Floating point numbers
- `TEXT`: Text values
- `BLOB`: Binary data

Note: Due to SQLite's "type flexibility" feature, data types from other database systems are converted to these five basic types.

### Table Features

- Auto-incrementing fields (`AUTOINCREMENT`)
- Table constraints
- Temporary tables (`TEMPORARY TABLE`)
- `WITHOUT ROWID` tables
- Virtual tables (with FTS and R-Tree modules)

### Indexes

- Unique indexes
- Composite indexes
- Partial indexes
- Descending indexes

### Constraints

- `NOT NULL`
- `UNIQUE`
- `PRIMARY KEY`
- `FOREIGN KEY` (must be explicitly enabled)
- `CHECK`
- `DEFAULT` values

## Usage Examples

### Simple Table Creation

```sql
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    email TEXT UNIQUE,
    created_at TEXT DEFAULT (datetime('now', 'localtime')),
    active INTEGER DEFAULT 1 CHECK (active IN (0,1))
);
```

### Related Tables

```sql
-- Enable foreign key support
PRAGMA foreign_keys = ON;

CREATE TABLE categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE products (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    category_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    price REAL NOT NULL CHECK (price > 0),
    stock INTEGER DEFAULT 0,
    FOREIGN KEY (category_id) REFERENCES categories(id)
        ON DELETE CASCADE
        ON UPDATE CASCADE
);
```

### Index Usage

```sql
-- Unique index
CREATE UNIQUE INDEX idx_user_email ON users(email);

-- Composite index
CREATE INDEX idx_product_category ON products(category_id, name);

-- Partial index
CREATE INDEX idx_active_users ON users(id) WHERE active = 1;

-- Descending index
CREATE INDEX idx_product_price ON products(price DESC);
```

### View Creation

```sql
CREATE VIEW active_products AS
SELECT p.*, c.name as category_name
FROM products p
JOIN categories c ON p.category_id = c.id
WHERE p.stock > 0;
```

### Trigger Creation

```sql
CREATE TRIGGER tr_update_stock
AFTER INSERT ON order_details
BEGIN
    UPDATE products
    SET stock = stock - NEW.quantity
    WHERE id = NEW.product_id;
END;
```

### Virtual Table (FTS) Usage

```sql
-- Create FTS5 table
CREATE VIRTUAL TABLE articles USING fts5(
    title,
    content,
    tags
);

-- Search example
SELECT * FROM articles
WHERE articles MATCH 'python AND programming';
```

## Conversion Notes

### To MySQL

- `INTEGER PRIMARY KEY AUTOINCREMENT` -> `INT AUTO_INCREMENT PRIMARY KEY`
- `TEXT` -> Appropriate length `VARCHAR` or `TEXT`
- `REAL` -> `DOUBLE`
- SQLite triggers -> More comprehensive MySQL triggers

### To PostgreSQL

- `INTEGER PRIMARY KEY AUTOINCREMENT` -> `SERIAL PRIMARY KEY`
- `TEXT` -> `TEXT` or `VARCHAR`
- `REAL` -> `DOUBLE PRECISION`
- SQLite indexes -> PostgreSQL's advanced index types

### To Oracle

- `INTEGER PRIMARY KEY AUTOINCREMENT` -> `NUMBER` + `SEQUENCE` + `TRIGGER`
- `TEXT` -> `VARCHAR2` or `CLOB`
- `REAL` -> `NUMBER`
- SQLite views -> Oracle materialized views

## Best Practices

1. Explicitly enable foreign key support
2. Use indexes carefully (too many indexes can degrade performance in SQLite)
3. Use separate tables for large BLOB data
4. Make effective use of transactions
5. Consider using WAL (Write-Ahead Logging) mode

## Limitations

- Data types are limited and simplified during conversion from other systems
- Concurrent write operations are limited
- Complex triggers and stored procedures are not supported
- Table and column ALTER operations are limited
- Some advanced database features are not available (partitioning, materialized views, etc.)
