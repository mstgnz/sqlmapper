# SQL Server Database Conversion Guide

## Table of Contents

1. [Introduction](#introduction)
2. [Data Type Mappings](#data-type-mappings)
3. [Syntax Differences](#syntax-differences)
4. [Common Issues](#common-issues)
5. [Examples](#examples)

## Introduction

This guide provides detailed information about converting SQL Server database schemas to and from other database systems using SQLMapper.

## Data Type Mappings

### SQL Server to MySQL

| SQL Server Type  | MySQL Type   | Notes                 |
| ---------------- | ------------ | --------------------- |
| BIGINT           | BIGINT       | -                     |
| INT              | INT          | -                     |
| SMALLINT         | SMALLINT     | -                     |
| TINYINT          | TINYINT      | Range differences     |
| DECIMAL(p,s)     | DECIMAL(p,s) | -                     |
| VARCHAR(n)       | VARCHAR(n)   | -                     |
| NVARCHAR(n)      | VARCHAR(n)   | UTF-8 encoding        |
| TEXT             | LONGTEXT     | -                     |
| DATETIME2        | DATETIME     | Precision differences |
| UNIQUEIDENTIFIER | CHAR(36)     | -                     |

### SQL Server to PostgreSQL

| SQL Server Type  | PostgreSQL Type | Notes |
| ---------------- | --------------- | ----- |
| BIGINT           | BIGINT          | -     |
| INT              | INTEGER         | -     |
| SMALLINT         | SMALLINT        | -     |
| DECIMAL(p,s)     | NUMERIC(p,s)    | -     |
| VARCHAR(n)       | VARCHAR(n)      | -     |
| NVARCHAR(n)      | VARCHAR(n)      | -     |
| TEXT             | TEXT            | -     |
| DATETIME2        | TIMESTAMP       | -     |
| UNIQUEIDENTIFIER | UUID            | -     |

## Syntax Differences

### Identity Columns

SQL Server:

```sql
CREATE TABLE users (
    id INT IDENTITY(1,1) PRIMARY KEY,
    username VARCHAR(50)
);
```

MySQL equivalent:

```sql
CREATE TABLE users (
    id INT AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(50)
);
```

PostgreSQL equivalent:

```sql
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    username VARCHAR(50)
);
```

### Stored Procedures

SQL Server:

```sql
CREATE PROCEDURE UpdateEmployee
    @EmpID INT,
    @Salary DECIMAL(10,2)
AS
BEGIN
    UPDATE Employees
    SET Salary = @Salary
    WHERE EmployeeID = @EmpID;
END;
```

MySQL equivalent:

```sql
DELIMITER //
CREATE PROCEDURE UpdateEmployee(
    IN p_EmpID INT,
    IN p_Salary DECIMAL(10,2)
)
BEGIN
    UPDATE Employees
    SET Salary = p_Salary
    WHERE EmployeeID = p_EmpID;
END //
DELIMITER ;
```

## Common Issues

### 1. Collation Differences

SQL Server uses different collation naming conventions:

```sql
-- SQL Server
COLLATE SQL_Latin1_General_CP1_CI_AS

-- MySQL
COLLATE utf8mb4_general_ci

-- PostgreSQL
COLLATE "en_US.utf8"
```

### 2. DateTime Handling

```sql
-- SQL Server
GETDATE()
DATEADD(day, 1, GETDATE())

-- MySQL
NOW()
DATE_ADD(NOW(), INTERVAL 1 DAY)

-- PostgreSQL
CURRENT_TIMESTAMP
CURRENT_TIMESTAMP + INTERVAL '1 day'
```

### 3. String Concatenation

```sql
-- SQL Server
SELECT FirstName + ' ' + LastName

-- MySQL
SELECT CONCAT(FirstName, ' ', LastName)

-- PostgreSQL
SELECT FirstName || ' ' || LastName
```

## Examples

### Converting Table with Computed Columns

```sql
-- Original SQL Server Table
CREATE TABLE orders (
    order_id INT IDENTITY(1,1),
    quantity INT,
    unit_price DECIMAL(10,2),
    total_price AS (quantity * unit_price),
    CONSTRAINT pk_orders PRIMARY KEY (order_id)
);

-- MySQL Conversion
CREATE TABLE orders (
    order_id INT AUTO_INCREMENT,
    quantity INT,
    unit_price DECIMAL(10,2),
    total_price DECIMAL(10,2) GENERATED ALWAYS AS (quantity * unit_price) STORED,
    PRIMARY KEY (order_id)
);

-- PostgreSQL Conversion
CREATE TABLE orders (
    order_id SERIAL,
    quantity INT,
    unit_price NUMERIC(10,2),
    total_price NUMERIC(10,2) GENERATED ALWAYS AS (quantity * unit_price) STORED,
    PRIMARY KEY (order_id)
);
```

### Converting Table with Custom Types

```sql
-- SQL Server Table with Custom Types
CREATE TABLE customer_data (
    id INT IDENTITY(1,1),
    customer_code UNIQUEIDENTIFIER DEFAULT NEWID(),
    status VARCHAR(20),
    metadata NVARCHAR(MAX),
    CONSTRAINT pk_customer PRIMARY KEY (id)
);

-- MySQL Conversion
CREATE TABLE customer_data (
    id INT AUTO_INCREMENT,
    customer_code CHAR(36) DEFAULT (UUID()),
    status VARCHAR(20),
    metadata JSON,
    PRIMARY KEY (id)
);

-- PostgreSQL Conversion
CREATE TABLE customer_data (
    id SERIAL,
    customer_code UUID DEFAULT gen_random_uuid(),
    status VARCHAR(20),
    metadata JSONB,
    PRIMARY KEY (id)
);
```
