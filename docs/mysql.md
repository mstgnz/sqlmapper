# MySQL Features and Usage

## Overview

SQLMapper provides comprehensive support for converting MySQL database schemas to other database systems. This document outlines MySQL-specific features and usage examples.

## Supported Features

### Data Types

- Numeric: `INT`, `TINYINT`, `SMALLINT`, `MEDIUMINT`, `BIGINT`, `DECIMAL`, `FLOAT`, `DOUBLE`
- Text: `CHAR`, `VARCHAR`, `TEXT`, `TINYTEXT`, `MEDIUMTEXT`, `LONGTEXT`
- Date/Time: `DATE`, `TIME`, `DATETIME`, `TIMESTAMP`, `YEAR`
- Binary: `BINARY`, `VARBINARY`, `BLOB`, `TINYBLOB`, `MEDIUMBLOB`, `LONGBLOB`
- Others: `ENUM`, `SET`, `JSON`

### Table Features

- Auto-incrementing fields (`AUTO_INCREMENT`)
- Table comments (`COMMENT`)
- Table character set and collation
- Storage engines (InnoDB, MyISAM, etc.)

### Indexes

- Primary keys
- Foreign keys
- Unique indexes
- Composite indexes
- Full-text indexes

### Constraints

- `NOT NULL`
- `UNIQUE`
- `PRIMARY KEY`
- `FOREIGN KEY`
- `CHECK` (MySQL 8.0.16 and above)
- `DEFAULT` values

## Usage Examples

### Simple Table Creation

```sql
CREATE TABLE users (
    id INT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(255) UNIQUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    COMMENT 'Table containing user information'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci;
```

### Related Tables

```sql
CREATE TABLE categories (
    id INT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description TEXT
) ENGINE=InnoDB;

CREATE TABLE products (
    id INT AUTO_INCREMENT PRIMARY KEY,
    category_id INT NOT NULL,
    name VARCHAR(200) NOT NULL,
    price DECIMAL(10,2) NOT NULL,
    stock INT DEFAULT 0,
    FOREIGN KEY (category_id) REFERENCES categories(id)
        ON DELETE CASCADE
        ON UPDATE CASCADE
) ENGINE=InnoDB;
```

### Index Usage

```sql
CREATE TABLE articles (
    id INT AUTO_INCREMENT PRIMARY KEY,
    title VARCHAR(200) NOT NULL,
    content TEXT,
    tags VARCHAR(500),
    publish_date TIMESTAMP,
    INDEX idx_publish_date (publish_date),
    FULLTEXT INDEX idx_title_content (title, content)
) ENGINE=InnoDB;
```

### View Creation

```sql
CREATE VIEW active_products AS
SELECT p.id, p.name, p.price, c.name AS category_name
FROM products p
JOIN categories c ON p.category_id = c.id
WHERE p.stock > 0;
```

### Trigger Creation

```sql
DELIMITER //
CREATE TRIGGER product_update_log
AFTER UPDATE ON products
FOR EACH ROW
BEGIN
    INSERT INTO product_log (product_id, old_price, new_price, update_time)
    VALUES (NEW.id, OLD.price, NEW.price, NOW());
END//
DELIMITER ;
```

## Conversion Notes

### To PostgreSQL

- `AUTO_INCREMENT` -> `SERIAL` or `IDENTITY`
- `UNSIGNED` -> Removed (PostgreSQL doesn't support it)
- `ON UPDATE CURRENT_TIMESTAMP` -> Simulated using triggers
- `ENUM` -> PostgreSQL's native `ENUM` type or `CHECK` constraint

### To SQLite

- `AUTO_INCREMENT` -> `AUTOINCREMENT`
- Complex data types -> `TEXT` or `BLOB`
- Foreign key constraints -> Limited FK support in SQLite
- Triggers -> Simplified trigger syntax

### To Oracle

- `AUTO_INCREMENT` -> `SEQUENCE` and `TRIGGER`
- `TIMESTAMP` -> `DATE` or `TIMESTAMP`
- `VARCHAR` -> `VARCHAR2`
- `TEXT` -> `CLOB`

## Best Practices

1. Always use UTF8MB4 character set
2. Prefer InnoDB engine type
3. Plan indexes carefully
4. Choose appropriate data types
5. Pay attention to comment usage

## Limitations

- Some MySQL-specific features may not be perfectly converted to other databases
- Complex triggers might need manual adjustment
- Some data types may be simplified during conversion
- Performance may vary with large schemas
