package oracle

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
)

func TestOracle_Parse(t *testing.T) {
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
			name: "CREATE TABLE with All Features",
			content: `
				CREATE TABLE users (
					id NUMBER DEFAULT users_seq.NEXTVAL PRIMARY KEY,
					username VARCHAR2(50) NOT NULL UNIQUE,
					email VARCHAR2(100) NOT NULL,
					password VARCHAR2(255) NOT NULL,
					status VARCHAR2(20) DEFAULT 'active' CHECK (status IN ('active', 'inactive')),
					created_at TIMESTAMP DEFAULT SYSTIMESTAMP,
					updated_at TIMESTAMP DEFAULT SYSTIMESTAMP
				);
				
				CREATE TABLE posts (
					id NUMBER DEFAULT posts_seq.NEXTVAL PRIMARY KEY,
					user_id NUMBER NOT NULL,
					title VARCHAR2(255) NOT NULL,
					content CLOB,
					status VARCHAR2(20) DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
					created_at TIMESTAMP DEFAULT SYSTIMESTAMP,
					updated_at TIMESTAMP DEFAULT SYSTIMESTAMP,
					CONSTRAINT fk_posts_users FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
				);`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Tables, 2)

				// Check the users table
				usersTable := schema.Tables[0]
				assert.Equal(t, "users", usersTable.Name)
				assert.Len(t, usersTable.Columns, 7)

				// Check the posts table
				postsTable := schema.Tables[1]
				assert.Equal(t, "posts", postsTable.Name)
				assert.Len(t, postsTable.Columns, 7)

				// Check the foreign key
				fkFound := false
				for _, constraint := range postsTable.Constraints {
					if constraint.Type == "FOREIGN KEY" {
						fkFound = true
						assert.Equal(t, "fk_posts_users", constraint.Name)
						assert.Equal(t, []string{"user_id"}, constraint.Columns)
						assert.Equal(t, "users", constraint.RefTable)
						assert.Equal(t, []string{"id"}, constraint.RefColumns)
						assert.Equal(t, "CASCADE", constraint.DeleteRule)
					}
				}
				assert.True(t, fkFound, "Foreign key constraint not found")
			},
		},
		{
			name: "CREATE SEQUENCE",
			content: `
				CREATE SEQUENCE users_seq START WITH 1 INCREMENT BY 1;
				CREATE SEQUENCE posts_seq START WITH 1 INCREMENT BY 1;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Sequences, 2)
				assert.Equal(t, "users_seq", schema.Sequences[0].Name)
				assert.Equal(t, "posts_seq", schema.Sequences[1].Name)
			},
		},
		{
			name: "CREATE VIEW",
			content: `
				CREATE OR REPLACE VIEW active_users_view AS
				SELECT 
					u.*,
					COUNT(p.id) as post_count,
					MAX(p.created_at) as last_post_date
				FROM users u
				LEFT JOIN posts p ON u.id = p.user_id
				WHERE u.status = 'active'
				GROUP BY u.id, u.username, u.email, u.password, u.status, u.created_at, u.updated_at;`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Views, 1)
				assert.Equal(t, "active_users_view", schema.Views[0].Name)
			},
		},
		{
			name: "CREATE TRIGGER",
			content: `
				CREATE OR REPLACE TRIGGER users_update_timestamp
				BEFORE UPDATE ON users
				FOR EACH ROW
				BEGIN
					:NEW.updated_at := SYSTIMESTAMP;
				END;
				/
				
				CREATE OR REPLACE TRIGGER posts_update_timestamp
				BEFORE UPDATE ON posts
				FOR EACH ROW
				BEGIN
					:NEW.updated_at := SYSTIMESTAMP;
				END;
				/`,
			wantErr: false,
			validate: func(t *testing.T, schema *sqlmapper.Schema) {
				assert.Len(t, schema.Triggers, 2)

				// Check the users trigger
				usersTrigger := schema.Triggers[0]
				assert.Equal(t, "users_update_timestamp", usersTrigger.Name)
				assert.Equal(t, "users", usersTrigger.Table)
				assert.Equal(t, "BEFORE", usersTrigger.Timing)
				assert.Equal(t, "UPDATE", usersTrigger.Event)
				assert.True(t, usersTrigger.ForEachRow)

				// Check the posts trigger
				postsTrigger := schema.Triggers[1]
				assert.Equal(t, "posts_update_timestamp", postsTrigger.Name)
				assert.Equal(t, "posts", postsTrigger.Table)
				assert.Equal(t, "BEFORE", postsTrigger.Timing)
				assert.Equal(t, "UPDATE", postsTrigger.Event)
				assert.True(t, postsTrigger.ForEachRow)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewOracle()
			schema, err := p.Parse(tt.content)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			if tt.validate != nil {
				tt.validate(t, schema)
			}
		})
	}
}

func TestOracle_Generate(t *testing.T) {
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
							{Name: "id", DataType: "NUMBER", IsPrimaryKey: true},
							{Name: "username", DataType: "VARCHAR2", Length: 50, IsNullable: false, IsUnique: true},
							{Name: "email", DataType: "VARCHAR2", Length: 100, IsNullable: false},
						},
					},
				},
			},
			want: strings.TrimSpace(`
CREATE TABLE users (
    id NUMBER PRIMARY KEY,
    username VARCHAR2(50) NOT NULL UNIQUE,
    email VARCHAR2(100) NOT NULL
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
							{Name: "id", DataType: "NUMBER", IsPrimaryKey: true},
							{Name: "name", DataType: "VARCHAR2", Length: 100, IsNullable: false},
							{Name: "price", DataType: "NUMBER", Length: 10, Scale: 2, IsNullable: true},
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
    id NUMBER PRIMARY KEY,
    name VARCHAR2(100) NOT NULL,
    price NUMBER(10,2)
);
CREATE INDEX idx_name ON products (name);
CREATE UNIQUE INDEX idx_price ON products (price);`),
			wantErr: false,
		},
		{
			name: "Full schema",
			schema: &sqlmapper.Schema{
				Tables: []sqlmapper.Table{
					{
						Name: "users",
						Columns: []sqlmapper.Column{
							{Name: "id", DataType: "NUMBER", IsPrimaryKey: true},
							{Name: "username", DataType: "VARCHAR2", Length: 50, IsNullable: false, IsUnique: true},
							{Name: "email", DataType: "VARCHAR2", Length: 100, IsNullable: false},
							{Name: "status", DataType: "VARCHAR2", Length: 20, IsNullable: false, DefaultValue: "active"},
						},
					},
				},
				Views: []sqlmapper.View{
					{
						Name:       "active_users_view",
						Definition: "SELECT u.*, COUNT(p.id) as post_count FROM users u LEFT JOIN posts p ON u.id = p.user_id WHERE u.status = 'active' GROUP BY u.id",
					},
				},
			},
			want: strings.TrimSpace(`
CREATE TABLE users (
    id NUMBER PRIMARY KEY,
    username VARCHAR2(50) NOT NULL UNIQUE,
    email VARCHAR2(100) NOT NULL,
    status VARCHAR2(20) DEFAULT 'active' NOT NULL
);

CREATE OR REPLACE VIEW active_users_view AS
SELECT u.*, COUNT(p.id) as post_count FROM users u LEFT JOIN posts p ON u.id = p.user_id WHERE u.status = 'active' GROUP BY u.id;`),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewOracle()
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

// A PL/SQL body carries semicolons of its own, and Oracle marks its end with a
// slash on a line by itself. Splitting on the semicolon cut the body at its
// first inner statement and lost everything after it in the file.
func TestOracle_ParseRoutineBody(t *testing.T) {
	const ddl = `CREATE TABLE users (id NUMBER PRIMARY KEY)
/
CREATE OR REPLACE TRIGGER touch_users BEFORE INSERT ON users FOR EACH ROW
BEGIN
  :NEW.id := 1;
  :NEW.id := :NEW.id + 1;
END;
/
CREATE TABLE orders (id NUMBER PRIMARY KEY)
/`

	schema, err := NewOracle().Parse(ddl)
	assert.NoError(t, err)

	assert.Len(t, schema.Tables, 2, "the table after the trigger has to survive")
	assert.Equal(t, "orders", schema.Tables[1].Name)

	assert.Len(t, schema.Triggers, 1)
	assert.Contains(t, schema.Triggers[0].Body, ":NEW.id + 1", "the whole body has to survive")
}

// CREATE TABLESPACE begins with the CREATE TABLE keyword as a prefix, and a
// dispatcher that matched on the prefix alone sent it to the table parser, which
// went looking for a column list that is not there.
func TestOracle_TablespaceIsNotATable(t *testing.T) {
	const ddl = `CREATE TABLESPACE example_data
DATAFILE 'example_data.dbf' SIZE 100M
AUTOEXTEND ON NEXT 50M MAXSIZE UNLIMITED;

CREATE TABLE users (
    id NUMBER PRIMARY KEY,
    email VARCHAR2(255) NOT NULL
);`

	schema, err := NewOracle().Parse(ddl)
	assert.NoError(t, err)

	assert.Len(t, schema.Tables, 1)
	assert.Equal(t, "users", schema.Tables[0].Name)
}
