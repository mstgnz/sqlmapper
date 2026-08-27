package sqlserver

import (
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realSSMSScript is in the shape SSMS "Generate Scripts" produces and that both
// SQL Server 2019 and 2022 accept verbatim: bracketed identifiers, an option
// block and a filegroup on every key and index, ASC in the key lists, defaults
// and foreign keys as separate ALTER TABLE statements, and a redundant
// CHECK CONSTRAINT statement after each one.
const realSSMSScript = `
CREATE TABLE [dbo].[customers](
	[id] [bigint] IDENTITY(1,1) NOT NULL,
	[email] [nvarchar](255) NOT NULL,
	[full_name] [nvarchar](4000) NULL,
	[is_active] [bit] NOT NULL,
	[score] [decimal](10, 2) NULL,
	[created_at] [datetime2](7) NOT NULL,
 CONSTRAINT [PK_customers] PRIMARY KEY CLUSTERED
(
	[id] ASC
)WITH (PAD_INDEX = OFF, STATISTICS_NORECOMPUTE = OFF, IGNORE_DUP_KEY = OFF, ALLOW_ROW_LOCKS = ON, ALLOW_PAGE_LOCKS = ON) ON [PRIMARY],
 CONSTRAINT [UQ_customers_email] UNIQUE NONCLUSTERED
(
	[email] ASC
)WITH (PAD_INDEX = OFF, STATISTICS_NORECOMPUTE = OFF, IGNORE_DUP_KEY = OFF, ALLOW_ROW_LOCKS = ON, ALLOW_PAGE_LOCKS = ON) ON [PRIMARY]
) ON [PRIMARY]
GO
CREATE TABLE [dbo].[invoices](
	[id] [bigint] IDENTITY(1,1) NOT NULL,
	[customer_id] [bigint] NOT NULL,
	[amount] [decimal](12, 2) NOT NULL,
	[issued_on] [date] NOT NULL,
 CONSTRAINT [PK_invoices] PRIMARY KEY CLUSTERED
(
	[id] ASC
)WITH (PAD_INDEX = OFF, STATISTICS_NORECOMPUTE = OFF, IGNORE_DUP_KEY = OFF, ALLOW_ROW_LOCKS = ON, ALLOW_PAGE_LOCKS = ON) ON [PRIMARY]
) ON [PRIMARY]
GO
CREATE NONCLUSTERED INDEX [IX_invoices_customer] ON [dbo].[invoices]
(
	[customer_id] ASC
)WITH (PAD_INDEX = OFF, STATISTICS_NORECOMPUTE = OFF, SORT_IN_TEMPDB = OFF, DROP_EXISTING = OFF, ONLINE = OFF, ALLOW_ROW_LOCKS = ON, ALLOW_PAGE_LOCKS = ON) ON [PRIMARY]
GO
ALTER TABLE [dbo].[customers] ADD  CONSTRAINT [DF_customers_is_active]  DEFAULT ((1)) FOR [is_active]
GO
ALTER TABLE [dbo].[customers] ADD  CONSTRAINT [DF_customers_created]  DEFAULT (sysutcdatetime()) FOR [created_at]
GO
ALTER TABLE [dbo].[invoices]  WITH CHECK ADD  CONSTRAINT [FK_invoices_customers] FOREIGN KEY([customer_id])
REFERENCES [dbo].[customers] ([id])
ON DELETE CASCADE
GO
ALTER TABLE [dbo].[invoices] CHECK CONSTRAINT [FK_invoices_customers]
GO
ALTER TABLE [dbo].[invoices]  WITH CHECK ADD  CONSTRAINT [CHK_amount] CHECK  (([amount]>=(0)))
GO
ALTER TABLE [dbo].[invoices] CHECK CONSTRAINT [CHK_amount]
GO
CREATE VIEW [dbo].[active_customers] AS SELECT id, email FROM dbo.customers WHERE is_active = 1
GO
`

func parseRealSSMS(t *testing.T) *sqlmapper.Schema {
	t.Helper()
	schema, err := NewSQLServer().Parse(realSSMSScript)
	require.NoError(t, err)
	return schema
}

func TestRealSSMS_Tables(t *testing.T) {
	schema := parseRealSSMS(t)
	require.Len(t, schema.Tables, 2)

	// The option blocks carry their own parentheses and commas; read as columns
	// they produced entries called STATISTICS_NORECOMPUTE and ALLOW_ROW_LOCKS.
	assert.Equal(t, "customers", schema.Tables[0].Name)
	assert.Len(t, schema.Tables[0].Columns, 6)
	assert.Equal(t, "invoices", schema.Tables[1].Name)
	assert.Len(t, schema.Tables[1].Columns, 4)
}

func TestRealSSMS_Columns(t *testing.T) {
	schema := parseRealSSMS(t)

	byName := map[string]sqlmapper.Column{}
	for _, c := range schema.Tables[0].Columns {
		byName[c.Name] = c
	}

	t.Run("identity becomes auto increment", func(t *testing.T) {
		// IDENTITY(1,1) carries a comma of its own; splitting on it produced a
		// phantom column called "1)".
		assert.True(t, byName["id"].AutoIncrement)
		assert.Equal(t, "bigint", byName["id"].DataType)
	})

	t.Run("bracketed types fold onto the shared vocabulary", func(t *testing.T) {
		assert.Equal(t, "varchar", byName["email"].DataType)
		assert.Equal(t, 255, byName["email"].Length)
		assert.Equal(t, "decimal", byName["score"].DataType)
		assert.Equal(t, 10, byName["score"].Length)
		assert.Equal(t, 2, byName["score"].Scale)
	})

	t.Run("bit is an integer flag", func(t *testing.T) {
		// Not boolean: SQL Server code compares a bit to 1, and view bodies are
		// carried over verbatim, so a real boolean target fails with
		// "operator does not exist: boolean = integer".
		assert.Equal(t, "smallint", byName["is_active"].DataType)
	})

	t.Run("datetime2 precision is not a length", func(t *testing.T) {
		// Carried through it produced DATETIME(7), which MySQL rejects: its
		// maximum fractional-seconds precision is 6.
		assert.Equal(t, "timestamp", byName["created_at"].DataType)
		assert.Equal(t, 0, byName["created_at"].Length)
	})

	t.Run("nullability is read from the definition", func(t *testing.T) {
		assert.False(t, byName["email"].IsNullable)
		assert.True(t, byName["full_name"].IsNullable)
	})
}

func TestRealSSMS_AlterTableForms(t *testing.T) {
	schema := parseRealSSMS(t)

	t.Run("a default belongs to its column", func(t *testing.T) {
		byName := map[string]sqlmapper.Column{}
		for _, c := range schema.Tables[0].Columns {
			byName[c.Name] = c
		}
		assert.Equal(t, "1", byName["is_active"].DefaultValue)
		assert.Equal(t, "CURRENT_TIMESTAMP", byName["created_at"].DefaultValue)

		// It must not also land in the constraint list as a nameless entry.
		for _, c := range schema.Tables[0].Constraints {
			assert.NotContains(t, strings.ToUpper(c.Name), "DF_",
				"a DEFAULT constraint is not a table constraint here")
		}
	})

	t.Run("foreign key and check are resolved", func(t *testing.T) {
		byType := map[string]sqlmapper.Constraint{}
		for _, c := range schema.Tables[1].Constraints {
			byType[c.Type] = c
		}

		require.Contains(t, byType, "FOREIGN KEY")
		fk := byType["FOREIGN KEY"]
		assert.Equal(t, "FK_invoices_customers", fk.Name)
		assert.Equal(t, []string{"customer_id"}, fk.Columns)
		assert.Equal(t, "customers", fk.RefTable)
		assert.Equal(t, "CASCADE", fk.DeleteRule)

		require.Contains(t, byType, "CHECK")
		assert.Equal(t, "amount>=(0)", byType["CHECK"].CheckExpression,
			"the brackets and the outer parentheses come off")
	})

	t.Run("keys survive the option blocks", func(t *testing.T) {
		byType := map[string]sqlmapper.Constraint{}
		for _, c := range schema.Tables[0].Constraints {
			byType[c.Type] = c
		}
		require.Contains(t, byType, "PRIMARY KEY")
		assert.Equal(t, []string{"id"}, byType["PRIMARY KEY"].Columns, "ASC is not part of the name")
		require.Contains(t, byType, "UNIQUE")
		assert.Equal(t, []string{"email"}, byType["UNIQUE"].Columns)
	})
}

func TestRealSSMS_IndexAndView(t *testing.T) {
	schema := parseRealSSMS(t)

	require.Len(t, schema.Tables[1].Indexes, 1)
	assert.Equal(t, "IX_invoices_customer", schema.Tables[1].Indexes[0].Name)
	assert.Equal(t, []string{"customer_id"}, schema.Tables[1].Indexes[0].Columns)

	require.Len(t, schema.Views, 1)
	assert.Contains(t, schema.Views[0].Name, "active_customers")
}

func TestSQLServerResolveType(t *testing.T) {
	s := &SQLServer{}

	tests := []struct {
		name string
		col  sqlmapper.Column
		want string
	}{
		{"varchar keeps its length", sqlmapper.Column{DataType: "varchar", Length: 255}, "NVARCHAR(255)"},
		{"text has no length", sqlmapper.Column{DataType: "text", Length: 100}, "NVARCHAR(MAX)"},
		{"boolean", sqlmapper.Column{DataType: "boolean"}, "BIT"},
		{"bigint", sqlmapper.Column{DataType: "bigint"}, "BIGINT"},
		{"decimal keeps precision", sqlmapper.Column{DataType: "decimal", Length: 12, Scale: 2}, "DECIMAL(12,2)"},
		{"jsonb has no native type", sqlmapper.Column{DataType: "jsonb"}, "NVARCHAR(MAX)"},
		{"uuid", sqlmapper.Column{DataType: "uuid"}, "UNIQUEIDENTIFIER"},
		{"timestamp", sqlmapper.Column{DataType: "timestamp"}, "DATETIME2"},
		{"timestamptz", sqlmapper.Column{DataType: "timestamptz"}, "DATETIMEOFFSET"},
		{"bytea", sqlmapper.Column{DataType: "bytea"}, "VARBINARY(MAX)"},
		{"array has no equivalent", sqlmapper.Column{DataType: "text", IsArray: true}, "NVARCHAR(MAX)"},
		{"enum", sqlmapper.Column{DataType: "enum"}, "NVARCHAR(255)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, s.resolveType(tt.col))
		})
	}
}

