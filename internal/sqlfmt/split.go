package sqlfmt

// SplitTopLevelCommas splits a parenthesised body on the commas that separate
// its parts, ignoring the ones nested inside parentheses or string literals.
//
// Splitting on every comma cuts a definition in half at the comma inside
// DECIMAL(10,2), inside CHECK (status IN ('a','b')), or inside a comment:
// mysqldump writes
//
//	`email` varchar(255) NOT NULL COMMENT 'login address, unique'
//
// and a naive split turned the tail of that comment into a definition of its
// own, which then read as a UNIQUE constraint the table never had.
func SplitTopLevelCommas(body string) []string {
	var parts []string
	depth := 0
	inString := false
	start := 0

	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\'':
			// A doubled quote is an escaped one and stays inside the string.
			if inString && i+1 < len(body) && body[i+1] == '\'' {
				i++
				continue
			}
			inString = !inString
		case '(':
			if !inString {
				depth++
			}
		case ')':
			if !inString {
				depth--
			}
		case ',':
			if !inString && depth == 0 {
				parts = append(parts, body[start:i])
				start = i + 1
			}
		}
	}

	if start < len(body) {
		parts = append(parts, body[start:])
	}
	return parts
}

// SplitTopLevelCommasBytes is SplitTopLevelCommas for a byte slice. The parts
// share the caller's backing array.
func SplitTopLevelCommasBytes(body []byte) [][]byte {
	var parts [][]byte
	for _, p := range SplitTopLevelCommas(string(body)) {
		parts = append(parts, []byte(p))
	}
	return parts
}
