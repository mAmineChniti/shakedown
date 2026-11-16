package checks

import "strings"

// SplitStatements splits a migration file into individual SQL statements.
// It understands single/double quotes, dollar-quoted bodies, line comments,
// and block comments. It does not parse SQL — it only finds statement
// boundaries, which is sufficient for the pattern checks in this package.
func SplitStatements(sql string) []string {
	var out []string
	var cur strings.Builder

	i := 0
	n := len(sql)
	for i < n {
		c := sql[i]

		// line comment
		if c == '-' && i+1 < n && sql[i+1] == '-' {
			for i < n && sql[i] != '\n' {
				cur.WriteByte(sql[i])
				i++
			}
			continue
		}
		// block comment
		if c == '/' && i+1 < n && sql[i+1] == '*' {
			cur.WriteString("/*")
			i += 2
			for i+1 < n && !(sql[i] == '*' && sql[i+1] == '/') {
				cur.WriteByte(sql[i])
				i++
			}
			if i+1 < n {
				cur.WriteString("*/")
				i += 2
			}
			continue
		}
		// single-quoted string
		if c == '\'' {
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(sql[i])
				if sql[i] == '\'' {
					if i+1 < n && sql[i+1] == '\'' { // escaped ''
						cur.WriteByte(sql[i+1])
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			continue
		}
		// double-quoted identifier
		if c == '"' {
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(sql[i])
				if sql[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}
		// backtick-quoted identifier (MySQL)
		if c == '`' {
			cur.WriteByte(c)
			i++
			for i < n {
				cur.WriteByte(sql[i])
				if sql[i] == '`' {
					i++
					break
				}
				i++
			}
			continue
		}
		// dollar-quoted string/body: $tag$...$tag$
		if c == '$' {
			if end := dollarTagEnd(sql, i); end > i {
				tag := sql[i:end]
				cur.WriteString(tag)
				i = end
				close := strings.Index(sql[i:], tag)
				if close < 0 {
					cur.WriteString(sql[i:])
					i = n
				} else {
					cur.WriteString(sql[i : i+close+len(tag)])
					i += close + len(tag)
				}
				continue
			}
		}
		if c == ';' {
			stmt := strings.TrimSpace(cur.String())
			if stmt != "" {
				out = append(out, stmt)
			}
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(c)
		i++
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}

// dollarTagEnd returns the index just past a valid $tag$ opener at i, or
// i if sql[i] does not start one.
func dollarTagEnd(sql string, i int) int {
	if sql[i] != '$' {
		return i
	}
	j := i + 1
	for j < len(sql) {
		cj := sql[j]
		if cj == '$' {
			return j + 1
		}
		if !(cj == '_' || cj >= 'a' && cj <= 'z' || cj >= 'A' && cj <= 'Z' || cj >= '0' && cj <= '9') {
			return i
		}
		j++
	}
	return i
}

// Normalize flattens whitespace for regex matching.
func Normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