func TestSQLServerDefaultLiteral(t *testing.T) {
	s := &SQLServer{}

	assert.Equal(t, "1", s.defaultLiteral(sqlmapper.Column{DefaultValue: "true"}, "BIT"))
	assert.Equal(t, "0", s.defaultLiteral(sqlmapper.Column{DefaultValue: "false"}, "BIT"))
	assert.Equal(t, "SYSUTCDATETIME()", s.defaultLiteral(sqlmapper.Column{DefaultValue: "CURRENT_TIMESTAMP"}, "DATETIME2"))
	assert.Equal(t, "42", s.defaultLiteral(sqlmapper.Column{DefaultValue: "42"}, "INT"))
	assert.Equal(t, "'draft'", s.defaultLiteral(sqlmapper.Column{DefaultValue: "draft"}, "NVARCHAR(20)"))
	assert.Empty(t, s.defaultLiteral(sqlmapper.Column{DefaultValue: "NULL"}, "INT"))
}

func TestNormalizeSQLServerDefault(t *testing.T) {
	// SSMS wraps every default in at least one extra pair of parentheses.
	assert.Equal(t, "1", normalizeSQLServerDefault("((1))"))
	assert.Equal(t, "0", normalizeSQLServerDefault("((0))"))
	assert.Equal(t, "CURRENT_TIMESTAMP", normalizeSQLServerDefault("(sysutcdatetime())"))
	assert.Equal(t, "CURRENT_TIMESTAMP", normalizeSQLServerDefault("(getdate())"))
	assert.Equal(t, "draft", normalizeSQLServerDefault("('draft')"))
}

