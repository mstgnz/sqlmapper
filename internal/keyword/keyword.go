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
