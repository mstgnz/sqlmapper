package expr

import (
	"fmt"
	"strings"
)

// precedence levels, lowest binding first. A binary operator is consumed while
// its level is above the caller's floor, which is what makes a AND b OR c parse
// as (a AND b) OR c.
const (
	precLowest  = 0
	precOr      = 1
	precAnd     = 2
	precNot     = 3
	precCompare = 4 // = <> < > <= >= IS LIKE IN BETWEEN
	precAdd     = 5 // + - ||
	precMul     = 6 // * / %
	precUnary   = 7 // unary - +
	precCast    = 8 // ::
)

// binaryPrec gives the binding level of each infix operator.
var binaryPrec = map[string]int{
	"=": precCompare, "<>": precCompare, "!=": precCompare, "<=>": precCompare,
	"<": precCompare, ">": precCompare, "<=": precCompare, ">=": precCompare,
	"+": precAdd, "-": precAdd, "||": precAdd,
	"*": precMul, "/": precMul, "%": precMul,
}

type parser struct {
	toks []Token
	pos  int
}

// Parse reads a single SQL expression. Surrounding parentheses, of which dump
// tools emit one or two more than the expression needs, are folded away by the
// tree rather than preserved.
func Parse(sql string) (Expr, error) {
	toks, err := Tokens(sql)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}

	e, err := p.parseExpr(precLowest)
	if err != nil {
		return nil, err
	}
	if tok := p.peek(); tok.Kind != TokEOF {
		return nil, fmt.Errorf("expr: unexpected %s %q at offset %d", tok.Kind, tok.Raw, tok.Pos)
	}
	return e, nil
}

func (p *parser) peek() Token { return p.toks[p.pos] }

func (p *parser) next() Token {
	tok := p.toks[p.pos]
	if tok.Kind != TokEOF {
		p.pos++
	}
	return tok
}

// at reports whether the current token is the given kind with the given text.
func (p *parser) at(kind TokenKind, text string) bool {
	tok := p.peek()
	return tok.Kind == kind && tok.Text == text
}

func (p *parser) accept(kind TokenKind, text string) bool {
	if p.at(kind, text) {
		p.next()
		return true
	}
	return false
}

func (p *parser) expect(kind TokenKind, text string) error {
	if p.accept(kind, text) {
		return nil
	}
	tok := p.peek()
	return fmt.Errorf("expr: expected %q but found %s %q at offset %d", text, tok.Kind, tok.Raw, tok.Pos)
}

// parseExpr reads an expression, consuming infix operators while they bind more
// tightly than floor.
func (p *parser) parseExpr(floor int) (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}

	for {
		tok := p.peek()

		switch {
		case tok.Kind == TokKeyword && tok.Text == "OR" && precOr > floor:
			p.next()
			right, err := p.parseExpr(precOr)
			if err != nil {
				return nil, err
			}
			left = &Binary{Op: "OR", L: left, R: right}

		case tok.Kind == TokKeyword && tok.Text == "AND" && precAnd > floor:
			p.next()
			right, err := p.parseExpr(precAnd)
			if err != nil {
				return nil, err
			}
			left = &Binary{Op: "AND", L: left, R: right}

		case tok.Kind == TokKeyword && tok.Text == "IS" && precCompare > floor:
			p.next()
			not := p.accept(TokKeyword, "NOT")
			if err := p.expect(TokKeyword, "NULL"); err != nil {
				return nil, err
			}
			left = &IsNull{X: left, Not: not}

		case tok.Kind == TokKeyword && (tok.Text == "IN" || tok.Text == "LIKE" || tok.Text == "BETWEEN") && precCompare > floor:
			var err error
			if left, err = p.parseKeywordInfix(left, false); err != nil {
				return nil, err
			}

		case tok.Kind == TokKeyword && tok.Text == "NOT" && precCompare > floor && p.notFollowedByPredicate():
			p.next()
			var err error
			if left, err = p.parseKeywordInfix(left, true); err != nil {
				return nil, err
			}

		case tok.Kind == TokOperator && tok.Text == "::" && precCast > floor:
			p.next()
			typeName, err := p.parseTypeName()
			if err != nil {
				return nil, err
			}
			left = &Cast{X: left, Type: typeName}

		case tok.Kind == TokOperator:
			prec, ok := binaryPrec[tok.Text]
			if !ok || prec <= floor {
				return left, nil
			}
			p.next()
			right, err := p.parseExpr(prec)
			if err != nil {
				return nil, err
			}
			left = &Binary{Op: tok.Text, L: left, R: right}

		default:
			return left, nil
		}
	}
}

