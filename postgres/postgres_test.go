package postgres

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
)

func TestPostgreSQL_Parse(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantErr  bool
		validate func(*testing.T, *sqlmapper.Schema)
	}{
		{
			name:    "Empty content",
			content: "",
			wantErr: true,
		},
		{
			name: "SET and Configuration Commands",
			content: `
				SET search_path TO myschema;
				SET client_encoding = 'UTF8';
				SET timezone = 'Europe/Istanbul';
				ALTER SYSTEM SET work_mem = '16MB';
				ALTER DATABASE mydb SET timezone = 'UTC';`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.NotNil(t, schema)
				// SET statements are not parsed yet
			},
		},
		{
			name: "CREATE DATABASE",
			content: `
				CREATE DATABASE testdb
				WITH 
					OWNER = postgres
					ENCODING = 'UTF8'
					LC_COLLATE = 'en_US.UTF-8'
					LC_CTYPE = 'en_US.UTF-8'
					TABLESPACE = pg_default
					CONNECTION LIMIT = -1;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Equal(t, "testdb", schema.Name)
			},
		},
		{
			name: "CREATE SCHEMA with Variations",
			content: `
				CREATE SCHEMA IF NOT EXISTS myschema;
				CREATE SCHEMA myschema2 AUTHORIZATION postgres;
				CREATE SCHEMA myschema3 
				CREATE TABLE mytable (id int);`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Contains(t, []string{"myschema", "myschema2", "myschema3"}, schema.Name)
			},
		},
		{
			name: "CREATE TABLE with All Features",
			content: `
				CREATE TABLE orders (
					order_id SERIAL PRIMARY KEY,
					customer_id INTEGER NOT NULL,
					order_date TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
					status VARCHAR(20) CHECK (status IN ('new', 'processing', 'completed')),
					total_amount DECIMAL(10,2) DEFAULT 0.00,
					notes TEXT,
					CONSTRAINT fk_customer FOREIGN KEY (customer_id) REFERENCES customers(id) ON DELETE CASCADE,
					CONSTRAINT uq_order UNIQUE (order_id, customer_id)
				) TABLESPACE mytablespace;
				
				COMMENT ON TABLE orders IS 'Store orders table';
				COMMENT ON COLUMN orders.status IS 'Order current status';`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Tables, 1)
				table := schema.Tables[0]
				assert.Equal(t, "orders", table.Name)
				assert.Len(t, table.Columns, 6)
				assert.Len(t, table.Constraints, 4) // PK, FK, CHECK, UNIQUE
			},
		},
		{
			name: "ALTER TABLE Operations",
			content: `
				ALTER TABLE employees ADD COLUMN email VARCHAR(100);
				ALTER TABLE employees ALTER COLUMN salary SET DEFAULT 5000;
				ALTER TABLE employees ALTER COLUMN active SET NOT NULL;
				ALTER TABLE employees DROP COLUMN notes;
				ALTER TABLE employees ADD CONSTRAINT chk_salary CHECK (salary > 0);
				ALTER TABLE employees RENAME TO staff;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				// ALTER statements are not parsed yet
			},
		},
		{
			name: "DROP TABLE",
			content: `
				DROP TABLE IF EXISTS old_employees;
				DROP TABLE employees CASCADE;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				// DROP statements are not parsed yet
			},
		},
		{
			name: "CREATE INDEX with Variations",
			content: `
				CREATE INDEX idx_employee_name ON employees(last_name, first_name);
				CREATE UNIQUE INDEX idx_employee_email ON employees(email) WHERE active = true;
				CREATE INDEX idx_employee_salary ON employees USING btree (salary DESC NULLS LAST);
				CREATE INDEX idx_employee_document ON employees USING gin (document jsonb_path_ops);`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				// Indexes attach to a table, so the table has to be created first
			},
		},
		{
			name: "CREATE SEQUENCE",
			content: `
				CREATE SEQUENCE order_seq
					INCREMENT BY 1
					MINVALUE 1
					MAXVALUE 999999
					START WITH 1
					CACHE 20
					CYCLE;
				
				ALTER SEQUENCE order_seq RESTART WITH 1000;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Sequences, 1)
				seq := schema.Sequences[0]
				assert.Equal(t, "order_seq", seq.Name)
			},
		},
		{
			name: "CREATE VIEW and MATERIALIZED VIEW",
			content: `
				CREATE OR REPLACE VIEW employee_summary AS
				SELECT 
					department_id,
					COUNT(*) as employee_count,
					AVG(salary) as avg_salary
				FROM employees
				GROUP BY department_id;

				CREATE MATERIALIZED VIEW monthly_sales WITH (fillfactor=80) AS
				SELECT 
					date_trunc('month', order_date) as month,
					SUM(total_amount) as total_sales
				FROM orders
				GROUP BY date_trunc('month', order_date)
				WITH DATA;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Views, 2)
				assert.Equal(t, "employee_summary", schema.Views[0].Name)
				assert.Equal(t, "monthly_sales", schema.Views[1].Name)
			},
		},
		{
			name: "CREATE FUNCTION and PROCEDURE",
			content: `
				CREATE OR REPLACE FUNCTION update_employee_status()
				RETURNS TRIGGER AS $$
				BEGIN
					NEW.updated_at := CURRENT_TIMESTAMP;
					RETURN NEW;
				END;
				$$ LANGUAGE plpgsql;

				CREATE OR REPLACE PROCEDURE process_payroll(
					month INTEGER,
					year INTEGER
				)
				LANGUAGE plpgsql
				AS $$
				BEGIN
					-- Process payroll logic
					COMMIT;
				END;
				$$;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Functions, 2)
				assert.Equal(t, "update_employee_status", schema.Functions[0].Name)
				assert.Equal(t, "process_payroll", schema.Functions[1].Name)
			},
		},
		{
			name: "CREATE TRIGGER",
			content: `
				CREATE TRIGGER update_employee_timestamp
				BEFORE UPDATE ON employees
				FOR EACH ROW
				EXECUTE FUNCTION update_employee_status();

				CREATE TRIGGER check_salary_changes
				AFTER UPDATE OF salary ON employees
				FOR EACH ROW
				WHEN (OLD.salary IS DISTINCT FROM NEW.salary)
				EXECUTE FUNCTION audit_salary_changes();`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Triggers, 2)
				assert.Equal(t, "update_employee_timestamp", schema.Triggers[0].Name)
				assert.Equal(t, "check_salary_changes", schema.Triggers[1].Name)
			},
		},
		{
			name: "CREATE TYPE",
			content: `
				CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy');
				
				CREATE TYPE complex AS (
					r       double precision,
					t       double precision
				);
				
				CREATE TYPE inventory_item AS (
					name            text,
					supplier_id     integer,
					price           numeric
				);`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Types, 3)
				assert.Equal(t, "mood", schema.Types[0].Name)
				assert.Equal(t, "complex", schema.Types[1].Name)
				assert.Equal(t, "inventory_item", schema.Types[2].Name)
			},
		},
		{
			name: "CREATE EXTENSION",
			content: `
				CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
				CREATE EXTENSION postgis;
				CREATE EXTENSION pg_trgm WITH SCHEMA public;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Extensions, 3)
				assert.Equal(t, "uuid-ossp", schema.Extensions[0].Name)
				assert.Equal(t, "postgis", schema.Extensions[1].Name)
				assert.Equal(t, "pg_trgm", schema.Extensions[2].Name)
			},
		},
		{
			name: "GRANT and REVOKE",
			content: `
				GRANT SELECT, INSERT ON employees TO hr_group;
				GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO admin_role;
				REVOKE UPDATE ON employees FROM intern_group;
				GRANT EXECUTE ON FUNCTION calculate_salary(integer) TO payroll_group;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Permissions, 4)
			},
		},
		{
			name: "COMMENT",
			content: `
				COMMENT ON TABLE employees IS 'Company employees';
				COMMENT ON COLUMN employees.salary IS 'Monthly salary in USD';
				COMMENT ON FUNCTION calculate_salary(integer) IS 'Calculates employee salary with bonuses';`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				// Comments belong in the Comment field of the object they describe
			},
		},
		{
			name: "Foreign Key Constraints",
			content: `
				CREATE TABLE departments (
					id SERIAL PRIMARY KEY,
					name VARCHAR(100) NOT NULL
				);

				CREATE TABLE employees (
					id SERIAL PRIMARY KEY,
					department_id INTEGER,
					manager_id INTEGER,
					CONSTRAINT fk_department 
						FOREIGN KEY (department_id) 
						REFERENCES departments(id) 
						ON DELETE CASCADE 
						ON UPDATE CASCADE,
					CONSTRAINT fk_manager 
						FOREIGN KEY (manager_id) 
						REFERENCES employees(id) 
						ON DELETE SET NULL
				);`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Tables, 2)
				empTable := schema.Tables[1]
				assert.Len(t, empTable.Constraints, 3) // PK + 2 FK

				var fkCount int
				for _, c := range empTable.Constraints {
					if c.Type == "FOREIGN KEY" {
						fkCount++
						if c.Columns[0] == "department_id" {
							assert.Equal(t, "CASCADE", c.DeleteRule)
						} else if c.Columns[0] == "manager_id" {
							assert.Equal(t, "SET NULL", c.DeleteRule)
						}
					}
				}
				assert.Equal(t, 2, fkCount)
			},
		},
		{
			name: "SET CONSTRAINTS",
			content: `
				SET CONSTRAINTS ALL DEFERRED;
				SET CONSTRAINTS fk_department IMMEDIATE;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				// SET CONSTRAINTS statements are not parsed yet
			},
		},
		{
			name: "INSERT INTO",
			content: `
				INSERT INTO departments (name) VALUES ('IT'), ('HR'), ('Sales');
				
				INSERT INTO employees (name, department_id, salary) 
				VALUES 
					('John Doe', 1, 5000),
					('Jane Smith', 2, 6000)
				ON CONFLICT (id) DO UPDATE 
				SET salary = EXCLUDED.salary;
				
				INSERT INTO employees 
				SELECT * FROM temp_employees 
				WHERE hire_date > '2023-01-01';`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				// INSERT statements are not parsed yet
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPostgreSQL()
			schema, err := p.Parse(tt.content)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, schema)

			if tt.validate != nil {
				tt.validate(t, schema)
			}
		})
	}
}

func TestPostgreSQL_Generate(t *testing.T) {
	tests := []struct {
		name    string
		schema  *sqlmapper.Schema
		want    string
		wantErr bool
	}{
		{
			name:    "Nil schema",
			schema:  nil,
			wantErr: true,
		},
		{
			name: "Basic schema with one table",
			schema: &sqlmapper.Schema{
				Tables: []sqlmapper.Table{
					{
						Name: "users",
						Columns: []sqlmapper.Column{
							{Name: "id", DataType: "SERIAL", IsPrimaryKey: true},
							{Name: "name", DataType: "VARCHAR", Length: 100, IsNullable: false},
							{Name: "email", DataType: "VARCHAR", Length: 255, IsNullable: false, IsUnique: true},
						},
					},
				},
			},
			want: strings.TrimSpace(`
CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE
);`),
			wantErr: false,
		},
		{
			name: "Schema with table and indexes",
			schema: &sqlmapper.Schema{
				Tables: []sqlmapper.Table{
					{
						Name: "products",
						Columns: []sqlmapper.Column{
							{Name: "id", DataType: "SERIAL", IsPrimaryKey: true},
							{Name: "name", DataType: "VARCHAR", Length: 100, IsNullable: false},
							{Name: "price", DataType: "NUMERIC", Length: 10, Scale: 2, IsNullable: true},
						},
						Indexes: []sqlmapper.Index{
							{Name: "idx_name", Columns: []string{"name"}},
							{Name: "idx_price", Columns: []string{"price"}, IsUnique: true},
						},
					},
				},
			},
			want: strings.TrimSpace(`