func TestSQLServerGenerateFromForeignSchema(t *testing.T) {
	schema := &sqlmapper.Schema{
		Tables: []sqlmapper.Table{
			{
				Name: "invoices",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "bigint", AutoIncrement: true, IsNullable: false},
					{Name: "customer_id", DataType: "bigint", IsNullable: false},
				},
				Constraints: []sqlmapper.Constraint{
					{Name: "invoices_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
					{Name: "fk_c", Type: "FOREIGN KEY", Columns: []string{"customer_id"},
						RefTable: "customers", RefColumns: []string{"id"}, DeleteRule: "CASCADE"},
				},
			},
			{
				Name: "customers",
				Columns: []sqlmapper.Column{
					{Name: "id", DataType: "bigint", AutoIncrement: true, IsNullable: false},
					{Name: "is_active", DataType: "boolean", DefaultValue: "true", IsNullable: false},
					{Name: "meta", DataType: "jsonb", IsNullable: true},
				},
				Constraints: []sqlmapper.Constraint{
					{Name: "customers_pkey", Type: "PRIMARY KEY", Columns: []string{"id"}},
				},
			},
		},
		Views: []sqlmapper.View{{Name: "v", Definition: "SELECT id FROM public.customers"}},
	}

	out, err := NewSQLServer().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "IDENTITY(1,1)")
	assert.Contains(t, out, "is_active BIT NOT NULL DEFAULT 1")
	assert.Contains(t, out, "meta NVARCHAR(MAX)")
	assert.Contains(t, out, "CONSTRAINT fk_c FOREIGN KEY (customer_id) REFERENCES customers (id) ON DELETE CASCADE")

	// The parent must precede the child even though the source listed it second.
	assert.Less(t, strings.Index(out, "CREATE TABLE customers"), strings.Index(out, "CREATE TABLE invoices"))

	// CREATE VIEW has to start its own batch or SQL Server rejects it with
	// "CREATE VIEW must be the first statement in a query batch".
	viewIdx := strings.Index(out, "CREATE VIEW")
	require.NotEqual(t, -1, viewIdx)
	assert.Contains(t, out[:viewIdx], "GO")
	assert.NotContains(t, out, "public.", "another dialect's default schema does not resolve here")
}