// notFollowedByPredicate reports whether a NOT in infix position introduces one
// of the predicates that can be negated there, as in "x NOT IN (...)". A NOT
// anywhere else is the prefix operator and belongs to parseUnary.
func (p *parser) notFollowedByPredicate() bool {
	if p.pos+1 >= len(p.toks) {
		return false
	}
	next := p.toks[p.pos+1]
	return next.Kind == TokKeyword && (next.Text == "IN" || next.Text == "LIKE" || next.Text == "BETWEEN")
}

// parseKeywordInfix reads IN, LIKE or BETWEEN, with the NOT already consumed.
func (p *parser) parseKeywordInfix(left Expr, not bool) (Expr, error) {
	tok := p.next()

	switch tok.Text {
	case "IN":
		if err := p.expect(TokPunct, "("); err != nil {
			return nil, err
		}
		var list []Expr
		for {
			item, err := p.parseExpr(precLowest)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
			if !p.accept(TokPunct, ",") {
				break
			}
		}
		if err := p.expect(TokPunct, ")"); err != nil {
			return nil, err
		}
		return &In{X: left, List: list, Not: not}, nil

	case "LIKE":
		right, err := p.parseExpr(precCompare)
		if err != nil {
			return nil, err
		}
		op := "LIKE"
		if not {
			op = "NOT LIKE"
		}
		return &Binary{Op: op, L: left, R: right}, nil

	case "BETWEEN":
		// The AND here binds tighter than a logical AND, so the bounds are read
		// at comparison level rather than through parseExpr's AND branch.
		low, err := p.parseExpr(precCompare)
		if err != nil {
			return nil, err
		}
		if err := p.expect(TokKeyword, "AND"); err != nil {
			return nil, err
		}
		hi, err := p.parseExpr(precCompare)
		if err != nil {
			return nil, err
		}
		return &Between{X: left, Low: low, Hi: hi, Not: not}, nil
	}

	return nil, fmt.Errorf("expr: unsupported operator %q at offset %d", tok.Raw, tok.Pos)
}

func (p *parser) parseUnary() (Expr, error) {
	tok := p.peek()

	switch {
	case tok.Kind == TokKeyword && tok.Text == "NOT":
		p.next()
		x, err := p.parseExpr(precNot)
		if err != nil {
			return nil, err
		}
		return &Unary{Op: "NOT", X: x}, nil

	case tok.Kind == TokOperator && (tok.Text == "-" || tok.Text == "+"):
		p.next()
		x, err := p.parseExpr(precUnary)
		if err != nil {
			return nil, err
		}
		return &Unary{Op: tok.Text, X: x}, nil
	}

	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expr, error) {
	tok := p.next()

	switch {
	case tok.Kind == TokPunct && tok.Text == "(":
		// The parentheses themselves are not recorded. Precedence puts back the
		// ones the expression needs when it is rendered.
		e, err := p.parseExpr(precLowest)
		if err != nil {
			return nil, err
		}
		if err := p.expect(TokPunct, ")"); err != nil {
			return nil, err
		}
		return e, nil

	case tok.Kind == TokNumber:
		return &Literal{Kind: NumberLit, Value: tok.Raw}, nil

	case tok.Kind == TokString:
		return &Literal{Kind: StringLit, Value: tok.Text}, nil

	case tok.Kind == TokKeyword && tok.Text == "NULL":
		return &Literal{Kind: NullLit}, nil

	case tok.Kind == TokKeyword && (tok.Text == "TRUE" || tok.Text == "FALSE"):
		return &Literal{Kind: BoolLit, Value: tok.Text}, nil

	case tok.Kind == TokKeyword && tok.Text == "CAST":
		return p.parseCastCall()

	case tok.Kind == TokIdent:
		return p.parseIdentOrCall(tok)
	}

	return nil, fmt.Errorf("expr: unexpected %s %q at offset %d", tok.Kind, tok.Raw, tok.Pos)
}

