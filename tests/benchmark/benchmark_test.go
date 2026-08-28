// Package benchmark measures the cost of reading and writing a schema.
//
// Every benchmark builds its parser inside the loop. Reusing one across
// iterations measured a schema growing by one copy of the fixture on every
// pass, which made the numbers a curve rather than a rate.
package benchmark

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/mstgnz/sqlmapper"
	"github.com/mstgnz/sqlmapper/mysql"
	"github.com/mstgnz/sqlmapper/oracle"
	"github.com/mstgnz/sqlmapper/postgres"
	"github.com/mstgnz/sqlmapper/sqlite"
	"github.com/mstgnz/sqlmapper/sqlserver"
	"github.com/mstgnz/sqlmapper/stream"
)

type dialect struct {
	name   string
	dump   string
	parser func() sqlmapper.Database
	stream func() stream.StreamParser
}

var dialects = []dialect{
	{"mysql", mysqlDump, mysql.NewMySQL,
		func() stream.StreamParser { return mysql.NewMySQLStreamParser() }},
	{"postgres", postgresDump, postgres.NewPostgreSQL,
		func() stream.StreamParser { return postgres.NewPostgreSQLStreamParser() }},
	{"sqlite", sqliteDump, sqlite.NewSQLite,
		func() stream.StreamParser { return sqlite.NewSQLiteStreamParser() }},
	{"oracle", oracleDump, oracle.NewOracle,
		func() stream.StreamParser { return oracle.NewOracleStreamParser() }},
	{"sqlserver", sqlserverDump, sqlserver.NewSQLServer,
		func() stream.StreamParser { return sqlserver.NewSQLServerStreamParser() }},
}

