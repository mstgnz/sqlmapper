package integration

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMySQLToPostgreSQL validates that a realistic MySQL dump converts correctly to PostgreSQL.
func TestMySQLToPostgreSQL(t *testing.T) {
	mysqlSQL := `
CREATE TABLE ` + "`" + `users` + "`" + ` (
  ` + "`" + `id` + "`" + ` int NOT NULL AUTO_INCREMENT,
  ` + "`" + `name` + "`" + ` varchar(100) NOT NULL,
  ` + "`" + `email` + "`" + ` varchar(255) NOT NULL,
  ` + "`" + `status` + "`" + ` enum('active','inactive','pending') NOT NULL DEFAULT 'active',
  ` + "`" + `balance` + "`" + ` decimal(10,2) NOT NULL DEFAULT 0.00,
  ` + "`" + `bio` + "`" + ` text,
  ` + "`" + `created_at` + "`" + ` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (` + "`" + `id` + "`" + `),
  UNIQUE KEY ` + "`" + `idx_email` + "`" + ` (` + "`" + `email` + "`" + `)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE ` + "`" + `orders` + "`" + ` (
  ` + "`" + `id` + "`" + ` int NOT NULL AUTO_INCREMENT,
  ` + "`" + `user_id` + "`" + ` int NOT NULL,
  ` + "`" + `total` + "`" + ` decimal(12,2) NOT NULL DEFAULT 0.00,
  PRIMARY KEY (` + "`" + `id` + "`" + `),
  CONSTRAINT ` + "`" + `fk_order_user` + "`" + ` FOREIGN KEY (` + "`" + `user_id` + "`" + `) REFERENCES ` + "`" + `users` + "`" + ` (` + "`" + `id` + "`" + `) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
`

	mysqlParser := mysql.NewMySQL()
	schema, err := mysqlParser.Parse(mysqlSQL)
	require.NoError(t, err)
	require.NotNil(t, schema)

	// Schema should have 2 tables
	assert.Len(t, schema.Tables, 2)

	// Verify users table parsing
	usersTable := schema.Tables[0]
	assert.Equal(t, "users", usersTable.Name)
	assert.Len(t, usersTable.Columns, 7)

	// id should be AUTO_INCREMENT
	assert.True(t, usersTable.Columns[0].AutoIncrement)
	assert.Equal(t, "int", usersTable.Columns[0].DataType)

	// status should have ENUM values
	statusCol := usersTable.Columns[3]
	assert.Equal(t, "enum", statusCol.DataType)
	assert.Equal(t, []string{"active", "inactive", "pending"}, statusCol.EnumValues)

	// email should not be nullable
	emailCol := usersTable.Columns[2]
	assert.False(t, emailCol.IsNullable)

	// orders table FK
	ordersTable := schema.Tables[1]
	var fkFound bool
	for _, c := range ordersTable.Constraints {
		if c.Type == "FOREIGN KEY" {
			fkFound = true
			assert.Equal(t, "fk_order_user", c.Name)
			assert.Equal(t, "users", c.RefTable)
			assert.Equal(t, "CASCADE", c.DeleteRule)
		}
	}
	assert.True(t, fkFound, "FK constraint should be found")

	// Generate PostgreSQL SQL
	pgParser := postgres.NewPostgreSQL()
	pgSQL, err := pgParser.Generate(schema)
	require.NoError(t, err)

	pgUpper := strings.ToUpper(pgSQL)

	// id should become SERIAL
	assert.Contains(t, pgUpper, "SERIAL")
	// int → INTEGER
	assert.Contains(t, pgUpper, "INTEGER")
	// varchar stays
	assert.Contains(t, pgUpper, "VARCHAR")
	// datetime → TIMESTAMP
	assert.Contains(t, pgUpper, "TIMESTAMP")
	// decimal stays
	assert.Contains(t, pgUpper, "DECIMAL")
	// text stays
	assert.Contains(t, pgUpper, "TEXT")
	// FK should be in output
	assert.Contains(t, pgUpper, "FOREIGN KEY")
	assert.Contains(t, pgUpper, "REFERENCES")
	// ENUM type definition should be created
	assert.Contains(t, pgSQL, "CREATE TYPE")
	assert.Contains(t, pgSQL, "AS ENUM")
}

