package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realDumpFunction is the shape pg_dump actually writes, taken from a
// PostgreSQL 17 server. The attributes sit between RETURNS and the body, which
// is the opposite of the order hand-written SQL usually uses.
const realDumpFunction = `CREATE TABLE public.users (
    id bigint NOT NULL,
    n integer,
    updated_at timestamp with time zone
);

CREATE FUNCTION public.touch() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  NEW.n = COALESCE(NEW.n, 0) + 2;
  NEW.updated_at = now();
  RETURN NEW;
END;
$$;

CREATE TRIGGER touch_users BEFORE INSERT ON public.users FOR EACH ROW EXECUTE FUNCTION public.touch();
`

// Insisting on "AS $$ ... $$ LANGUAGE x" meant no function in a real dump was
// found at all, and the trigger was left calling a function that never arrived.
func TestPostgreSQL_RealDumpFunction(t *testing.T) {
	schema, err := NewPostgreSQL().Parse(realDumpFunction)
	require.NoError(t, err)

	require.Len(t, schema.Functions, 1, "a dumped function has to be found")
	fn := schema.Functions[0]
	assert.Equal(t, "touch", fn.Name)
	assert.Equal(t, "public", fn.Schema)
	assert.Equal(t, "trigger", fn.Returns, "the attributes are not part of the return type")
	assert.Equal(t, "plpgsql", fn.Language)
	assert.Contains(t, fn.Body, "RETURN NEW")

	require.Len(t, schema.Triggers, 1)
	assert.Equal(t, "touch_users", schema.Triggers[0].Name)
}

// The function and the trigger that runs it have to agree on the name. The
// function is written out unqualified, so a trigger keeping the schema
// qualifier called something that was never created outside public.
func TestPostgreSQL_RealDumpRoundTrip(t *testing.T) {
	schema, err := NewPostgreSQL().Parse(realDumpFunction)
	require.NoError(t, err)

	out, err := NewPostgreSQL().Generate(schema)
	require.NoError(t, err)

	assert.Contains(t, out, "CREATE FUNCTION touch() RETURNS trigger AS $$")
	assert.Contains(t, out, "LANGUAGE plpgsql")
	assert.Contains(t, out, "EXECUTE FUNCTION touch()")
	assert.NotContains(t, out, "EXECUTE FUNCTION public.touch()")
}

// A tagged body is what pg_dump reaches for when the body itself contains $$.
// Go's regexp engine has no backreferences, so the tag is matched by hand.
func TestPostgreSQL_TaggedFunctionBody(t *testing.T) {
	const ddl = `CREATE FUNCTION public.quoter() RETURNS text
    LANGUAGE sql
    AS $_$ SELECT 'a $$ b'::text $_$;`

	schema, err := NewPostgreSQL().Parse(ddl)
	require.NoError(t, err)

	require.Len(t, schema.Functions, 1)
	assert.Equal(t, "sql", schema.Functions[0].Language)
	assert.Contains(t, schema.Functions[0].Body, "a $$ b")
}