// BenchmarkParse reads a dump of the same shape in every dialect, so the
// numbers can be compared across them.
func BenchmarkParse(b *testing.B) {
	for _, d := range dialects {
		b.Run(d.name, func(b *testing.B) {
			b.SetBytes(int64(len(d.dump)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := d.parser().Parse(d.dump); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkGenerate writes the same schema out in every dialect.
func BenchmarkGenerate(b *testing.B) {
	schema, err := postgres.NewPostgreSQL().Parse(postgresDump)
	if err != nil {
		b.Fatal(err)
	}

	for _, d := range dialects {
		b.Run(d.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := d.parser().Generate(schema); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkConvert is what the command does: read one dialect, write another.
func BenchmarkConvert(b *testing.B) {
	pairs := []struct{ from, to dialect }{
		{dialects[0], dialects[1]}, // mysql -> postgres
		{dialects[1], dialects[0]}, // postgres -> mysql
		{dialects[1], dialects[3]}, // postgres -> oracle
		{dialects[4], dialects[1]}, // sqlserver -> postgres
		{dialects[2], dialects[1]}, // sqlite -> postgres
	}

	for _, p := range pairs {
		b.Run(p.from.name+"_to_"+p.to.name, func(b *testing.B) {
			b.SetBytes(int64(len(p.from.dump)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				schema, err := p.from.parser().Parse(p.from.dump)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := p.to.parser().Generate(schema); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkParseStream measures the reader that exists for dumps too large to
// hold in memory, against the whole-file reader on the same input.
func BenchmarkParseStream(b *testing.B) {
	for _, d := range dialects {
		b.Run(d.name, func(b *testing.B) {
			b.SetBytes(int64(len(d.dump)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				err := d.stream().ParseStream(strings.NewReader(d.dump),
					func(stream.SchemaObject) error { return nil })
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkGenerateStream writes through the streaming path.
func BenchmarkGenerateStream(b *testing.B) {
	schema, err := postgres.NewPostgreSQL().Parse(postgresDump)
	if err != nil {
		b.Fatal(err)
	}

	for _, d := range dialects {
		b.Run(d.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := d.stream().GenerateStream(schema, io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkParseScaling reads schemas of growing size. Watch the MB/s column
// rather than the time: it has to stay flat. A parser that rescans what it has
// already read turns a large dump into a wait rather than a slowdown, and two
// of them did, looking for comments over the whole file once per table. At 500
// tables that was two seconds, of which all but forty milliseconds was the
// rescanning.
func BenchmarkParseScaling(b *testing.B) {
	for _, d := range scalingDialects {
		for _, tables := range []int{10, 100, 500} {
			dump := d.dump(tables)
			b.Run(fmt.Sprintf("%s/%d_tables", d.name, tables), func(b *testing.B) {
				b.SetBytes(int64(len(dump)))
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					schema, err := d.parser().Parse(dump)
					if err != nil {
						b.Fatal(err)
					}
					if len(schema.Tables) != tables {
						b.Fatalf("read %d tables, wrote %d", len(schema.Tables), tables)
					}
				}
			})
		}
	}
}

var scalingDialects = []struct {
	name   string
	parser func() sqlmapper.Database
	dump   func(n int) string
}{
	{"mysql", mysql.NewMySQL, generatedMySQLDump},
	{"postgres", postgres.NewPostgreSQL, generatedPostgresDump},
	{"sqlite", sqlite.NewSQLite, generatedSQLiteDump},
	{"oracle", oracle.NewOracle, generatedOracleDump},
	{"sqlserver", sqlserver.NewSQLServer, generatedSQLServerDump},
}

// BenchmarkOrderTablesByDependency measures the sort every generator runs
// before it writes anything.
func BenchmarkOrderTablesByDependency(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		tables := chainedTables(n)
		b.Run(fmt.Sprintf("%d_tables", n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sqlmapper.OrderTablesByDependency(tables)
			}
		})
	}
}

// generatedMySQLDump writes a dump of n tables, each with a key into the one
// before it, which is the shape that makes a generator order them.
func generatedMySQLDump(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "CREATE TABLE `t%d` (\n"+
			"  `id` bigint NOT NULL AUTO_INCREMENT,\n"+
			"  `name` varchar(255) NOT NULL,\n"+
			"  `amount` decimal(12,2) DEFAULT '0.00',\n"+
			"  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,\n"+
			"  PRIMARY KEY (`id`),\n"+
			"  UNIQUE KEY `uq_t%d_name` (`name`)\n"+
			") ENGINE=InnoDB;\n", i, i)
	}
	return b.String()
}

// chainedTables builds n tables where each refers to the one before it, the
// worst case for a dependency sort that has to walk the chain.
func chainedTables(n int) []sqlmapper.Table {
	tables := make([]sqlmapper.Table, 0, n)
	for i := n - 1; i >= 0; i-- {
		t := sqlmapper.Table{
			Name:    fmt.Sprintf("t%d", i),
			Columns: []sqlmapper.Column{{Name: "id", DataType: "bigint"}},
		}
		if i > 0 {
			t.Constraints = []sqlmapper.Constraint{{
				Type: "FOREIGN KEY", Columns: []string{"parent_id"},
				RefTable: fmt.Sprintf("t%d", i-1), RefColumns: []string{"id"},
			}}
		}
		tables = append(tables, t)
	}
	return tables
}

func generatedPostgresDump(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "CREATE TABLE public.t%d (\n"+
			"    id bigint NOT NULL,\n"+
			"    name character varying(255) NOT NULL,\n"+
			"    amount numeric(12,2) DEFAULT 0,\n"+
			"    created_at timestamp with time zone DEFAULT now() NOT NULL\n"+
			");\n"+
			"ALTER TABLE ONLY public.t%d ADD CONSTRAINT t%d_pkey PRIMARY KEY (id);\n"+
			"COMMENT ON TABLE public.t%d IS 'table %d';\n", i, i, i, i, i)
	}
	return b.String()
}

func generatedSQLiteDump(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "CREATE TABLE t%d (\n"+
			"    id INTEGER PRIMARY KEY AUTOINCREMENT,\n"+
			"    name TEXT NOT NULL,\n"+
			"    amount NUMERIC(12,2) DEFAULT 0,\n"+
			"    CONSTRAINT uq_t%d_name UNIQUE (name)\n"+
			");\n", i, i)
	}
	return b.String()
}

func generatedOracleDump(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "  CREATE TABLE \"APP\".\"T%d\"\n"+
			"   (\t\"ID\" NUMBER GENERATED BY DEFAULT AS IDENTITY NOT NULL ENABLE,\n"+
			"\t\"NAME\" VARCHAR2(255) NOT NULL ENABLE,\n"+
			"\t\"AMOUNT\" NUMBER(12,2) DEFAULT 0,\n"+
			"\t CONSTRAINT \"PK_T%d\" PRIMARY KEY (\"ID\")\n"+
			"  USING INDEX  ENABLE\n"+
			"   ) ;\n\n", i, i)
	}
	return b.String()
}

func generatedSQLServerDump(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "CREATE TABLE [dbo].[t%d](\n"+
			"\t[id] [bigint] IDENTITY(1,1) NOT NULL,\n"+
			"\t[name] [nvarchar](255) NOT NULL,\n"+
			"\t[amount] [decimal](12, 2) NULL,\n"+
			" CONSTRAINT [PK_t%d] PRIMARY KEY CLUSTERED ([id] ASC)\n"+
			") ON [PRIMARY]\nGO\n", i, i)
	}
	return b.String()
}