// parseCastCall reads CAST(x AS type), which becomes the same node as x::type.
func (p *parser) parseCastCall() (Expr, error) {
	if err := p.expect(TokPunct, "("); err != nil {
		return nil, err
	}
	x, err := p.parseExpr(precLowest)
	if err != nil {
		return nil, err
	}
	if err := p.expect(TokKeyword, "AS"); err != nil {
		return nil, err
	}
	typeName, err := p.parseTypeName()
	if err != nil {
		return nil, err
	}
	if err := p.expect(TokPunct, ")"); err != nil {
		return nil, err
	}
	return &Cast{X: x, Type: typeName}, nil
}

// parseTypeName reads a type, which may be several words and may carry a
// precision, as in "character varying(255)" or "numeric(10,2)".
func (p *parser) parseTypeName() (string, error) {
	tok := p.peek()
	if tok.Kind != TokIdent && tok.Kind != TokKeyword {
		return "", fmt.Errorf("expr: expected a type name but found %s %q at offset %d", tok.Kind, tok.Raw, tok.Pos)
	}

	var words []string
	for p.peek().Kind == TokIdent {
		var word strings.Builder
		word.WriteString(p.next().Text)
		// A type may be schema-qualified, "public.order_status", which
		// pg_dump writes for a user-defined type. Stopping at the dot left the
		// cast unparsed, so the whole expression came back untouched and
		// PostgreSQL's :: reached targets that have no such syntax.
		for p.at(TokPunct, ".") {
			p.next()
			if p.peek().Kind != TokIdent {
				break
			}
			word.WriteString(".")
			word.WriteString(p.next().Text)
		}
		words = append(words, word.String())
	}
	var name strings.Builder
	name.WriteString(strings.Join(words, " "))

	// An optional precision, which is part of the type rather than a call.
	if p.at(TokPunct, "(") {
		start := p.pos
		p.next()
		var nums []string
		for p.peek().Kind == TokNumber {
			nums = append(nums, p.next().Raw)
			if !p.accept(TokPunct, ",") {
				break
			}
		}
		if len(nums) > 0 && p.accept(TokPunct, ")") {
			name.WriteString("(")
			name.WriteString(strings.Join(nums, ","))
			name.WriteString(")")
		} else {
			p.pos = start // not a precision after all
		}
	}

	// An array marker, as in text[].
	for p.at(TokPunct, "[") {
		p.next()
		if !p.accept(TokPunct, "]") {
			break
		}
		name.WriteString("[]")
	}

	return name.String(), nil
}

// parseIdentOrCall reads a name, which becomes a qualified name when dots
// follow it and a call when an argument list does.
func (p *parser) parseIdentOrCall(first Token) (Expr, error) {
	parts := []Token{first}
	for p.at(TokPunct, ".") {
		p.next()
		tok := p.peek()
		if tok.Kind != TokIdent {
			return nil, fmt.Errorf("expr: expected a name after '.' but found %s %q at offset %d", tok.Kind, tok.Raw, tok.Pos)
		}
		parts = append(parts, p.next())
	}

	if p.at(TokPunct, "(") {
		return p.parseCall(parts)
	}

	id := &Ident{Name: parts[len(parts)-1].Text, Quoted: parts[len(parts)-1].Quoted}
	for _, part := range parts[:len(parts)-1] {
		id.Qualifier = append(id.Qualifier, part.Text)
	}
	return id, nil
}

func (p *parser) parseCall(nameParts []Token) (Expr, error) {
	names := make([]string, len(nameParts))
	for i, part := range nameParts {
		names[i] = part.Text
	}

	if err := p.expect(TokPunct, "("); err != nil {
		return nil, err
	}

	call := &Call{Name: strings.Join(names, ".")}
	call.Distinct = p.accept(TokKeyword, "DISTINCT")

	if p.accept(TokPunct, ")") {
		return call, nil
	}
	for {
		// COUNT(*) is the one place a bare star is an argument.
		if p.at(TokOperator, "*") {
			p.next()
			call.Args = append(call.Args, &Ident{Name: "*"})
		} else {
			arg, err := p.parseExpr(precLowest)
			if err != nil {
				return nil, err
			}
			call.Args = append(call.Args, arg)
		}
		if !p.accept(TokPunct, ",") {
			break
		}
	}
	if err := p.expect(TokPunct, ")"); err != nil {
		return nil, err
	}
	return call, nil
}