// TestPostgresToMySQL validates that a realistic PostgreSQL dump converts correctly to MySQL.
func TestPostgresToMySQL(t *testing.T) {
	pgSQL := `
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(255) NOT NULL,
    role VARCHAR(20) NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin', 'moderator')),
    score NUMERIC(8,2),
    avatar BYTEA,
    metadata JSONB,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (email)
);

CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    user_id INTEGER NOT NULL,
    total DECIMAL(12,2) NOT NULL DEFAULT 0.00,
    CONSTRAINT fk_order_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE ON UPDATE CASCADE
);
`

	pgParser := postgres.NewPostgreSQL()
	schema, err := pgParser.Parse(pgSQL)
	require.NoError(t, err)
	require.NotNil(t, schema)

	assert.Len(t, schema.Tables, 2)

	// Verify users table
	usersTable := schema.Tables[0]
	assert.Equal(t, "users", usersTable.Name)

	// id: SERIAL → AutoIncrement=true
	idCol := usersTable.Columns[0]
	assert.True(t, idCol.AutoIncrement)
	assert.True(t, idCol.IsPrimaryKey)

	// NOT NULL columns
	nameCol := usersTable.Columns[1]
	assert.False(t, nameCol.IsNullable)

	// email NOT NULL
	emailCol := usersTable.Columns[2]
	assert.False(t, emailCol.IsNullable)

	// Verify orders FK constraint
	ordersTable := schema.Tables[1]
	var fkFound bool
	for _, c := range ordersTable.Constraints {
		if c.Type == "FOREIGN KEY" {
			fkFound = true
			assert.Equal(t, "fk_order_user", c.Name)
			assert.Equal(t, "users", c.RefTable)
			assert.Equal(t, "CASCADE", c.DeleteRule)
			assert.Equal(t, "CASCADE", c.UpdateRule)
		}
	}
	assert.True(t, fkFound, "FK constraint should be found")

	// Generate MySQL SQL
	mysqlParser := mysql.NewMySQL()
	mysqlSQL, err := mysqlParser.Generate(schema)
	require.NoError(t, err)

	sqlUpper := strings.ToUpper(mysqlSQL)

	// SERIAL → INT AUTO_INCREMENT
	assert.Contains(t, sqlUpper, "INT")
	assert.Contains(t, sqlUpper, "AUTO_INCREMENT")
	// INTEGER → INT
	assert.Contains(t, sqlUpper, "INT")
	// NUMERIC → DECIMAL
	assert.Contains(t, sqlUpper, "DECIMAL")
	// BYTEA → BLOB
	assert.Contains(t, sqlUpper, "BLOB")
	// JSONB → JSON
	assert.Contains(t, sqlUpper, "JSON")
	// TIMESTAMP → DATETIME
	assert.Contains(t, sqlUpper, "DATETIME")
	// VARCHAR stays
	assert.Contains(t, sqlUpper, "VARCHAR")
	// FK constraint should be preserved
	assert.Contains(t, sqlUpper, "FOREIGN KEY")
	assert.Contains(t, sqlUpper, "REFERENCES")
	assert.Contains(t, sqlUpper, "ON DELETE CASCADE")
}

