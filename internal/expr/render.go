package expr

import "strings"

// Options steers how an expression is rendered.
type Options struct {
	// Quote writes identifiers in the target's quoting characters. Dumps this
	// package produces are read by a human as often as by a server, so the
	// default is to leave a plain name plain.
	Quote bool

	// Condition marks an expression that has to evaluate to a truth value, such
	// as the body of a CHECK or the WHERE clause of a view. In a dialect with no
	// boolean type, a bare column there is rewritten to compare against zero.
	Condition bool
}

// SQL renders an expression for the given dialect.
func SQL(e Expr, d Dialect) string {
	return Render(e, d, Options{})
}

// Render writes an expression for the given dialect with the given options.
func Render(e Expr, d Dialect, opts Options) string {
	r := &renderer{dialect: d, opts: opts}
	var b strings.Builder
	r.write(&b, e, precLowest, opts.Condition)
	return b.String()
}

// Translate parses an expression written in one dialect and renders it for
// another. It is the form the generators use.
func Translate(sql string, from, to Dialect, opts Options) (string, error) {
	e, err := Parse(sql)
	if err != nil {
		return "", err
	}
	_ = from // reserved: no rule yet needs to know the source dialect
	return Render(e, to, opts), nil
}

type renderer struct {
	dialect Dialect
	opts    Options
}

// write emits e, wrapping it in parentheses when its own precedence is lower
// than the context requires. cond reports whether the value is being used where
// a truth value is expected.
func (r *renderer) write(b *strings.Builder, e Expr, floor int, cond bool) {
	switch n := e.(type) {

	case *Literal:
		b.WriteString(r.literal(n))

	case *Ident:
		// Half the dialects write the current timestamp as a bare word rather
		// than a call: CURRENT_TIMESTAMP, SYSTIMESTAMP, SYSDATE. A quoted name
		// was chosen deliberately and is never one of them.
		if !n.Quoted && len(n.Qualifier) == 0 && nowAliases[strings.ToLower(n.Name)] {
			b.WriteString(r.dialect.now())
			return
		}

		name := r.ident(n)
		// A bare column standing where a condition belongs is a boolean use.
		// Oracle and SQL Server have no boolean, so the comparison has to be
		// written out or the statement will not parse there.
		if cond && !r.dialect.hasBoolean() {
			b.WriteString(name)
			b.WriteString(" <> 0")
			return
		}
		b.WriteString(name)

	case *Call:
		r.writeCall(b, n)

	case *Cast:
		// A cast is PostgreSQL's own notation and carries no meaning elsewhere:
		// the column already has the type the target declared for it.
		if r.dialect != PostgreSQL {
			r.write(b, n.X, floor, cond)
			return
		}
		r.wrap(b, precCast, floor, func() {
			r.write(b, n.X, precCast, false)
			b.WriteString("::")
			b.WriteString(n.Type)
		})

	case *Unary:
		prec := precUnary
		if n.Op == "NOT" {
			prec = precNot
		}
		r.wrap(b, prec, floor, func() {
			if n.Op == "NOT" {
				b.WriteString("NOT ")
				r.write(b, n.X, prec, true)
				return
			}
			b.WriteString(n.Op)
			r.write(b, n.X, prec, false)
		})

	case *Binary:
		r.writeBinary(b, n, floor)

	case *In:
		r.wrap(b, precCompare, floor, func() {
			r.write(b, n.X, precCompare, false)
			if n.Not {
				b.WriteString(" NOT")
			}
			b.WriteString(" IN (")
			for i, item := range n.List {
				if i > 0 {
					b.WriteString(", ")
				}
				r.write(b, item, precLowest, false)
			}
			b.WriteString(")")
		})

	case *Between:
		r.wrap(b, precCompare, floor, func() {
			r.write(b, n.X, precCompare, false)
			if n.Not {
				b.WriteString(" NOT")
			}
			b.WriteString(" BETWEEN ")
			r.write(b, n.Low, precCompare, false)
			b.WriteString(" AND ")
			r.write(b, n.Hi, precCompare, false)
		})

	case *IsNull:
		r.wrap(b, precCompare, floor, func() {
			r.write(b, n.X, precCompare, false)
			if n.Not {
				b.WriteString(" IS NOT NULL")
				return
			}
			b.WriteString(" IS NULL")
		})
	}
}

