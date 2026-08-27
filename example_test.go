package sqlmapper_test

import (
	"fmt"
	"log"
	"strings"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/stream"
)

// Converting a MySQL dump to PostgreSQL is two calls: parse the source into the
// dialect-neutral schema, then render that schema for the target.
func Example() {
	const dump = "CREATE TABLE `users` (\n" +
		"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
		"  `email` varchar(255) NOT NULL,\n" +
		"  `status` enum('active','banned') NOT NULL DEFAULT 'active',\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  UNIQUE KEY `uq_email` (`email`)\n" +
		") ENGINE=InnoDB;"

	schema, err := mysql.NewMySQL().Parse(dump)
	if err != nil {
		log.Fatal(err)
	}

	out, err := postgres.NewPostgreSQL().Generate(schema)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Print(out)
	// Output:
	// CREATE TYPE users_status_enum AS ENUM ('active', 'banned');
	// CREATE TABLE users (
	//     id BIGSERIAL PRIMARY KEY,
	//     email VARCHAR(255) NOT NULL,
	//     status users_status_enum NOT NULL DEFAULT 'active',
	//     CONSTRAINT uq_email UNIQUE (email)
	// );
}

// The parsed schema is an ordinary data structure, so a conversion can be
// inspected or edited before it is written back out.
func Example_inspectingTheSchema() {
	const dump = `
CREATE TABLE customers (
    id bigserial PRIMARY KEY,
    email character varying(255) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);`

	schema, err := postgres.NewPostgreSQL().Parse(dump)
	if err != nil {
		log.Fatal(err)
	}

	for _, col := range schema.Tables[0].Columns {
		fmt.Printf("%-12s %-10s nullable=%v\n", col.Name, col.DataType, col.IsNullable)
	}
	// Output:
	// id           bigint     nullable=false
	// email        varchar    nullable=false
	// created_at   timestamp with time zone nullable=false
}

// Dump tools do not order tables by dependency: mysqldump writes them
// alphabetically, so a child table routinely precedes its parent. Generators
// call OrderTablesByDependency to fix that before writing anything out.
func ExampleOrderTablesByDependency() {
	tables := []sqlmapper.Table{
		{
			Name: "orders",
			Constraints: []sqlmapper.Constraint{{
				Type: "FOREIGN KEY", Columns: []string{"user_id"},
				RefTable: "users", RefColumns: []string{"id"},
			}},
		},
		{Name: "users"},
	}

	ordered, deferred := sqlmapper.OrderTablesByDependency(tables)

	for _, t := range ordered {
		fmt.Println(t.Name)
	}
	fmt.Println("deferred:", len(deferred))
	// Output:
	// users
	// orders
	// deferred: 0
}

// A dump too large to hold in memory is read through a stream parser, which
// reports each object as it is found.
func Example_streaming() {
	const dump = `CREATE TABLE users (id INT AUTO_INCREMENT PRIMARY KEY);
CREATE TABLE orders (id INT AUTO_INCREMENT PRIMARY KEY);
CREATE VIEW active_users AS SELECT id FROM users;`

	err := mysql.NewMySQLStreamParser().ParseStream(strings.NewReader(dump),
		func(obj stream.SchemaObject) error {
			switch v := obj.Data.(type) {
			case *sqlmapper.Table:
				fmt.Println("table:", v.Name)
			case *sqlmapper.View:
				fmt.Println("view:", v.Name)
			}
			return nil
		})
	if err != nil {
		log.Fatal(err)
	}
	// Output:
	// table: users
	// table: orders
	// view: active_users
}
