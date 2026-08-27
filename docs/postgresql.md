# PostgreSQL Features and Usage

## Overview

SQLMapper provides comprehensive support for converting PostgreSQL database schemas to other database systems. This document outlines PostgreSQL-specific features and usage examples.

## Supported Features

### Data Types

- Numeric: `SMALLINT`, `INTEGER`, `BIGINT`, `DECIMAL`, `NUMERIC`, `REAL`, `DOUBLE PRECISION`
- Text: `CHAR`, `VARCHAR`, `TEXT`
- Date/Time: `DATE`, `TIME`, `TIMESTAMP`, `INTERVAL`
- Binary: `BYTEA`
- Geometric: `POINT`, `LINE`, `LSEG`, `BOX`, `PATH`, `POLYGON`, `CIRCLE`
- Network Addresses: `INET`, `CIDR`, `MACADDR`
- JSON: `JSON`, `JSONB`
- Arrays: Array version of each data type
- Special: `UUID`, `XML`, `MONEY`

### Table Features

- Auto-incrementing fields (`SERIAL`, `BIGSERIAL`)
- Table and column comments
- Tablespace definitions
- Inheritance
- Partitioned tables

### Indexes

- B-tree indexes
- Hash indexes
- GiST indexes
- SP-GiST indexes
- GIN indexes
- BRIN indexes
- Partial indexes
- Expression indexes

### Constraints

- `NOT NULL`
- `UNIQUE`
- `PRIMARY KEY`
- `FOREIGN KEY`
- `CHECK`
- `EXCLUDE`
- `DEFAULT` values

## Usage Examples

### Simple Table Creation

```sql
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(255) UNIQUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT email_format CHECK (email ~* '^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$')
);

COMMENT ON TABLE users IS 'Table containing user information';
COMMENT ON COLUMN users.email IS 'Unique email address of the user';
```

### Related Tables and Inheritance

```sql
CREATE TABLE employees (
    id SERIAL PRIMARY KEY,
    first_name VARCHAR(100) NOT NULL,
    last_name VARCHAR(100) NOT NULL,
    salary NUMERIC(10,2) NOT NULL,
    department_id INTEGER REFERENCES departments(id)
);

CREATE TABLE managers (
    authority_level INTEGER NOT NULL,
    bonus_percentage NUMERIC(5,2)
) INHERITS (employees);
```

### Advanced Index Usage

```sql
-- B-tree index
CREATE INDEX idx_user_name ON users (name);

-- Partial index
CREATE INDEX idx_active_users ON users (id)
WHERE active = true;

-- Expression index
CREATE INDEX idx_email_domain ON users ((split_part(email, '@', 2)));

-- GiST index (for geometric data)
CREATE INDEX idx_location ON locations USING GIST (coordinate);

-- GIN index (for JSONB data)
CREATE INDEX idx_properties ON products USING GIN (properties);
```

### Views and Materialized Views

```sql
-- Regular view
CREATE VIEW active_orders AS
SELECT o.*, u.name as customer_name
FROM orders o
JOIN users u ON o.user_id = u.id
WHERE o.status = 'active';

-- Materialized view
CREATE MATERIALIZED VIEW monthly_sales_summary AS
SELECT
    date_trunc('month', date) as month,
    sum(amount) as total_sales,
    count(*) as order_count
FROM orders
GROUP BY 1
WITH DATA;
```

### Triggers and Functions

```sql
CREATE OR REPLACE FUNCTION update_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tr_update_timestamp
    BEFORE UPDATE ON users
    FOR EACH ROW
    EXECUTE FUNCTION update_timestamp();
```

## Conversion Notes

### To MySQL

- `SERIAL` -> `AUTO_INCREMENT`
- `INTERVAL` -> `VARCHAR` or `INT`
- Inheritance tables -> Split into separate tables
- CHECK constraints -> Not supported before MySQL 8.0.16

### To SQLite

- `SERIAL` -> `AUTOINCREMENT`
- Complex data types -> `TEXT` or `BLOB`
- Materialized views -> Regular views or tables
- GiST/GIN indexes -> Not supported

### To Oracle

- `SERIAL` -> `SEQUENCE` and `TRIGGER`
- `VARCHAR` -> `VARCHAR2`
- `TEXT` -> `CLOB`
- `JSONB` -> `CLOB` + JSON functions

## Best Practices

1. Choose appropriate index types
2. Regularly refresh materialized views
3. Plan partitioning strategy carefully
4. Use EXPLAIN ANALYZE to optimize queries
5. Use constraints and triggers effectively

## Limitations

- Some PostgreSQL-specific features may not be perfectly converted to other databases
- Complex functions and procedures might need manual adjustment
- Custom data types may be simplified during conversion
- Performance may vary with large schemas