CREATE TABLE products (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    price NUMERIC(10,2)
);
CREATE INDEX idx_name ON products(name);
CREATE UNIQUE INDEX idx_price ON products(price);`),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewPostgreSQL()
			result, err := s.Generate(tt.schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("Generate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.want != "" {
				assert.Equal(t, tt.want, strings.TrimSpace(result))
			}
		})
	}
}

// A PL/pgSQL body is wrapped in dollar quotes precisely because it contains
// semicolons. Splitting on the semicolon cut it at its first inner statement.
func TestPostgreSQL_ParseRoutineBody(t *testing.T) {
	const ddl = `CREATE TABLE users (id bigint PRIMARY KEY);
CREATE FUNCTION bump(n integer) RETURNS integer AS $$
BEGIN
  n := n + 1;
  RETURN n;
END;
$$ LANGUAGE plpgsql;
CREATE TABLE orders (id bigint PRIMARY KEY);`

	schema, err := NewPostgreSQL().Parse(ddl)
	assert.NoError(t, err)

	assert.Len(t, schema.Tables, 2, "the table after the function has to survive")
	assert.Equal(t, "orders", schema.Tables[1].Name)

	assert.Len(t, schema.Functions, 1)
	assert.Contains(t, schema.Functions[0].Body, "RETURN n", "the whole body has to survive")
}

// CREATE TABLESPACE begins with the CREATE TABLE keyword as a prefix, and must
// not be read as a table.
func TestPostgreSQL_TablespaceIsNotATable(t *testing.T) {
	const ddl = `CREATE TABLESPACE fastspace LOCATION '/ssd/pg';
CREATE TABLE users (id bigint PRIMARY KEY);`

	schema, err := NewPostgreSQL().Parse(ddl)
	assert.NoError(t, err)

	assert.Len(t, schema.Tables, 1)
	assert.Equal(t, "users", schema.Tables[0].Name)
}
