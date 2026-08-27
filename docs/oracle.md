# Oracle Database Conversion Guide

## Table of Contents

1. [Introduction](#introduction)
2. [Data Type Mappings](#data-type-mappings)
3. [Syntax Differences](#syntax-differences)
4. [Common Issues](#common-issues)
5. [Examples](#examples)

## Introduction

This guide provides detailed information about converting Oracle database schemas to and from other database systems using SQLMapper.

## Data Type Mappings

### Oracle to MySQL

| Oracle Type | MySQL Type   | Notes                       |
| ----------- | ------------ | --------------------------- |
| NUMBER(p,s) | DECIMAL(p,s) | For exact numeric values    |
| NUMBER      | BIGINT       | When no precision specified |
| VARCHAR2    | VARCHAR      | -                           |
| CLOB        | LONGTEXT     | -                           |
| BLOB        | LONGBLOB     | -                           |
| DATE        | DATETIME     | -                           |
| TIMESTAMP   | TIMESTAMP    | -                           |

### Oracle to PostgreSQL

| Oracle Type | PostgreSQL Type | Notes |
| ----------- | --------------- | ----- |
| NUMBER(p,s) | NUMERIC(p,s)    | -     |
| VARCHAR2    | VARCHAR         | -     |
| CLOB        | TEXT            | -     |
| BLOB        | BYTEA           | -     |
| DATE        | TIMESTAMP       | -     |
| TIMESTAMP   | TIMESTAMP       | -     |

## Syntax Differences

### Sequences

Oracle:

```sql
CREATE SEQUENCE my_sequence
    START WITH 1
    INCREMENT BY 1
    NOCACHE
    NOCYCLE;
```

MySQL equivalent:

```sql
CREATE TABLE my_sequence (
    id BIGINT NOT NULL AUTO_INCREMENT,
    PRIMARY KEY (id)
);
```

PostgreSQL equivalent:

```sql
CREATE SEQUENCE my_sequence
    START 1
    INCREMENT 1
    NO CYCLE;
```

### Stored Procedures

Oracle:

```sql
CREATE OR REPLACE PROCEDURE update_employee(
    p_emp_id IN NUMBER,
    p_salary IN NUMBER
)
IS
BEGIN
    UPDATE employees
    SET salary = p_salary
    WHERE employee_id = p_emp_id;
END;
/
```

MySQL equivalent:

```sql
DELIMITER //
CREATE PROCEDURE update_employee(
    IN p_emp_id INT,
    IN p_salary DECIMAL(10,2)
)
BEGIN
    UPDATE employees
    SET salary = p_salary
    WHERE employee_id = p_emp_id;
END //
DELIMITER ;
```

## Common Issues

### 1. Date Format Differences

Oracle's default date format differs from other databases. Use explicit format strings:

```sql
-- Oracle
TO_DATE('2023-12-27', 'YYYY-MM-DD')

-- MySQL
STR_TO_DATE('2023-12-27', '%Y-%m-%d')

-- PostgreSQL
TO_DATE('2023-12-27', 'YYYY-MM-DD')
```

### 2. Sequence Usage

When converting sequences, be aware that:

- MySQL doesn't support sequences natively
- PostgreSQL sequences require explicit nextval() calls
- Oracle sequences can be used in DEFAULT values

### 3. NULL Handling

Oracle treats empty strings as NULL, while other databases distinguish between empty strings and NULL values.

## Examples

### Converting Table with Identity Column

```sql
-- Original Oracle Table
CREATE TABLE employees (
    emp_id NUMBER GENERATED ALWAYS AS IDENTITY,
    name VARCHAR2(100),
    salary NUMBER(10,2),
    hire_date DATE,
    CONSTRAINT pk_emp PRIMARY KEY (emp_id)
);

-- MySQL Conversion
CREATE TABLE employees (
    emp_id BIGINT AUTO_INCREMENT,
    name VARCHAR(100),
    salary DECIMAL(10,2),
    hire_date DATETIME,
    PRIMARY KEY (emp_id)
);

-- PostgreSQL Conversion
CREATE TABLE employees (
    emp_id SERIAL,
    name VARCHAR(100),
    salary NUMERIC(10,2),
    hire_date TIMESTAMP,
    PRIMARY KEY (emp_id)
);
```

### Converting Complex Types

```sql
-- Oracle Table with Complex Types
CREATE TABLE documents (
    doc_id NUMBER,
    content CLOB,
    metadata VARCHAR2(4000),
    binary_data BLOB,
    CONSTRAINT pk_doc PRIMARY KEY (doc_id)
);

-- MySQL Conversion
CREATE TABLE documents (
    doc_id BIGINT,
    content LONGTEXT,
    metadata JSON,
    binary_data LONGBLOB,
    PRIMARY KEY (doc_id)
);

-- PostgreSQL Conversion
CREATE TABLE documents (
    doc_id BIGINT,
    content TEXT,
    metadata JSONB,
    binary_data BYTEA,
    PRIMARY KEY (doc_id)
);
```
