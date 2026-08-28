// Package keyword matches SQL keywords at a word boundary.
//
// Testing a statement with strings.HasPrefix is what every dialect's dispatcher
// used to do, and it classifies CREATE TABLESPACE as a CREATE TABLE: the
// keyword is a prefix of the other. The statement then reaches a parser that
// expects a column list and does not find one.
package keyword

import (
	"bytes"
	"strings"
)

// HasPrefix reports whether stmt begins with the given keyword phrase, followed
// by something that is not part of a longer word. The comparison is
// case-insensitive; the keyword is expected in upper case.
func HasPrefix(stmt, kw string) bool {
	if len(stmt) < len(kw) {
		return false
	}
	if !strings.EqualFold(stmt[:len(kw)], kw) {
		return false
	}
	if len(stmt) == len(kw) {
		return true
	}
	return !isWordByte(stmt[len(kw)])
}

// HasPrefixBytes is HasPrefix for a statement held as bytes, which is how the
// file parsers carry one.
func HasPrefixBytes(stmt []byte, kw string) bool {
	if len(stmt) < len(kw) {
		return false
	}
	if !bytes.EqualFold(stmt[:len(kw)], []byte(kw)) {
		return false
	}
	if len(stmt) == len(kw) {
		return true
	}
	return !isWordByte(stmt[len(kw)])
}

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// UpperASCII folds a to z into A to Z and leaves every other byte alone.
//
// It exists because strings.ToUpper can change a string's length: some
// characters have an upper-case form of a different size in UTF-8. A parser
// that finds a keyword in the folded copy and then slices the original at that
// offset is reading two different strings, and on a dump carrying any such
// character the offset lands past the end. SQL keywords are ASCII, so folding
// only ASCII is both correct here and length-preserving.
func UpperASCII(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 'a' || c > 'z' {
			continue
		}
		if b == nil {
			b = []byte(s)
		}
		b[i] = c - ('a' - 'A')
	}
	if b == nil {
		return s
	}
	return string(b)
}

// UpperASCIIBytes is UpperASCII for a byte slice. The result is a copy.
func UpperASCIIBytes(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return out
}