// TestMySQLToPostgreSQLRoundtrip verifies key structural properties survive the conversion.
func TestMySQLToPostgreSQLRoundtrip(t *testing.T) {
	mysqlSQL := `
CREATE TABLE ` + "`" + `products` + "`" + ` (
  ` + "`" + `id` + "`" + ` int NOT NULL AUTO_INCREMENT,
  ` + "`" + `name` + "`" + ` varchar(200) NOT NULL,
  ` + "`" + `price` + "`" + ` decimal(10,2) NOT NULL DEFAULT 0.00,
  ` + "`" + `stock` + "`" + ` int NOT NULL DEFAULT 0,
  PRIMARY KEY (` + "`" + `id` + "`" + `)
) ENGINE=InnoDB;

CREATE TABLE ` + "`" + `categories` + "`" + ` (
  ` + "`" + `id` + "`" + ` int NOT NULL AUTO_INCREMENT,
  ` + "`" + `product_id` + "`" + ` int NOT NULL,
  ` + "`" + `name` + "`" + ` varchar(100) NOT NULL,
  PRIMARY KEY (` + "`" + `id` + "`" + `),
  CONSTRAINT ` + "`" + `fk_cat_product` + "`" + ` FOREIGN KEY (` + "`" + `product_id` + "`" + `) REFERENCES ` + "`" + `products` + "`" + ` (` + "`" + `id` + "`" + `) ON DELETE CASCADE
) ENGINE=InnoDB;
`

	// Parse MySQL
	mysqlParser := mysql.NewMySQL()
	schema, err := mysqlParser.Parse(mysqlSQL)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 2)

	// Generate PostgreSQL
	pgParser := postgres.NewPostgreSQL()
	pgSQL, err := pgParser.Generate(schema)
	require.NoError(t, err)

	// Parse the generated PostgreSQL to verify it is structurally valid
	pgParser2 := postgres.NewPostgreSQL()
	schema2, err := pgParser2.Parse(pgSQL)
	require.NoError(t, err)
	require.Len(t, schema2.Tables, 2, "roundtrip should produce same number of tables")

	// Table names preserved
	assert.Equal(t, schema.Tables[0].Name, schema2.Tables[0].Name)
	assert.Equal(t, schema.Tables[1].Name, schema2.Tables[1].Name)

	// Column counts preserved
	assert.Equal(t, len(schema.Tables[0].Columns), len(schema2.Tables[0].Columns))
	assert.Equal(t, len(schema.Tables[1].Columns), len(schema2.Tables[1].Columns))

	// FK still present after roundtrip
	var fkFound bool
	for _, c := range schema2.Tables[1].Constraints {
		if c.Type == "FOREIGN KEY" {
			fkFound = true
			assert.Equal(t, "CASCADE", c.DeleteRule)
		}
	}
	assert.True(t, fkFound, "FK constraint should survive MySQL→PostgreSQL roundtrip")
}

// TestPostgresToMySQLRoundtrip verifies key structural properties survive the conversion.
func TestPostgresToMySQLRoundtrip(t *testing.T) {
	pgSQL := `
CREATE TABLE departments (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL
);

CREATE TABLE employees (
    id SERIAL PRIMARY KEY,
    department_id INTEGER NOT NULL,
    name VARCHAR(200) NOT NULL,
    salary DECIMAL(10,2) NOT NULL DEFAULT 50000.00,
    CONSTRAINT fk_emp_dept FOREIGN KEY (department_id) REFERENCES departments (id) ON DELETE RESTRICT
);
`

	// Parse PostgreSQL
	pgParser := postgres.NewPostgreSQL()
	schema, err := pgParser.Parse(pgSQL)
	require.NoError(t, err)
	require.Len(t, schema.Tables, 2)

	// Generate MySQL
	mysqlParser := mysql.NewMySQL()
	mysqlSQL, err := mysqlParser.Generate(schema)
	require.NoError(t, err)

	// Parse the generated MySQL to verify it is structurally valid
	mysqlParser2 := mysql.NewMySQL()
	schema2, err := mysqlParser2.Parse(mysqlSQL)
	require.NoError(t, err)
	require.Len(t, schema2.Tables, 2, "roundtrip should produce same number of tables")

	// Table names preserved
	assert.Equal(t, schema.Tables[0].Name, schema2.Tables[0].Name)
	assert.Equal(t, schema.Tables[1].Name, schema2.Tables[1].Name)

	// Column counts preserved
	assert.Equal(t, len(schema.Tables[0].Columns), len(schema2.Tables[0].Columns))
	assert.Equal(t, len(schema.Tables[1].Columns), len(schema2.Tables[1].Columns))

	// FK preserved after roundtrip
	var fkFound bool
	for _, c := range schema2.Tables[1].Constraints {
		if c.Type == "FOREIGN KEY" {
			fkFound = true
			assert.Equal(t, "RESTRICT", c.DeleteRule)
		}
	}
	assert.True(t, fkFound, "FK constraint should survive PostgreSQL→MySQL roundtrip")
}

