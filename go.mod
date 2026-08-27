module github.com/mstgnz/sqlmapper

go 1.23

require github.com/stretchr/testify v1.10.0

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// v1.0.0 mis-parsed schema-qualified Oracle identifiers, silently truncated
// character varying(n) to a single character on the way out of PostgreSQL, and
// its release binaries were archives rather than executables. Use v1.1.0 or later.
retract v1.0.0