func (r *renderer) writeBinary(b *strings.Builder, n *Binary, floor int) {
	prec, ok := binaryPrec[n.Op]
	if !ok {
		switch n.Op {
		case "AND":
			prec = precAnd
		case "OR":
			prec = precOr
		default:
			prec = precCompare
		}
	}

	// Both sides of a logical operator are themselves conditions.
	logical := n.Op == "AND" || n.Op == "OR"

	r.wrap(b, prec, floor, func() {
		r.write(b, n.L, prec, logical)
		b.WriteString(" ")
		b.WriteString(n.Op)
		b.WriteString(" ")
		// The parser is left associative, so a same-precedence operator on the
		// right was parenthesised in the source and has to stay that way:
		// a - (b - c) is not a - b - c.
		r.write(b, n.R, prec+1, logical)
	})
}

func (r *renderer) writeCall(b *strings.Builder, n *Call) {
	// Every dialect spells "the current timestamp" differently, and it is the
	// one function that turns up in nearly every schema.
	if len(n.Args) == 0 && nowAliases[strings.ToLower(n.Name)] {
		b.WriteString(r.dialect.now())
		return
	}

	b.WriteString(n.Name)
	b.WriteString("(")
	if n.Distinct {
		b.WriteString("DISTINCT ")
	}
	for i, arg := range n.Args {
		if i > 0 {
			b.WriteString(", ")
		}
		r.write(b, arg, precLowest, false)
	}
	b.WriteString(")")
}

// wrap emits inner, in parentheses when its precedence is looser than the
// surrounding context demands.
func (r *renderer) wrap(b *strings.Builder, prec, floor int, inner func()) {
	if prec < floor {
		b.WriteString("(")
		inner()
		b.WriteString(")")
		return
	}
	inner()
}

func (r *renderer) literal(n *Literal) string {
	switch n.Kind {
	case NullLit:
		return "NULL"
	case NumberLit:
		return n.Value
	case StringLit:
		return "'" + strings.ReplaceAll(n.Value, "'", "''") + "'"
	case BoolLit:
		// Where there is no boolean type the keyword is not a value either, so
		// it becomes the number the column is actually declared as.
		if r.dialect.hasBoolean() {
			return n.Value
		}
		if strings.EqualFold(n.Value, "TRUE") {
			return "1"
		}
		return "0"
	}
	return n.Value
}

func (r *renderer) ident(n *Ident) string {
	parts := append([]string{}, n.Qualifier...)

	// A qualifier naming another dialect's default schema does not resolve
	// here, and one naming this dialect's own is noise.
	if len(parts) > 0 && defaultSchemas[strings.ToLower(parts[0])] {
		parts = parts[1:]
	}
	parts = append(parts, n.Name)

	if !r.opts.Quote || n.Name == "*" {
		return strings.Join(parts, ".")
	}

	open, close := r.dialect.quotes()
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = open + strings.ReplaceAll(p, close, close+close) + close
	}
	return strings.Join(quoted, ".")
}

// IsJSONGuard reports whether the expression is only there to emulate a JSON
// column type, as MariaDB's CHECK (json_valid(col)) is. A dialect with a real
// JSON type validates the value itself, and none of them has the function.
func IsJSONGuard(e Expr) bool {
	call, ok := e.(*Call)
	return ok && strings.EqualFold(call.Name, "json_valid")
}

// Condition translates an expression that has to evaluate to a truth value,
// such as a CHECK body or the WHERE clause of a view.
//
// Text this package cannot parse is returned unchanged. A conversion that
// worked before this layer existed keeps working: the worst case is the old
// behaviour of copying the expression across verbatim.
func Condition(sql string, to Dialect) string {
	e, err := Parse(sql)
	if err != nil {
		return strings.TrimSpace(sql)
	}
	return Render(e, to, Options{Condition: true})
}

// Value translates an expression used where a value is expected, such as a
// column default. Unparseable text is returned unchanged, as in Condition.
func Value(sql string, to Dialect) string {
	e, err := Parse(sql)
	if err != nil {
		return strings.TrimSpace(sql)
	}
	return Render(e, to, Options{})
}

// IsJSONGuardSQL reports whether the text is only there to emulate a JSON column
// type, as MariaDB's CHECK (json_valid(col)) is.
func IsJSONGuardSQL(sql string) bool {
	e, err := Parse(sql)
	return err == nil && IsJSONGuard(e)
}

// Normalize rewrites an expression into a dialect-neutral form, which is what
// the schema model should hold: SQL Server's ([amount]>=(0)) and PostgreSQL's
// ((amount >= (0)::numeric)) both become amount >= 0, so a consumer reading the
// model is not looking at whichever dialect happened to produce the dump.
//
// Text this package cannot parse is returned unchanged.
func Normalize(sql string) string {
	e, err := Parse(sql)
	if err != nil {
		return strings.TrimSpace(sql)
	}
	return SQL(e, Generic)
}