// TestPgDumpFormat verifies that a real pg_dump file (with COPY blocks,
// block comments, and ALTER TABLE ... ADD CONSTRAINT FKs) is parsed correctly.
func TestPgDumpFormat(t *testing.T) {
	pgDump := `
--
-- PostgreSQL database dump
--

SET statement_timeout = 0;
SET lock_timeout = 0;
SET client_encoding = 'UTF8';

--
-- Name: users; Type: TABLE
--

CREATE TABLE public.users (
    id integer NOT NULL,
    name character varying(100) NOT NULL,
    email character varying(255) NOT NULL,
    created_at timestamp without time zone DEFAULT CURRENT_TIMESTAMP NOT NULL
);

--
-- Name: orders; Type: TABLE
--

CREATE TABLE public.orders (
    id integer NOT NULL,
    user_id integer NOT NULL,
    total numeric(12,2) DEFAULT 0.00 NOT NULL
);

--
-- Data for Name: users; Type: TABLE DATA
--

COPY public.users (id, name, email, created_at) FROM stdin;
1	Alice	alice@example.com	2024-01-01 00:00:00
2	Bob	bob@example.com	2024-01-02 00:00:00
\.

COPY public.orders (id, user_id, total) FROM stdin;
1	1	99.99
\.

--
-- Name: users users_pkey; Type: CONSTRAINT
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);

--
-- Name: orders orders_pkey; Type: CONSTRAINT
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT orders_pkey PRIMARY KEY (id);

--
-- Name: users users_email_key; Type: CONSTRAINT
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_email_key UNIQUE (email);

--
-- Name: orders fk_order_user; Type: FK CONSTRAINT
--

ALTER TABLE ONLY public.orders
    ADD CONSTRAINT fk_order_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

--
-- PostgreSQL database dump complete
--
`

	pgParser := postgres.NewPostgreSQL()
	schema, err := pgParser.Parse(pgDump)
	require.NoError(t, err)
	require.NotNil(t, schema)

	require.Len(t, schema.Tables, 2)

	usersTable := schema.Tables[0]
	assert.Equal(t, "users", usersTable.Name)
	assert.Len(t, usersTable.Columns, 4)

	// id must not be nullable (set by ALTER TABLE PRIMARY KEY)
	assert.False(t, usersTable.Columns[0].IsNullable)
	assert.True(t, usersTable.Columns[0].IsPrimaryKey)

	// UNIQUE constraint from ALTER TABLE
	var uniqueFound bool
	for _, c := range usersTable.Constraints {
		if c.Type == "UNIQUE" && c.Name == "users_email_key" {
			uniqueFound = true
		}
	}
	assert.True(t, uniqueFound, "UNIQUE constraint from ALTER TABLE should be parsed")

	// FK on orders from ALTER TABLE
	ordersTable := schema.Tables[1]
	var fkFound bool
	for _, c := range ordersTable.Constraints {
		if c.Type == "FOREIGN KEY" {
			fkFound = true
			assert.Equal(t, "fk_order_user", c.Name)
			assert.Equal(t, "CASCADE", c.DeleteRule)
		}
	}
	assert.True(t, fkFound, "FK from ALTER TABLE should be parsed")

	// COPY data blocks must not create phantom tables
	assert.Len(t, schema.Tables, 2)

	// Convert to MySQL and verify basic output
	mysqlParser := mysql.NewMySQL()
	mysqlSQL, err := mysqlParser.Generate(schema)
	require.NoError(t, err)

	sqlUpper := strings.ToUpper(mysqlSQL)
	assert.Contains(t, sqlUpper, "CREATE TABLE")
	assert.Contains(t, sqlUpper, "FOREIGN KEY")
	assert.Contains(t, sqlUpper, "ON DELETE CASCADE")
}

