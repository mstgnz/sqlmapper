package expr

import "strings"

// Expr is a node in a parsed SQL expression.
//
// There is no node for parentheses. They carry no meaning once the tree records
// the nesting, and dropping them is what turns SQL Server's (([amount]>=(0)))
// into amount >= 0. The renderer puts back exactly the parentheses that
// operator precedence requires, and no others.
type Expr interface {
	isExpr()
}

// LiteralKind distinguishes the kinds of constant an expression can hold.
type LiteralKind int

const (
	// NumberLit is a numeric constant.
	NumberLit LiteralKind = iota
	// StringLit is a text constant.
	StringLit
	// NullLit is NULL.
	NullLit
	// BoolLit is TRUE or FALSE.
	BoolLit
)

// Literal is a constant value.
type Literal struct {
	Kind LiteralKind

	// Value holds the number as written, the unquoted text of a string, or
	// "TRUE" / "FALSE" for a boolean. It is empty for NULL.
	Value string
}

// Ident is a column or table name, optionally qualified and optionally written
// in quotes at the source.
type Ident struct {
	// Qualifier holds the parts before the final name, so public.customers.id
	// arrives as {"public", "customers"} and "id".
	Qualifier []string
	Name      string

	// Quoted reports whether the source wrote the name in quotes. An unquoted
	// name was folded to the server's own case and may be folded back; a quoted
	// one was chosen deliberately and is left alone.
	Quoted bool
}

// Call is a function call. The name is kept as written; the renderer decides
// whether the target dialect spells it differently.
type Call struct {
	Name     string
	Args     []Expr
	Distinct bool
}

// Unary is a prefix operator: NOT or a sign.
type Unary struct {
	Op string
	X  Expr
}

// Binary is an infix operator. The set is deliberately open: anything the
// scanner produced as an operator can appear here, so an expression using
// something this package has no opinion about still survives a round trip.
type Binary struct {
	Op   string
	L, R Expr
}

// Cast is PostgreSQL's value::type. A CAST(x AS t) call parses to the same node,
// so the renderer can write whichever form the target dialect prefers.
type Cast struct {
	X    Expr
	Type string
}

// In is a membership test against a literal list.
type In struct {
	X    Expr
	List []Expr
	Not  bool
}

// Between is a range test. It has its own node because its AND binds tighter
// than a logical AND and cannot be represented as nested Binary nodes.
type Between struct {
	X       Expr
	Low, Hi Expr
	Not     bool
}

// IsNull is x IS NULL, or x IS NOT NULL when Not is set.
type IsNull struct {
	X   Expr
	Not bool
}

func (*Literal) isExpr() {}
func (*Ident) isExpr()   {}
func (*Call) isExpr()    {}
func (*Unary) isExpr()   {}
func (*Binary) isExpr()  {}
func (*Cast) isExpr()    {}
func (*In) isExpr()      {}
func (*Between) isExpr() {}
func (*IsNull) isExpr()  {}

// FullName returns the identifier with its qualifier, as written.
func (i *Ident) FullName() string {
	if len(i.Qualifier) == 0 {
		return i.Name
	}
	return strings.Join(append(append([]string{}, i.Qualifier...), i.Name), ".")
}

// Walk calls fn for every node in the tree, parents before children. Returning
// false from fn stops the descent into that node's children.
func Walk(e Expr, fn func(Expr) bool) {
	if e == nil || !fn(e) {
		return
	}
	switch n := e.(type) {
	case *Unary:
		Walk(n.X, fn)
	case *Binary:
		Walk(n.L, fn)
		Walk(n.R, fn)
	case *Cast:
		Walk(n.X, fn)
	case *Call:
		for _, a := range n.Args {
			Walk(a, fn)
		}
	case *In:
		Walk(n.X, fn)
		for _, a := range n.List {
			Walk(a, fn)
		}
	case *Between:
		Walk(n.X, fn)
		Walk(n.Low, fn)
		Walk(n.Hi, fn)
	case *IsNull:
		Walk(n.X, fn)
	}
}