// TestMysqldumpFormat verifies that a real mysqldump file (with /*!...*/
// conditional comments, LOCK/UNLOCK TABLES, and backtick identifiers) is parsed correctly.
func TestMysqldumpFormat(t *testing.T) {
	mysqldump := `
-- MySQL dump 10.13  Distrib 8.0.33
--
-- Host: localhost    Database: shop
-- Server version	8.0.33

/*!40101 SET @OLD_CHARACTER_SET_CLIENT=@@CHARACTER_SET_CLIENT */;
/*!40101 SET NAMES utf8mb4 */;
/*!40014 SET @OLD_UNIQUE_CHECKS=@@UNIQUE_CHECKS, UNIQUE_CHECKS=0 */;
/*!40014 SET @OLD_FOREIGN_KEY_CHECKS=@@FOREIGN_KEY_CHECKS, FOREIGN_KEY_CHECKS=0 */;

--
-- Table structure for table ` + "`" + `products` + "`" + `
--

DROP TABLE IF EXISTS ` + "`" + `products` + "`" + `;
/*!40101 SET @saved_cs_client = @@character_set_client */;
/*!40101 SET character_set_client = utf8mb4 */;
CREATE TABLE ` + "`" + `products` + "`" + ` (
  ` + "`" + `id` + "`" + ` int NOT NULL AUTO_INCREMENT,
  ` + "`" + `name` + "`" + ` varchar(200) NOT NULL,
  ` + "`" + `price` + "`" + ` decimal(10,2) NOT NULL DEFAULT '0.00',
  ` + "`" + `category` + "`" + ` enum('electronics','clothing','food') NOT NULL DEFAULT 'electronics',
  PRIMARY KEY (` + "`" + `id` + "`" + `),
  KEY ` + "`" + `idx_name` + "`" + ` (` + "`" + `name` + "`" + `)
) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb4;
/*!40101 SET character_set_client = @saved_cs_client */;

LOCK TABLES ` + "`" + `products` + "`" + ` WRITE;
/*!40000 ALTER TABLE ` + "`" + `products` + "`" + ` DISABLE KEYS */;
INSERT INTO ` + "`" + `products` + "`" + ` VALUES (1,'Widget',9.99,'electronics');
/*!40000 ALTER TABLE ` + "`" + `products` + "`" + ` ENABLE KEYS */;
UNLOCK TABLES;

--
-- Table structure for table ` + "`" + `orders` + "`" + `
--

DROP TABLE IF EXISTS ` + "`" + `orders` + "`" + `;
CREATE TABLE ` + "`" + `orders` + "`" + ` (
  ` + "`" + `id` + "`" + ` int NOT NULL AUTO_INCREMENT,
  ` + "`" + `product_id` + "`" + ` int NOT NULL,
  ` + "`" + `qty` + "`" + ` int NOT NULL DEFAULT '1',
  PRIMARY KEY (` + "`" + `id` + "`" + `),
  CONSTRAINT ` + "`" + `fk_order_product` + "`" + ` FOREIGN KEY (` + "`" + `product_id` + "`" + `) REFERENCES ` + "`" + `products` + "`" + ` (` + "`" + `id` + "`" + `) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

/*!40101 SET CHARACTER_SET_CLIENT=@OLD_CHARACTER_SET_CLIENT */;
/*!40014 SET FOREIGN_KEY_CHECKS=@OLD_FOREIGN_KEY_CHECKS */;
`

	mysqlParser := mysql.NewMySQL()
	schema, err := mysqlParser.Parse(mysqldump)
	require.NoError(t, err)
	require.NotNil(t, schema)

	require.Len(t, schema.Tables, 2)

	products := schema.Tables[0]
	assert.Equal(t, "products", products.Name)
	assert.Len(t, products.Columns, 4)

	// id: AUTO_INCREMENT
	assert.True(t, products.Columns[0].AutoIncrement)

	// category: ENUM with values
	catCol := products.Columns[3]
	assert.Equal(t, "enum", catCol.DataType)
	assert.Equal(t, []string{"electronics", "clothing", "food"}, catCol.EnumValues)

	// KEY idx_name should be parsed as an index
	assert.NotEmpty(t, products.Indexes, "KEY inside CREATE TABLE should become an index")

	// FK on orders
	orders := schema.Tables[1]
	var fkFound bool
	for _, c := range orders.Constraints {
		if c.Type == "FOREIGN KEY" {
			fkFound = true
			assert.Equal(t, "fk_order_product", c.Name)
			assert.Equal(t, "products", c.RefTable)
			assert.Equal(t, "CASCADE", c.DeleteRule)
		}
	}
	assert.True(t, fkFound, "FK from mysqldump should be parsed")

	// Convert to PostgreSQL
	pgParser := postgres.NewPostgreSQL()
	pgSQL, err := pgParser.Generate(schema)
	require.NoError(t, err)

	pgUpper := strings.ToUpper(pgSQL)
	assert.Contains(t, pgSQL, "CREATE TYPE") // ENUM type
	assert.Contains(t, pgSQL, "AS ENUM")
	assert.Contains(t, pgUpper, "SERIAL") // AUTO_INCREMENT → SERIAL
	assert.Contains(t, pgUpper, "FOREIGN KEY")
	assert.Contains(t, pgUpper, "ON DELETE CASCADE")
}
