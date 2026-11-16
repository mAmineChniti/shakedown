package checks

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/mAmineChniti/shakedown/internal/engine"
)

// DataSafety is the pre-flight check: it scans migration statements for
// operations that can lose data or fail on existing rows, and counts the
// actual affected rows against the scratch database.
type DataSafety struct {
	Dialect engine.Dialect
}

func (d DataSafety) Name() string { return "data-safety" }

var (
	reAlterTable = regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(.+?)\s+`)
	reCreateIdx  = regexp.MustCompile(`(?i)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(\S+)\s+ON\s+(.+?)\s*(?:USING\s+\S+\s*)?\(`)
	reDropTable  = regexp.MustCompile(`(?i)^\s*DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?(.+?)(?:\s+CASCADE|\s+RESTRICT)?\s*$`)
	reTruncate   = regexp.MustCompile(`(?i)^\s*TRUNCATE\s+(?:TABLE\s+)?(.+?)(?:\s+RESTART\s+IDENTITY|\s+C?CASCADE|\s+RESTRICT)*\s*$`)
)

// Analyze implements PreCheck.
func (d DataSafety) Analyze(ctx context.Context, sess Session, statements []string) []Finding {
	var out []Finding
	for _, stmt := range statements {
		out = append(out, d.analyzeStatement(ctx, sess, Normalize(stmt))...)
	}
	return out
}

func (d DataSafety) analyzeStatement(ctx context.Context, sess Session, stmt string) []Finding {
	var out []Finding

	if m := reDropTable.FindStringSubmatch(stmt); m != nil {
		for _, t := range splitNameList(m[1]) {
			out = append(out, d.dropProbe(ctx, sess, t)...)
		}
		return out
	}
	if m := reTruncate.FindStringSubmatch(stmt); m != nil {
		for _, t := range splitNameList(m[1]) {
			out = append(out, d.dropProbe(ctx, sess, t)...)
		}
		return out
	}
	if m := reCreateIdx.FindStringSubmatch(stmt); m != nil {
		return d.analyzeCreateIndex(ctx, sess, stmt, m[1], m[2])
	}
	if m := reAlterTable.FindStringSubmatch(stmt); m != nil {
		table := strings.TrimSpace(m[1])
		actions := stmt[len(m[0]):]
		for _, action := range splitActions(actions) {
			out = append(out, d.analyzeAlterAction(ctx, sess, table, action)...)
		}
		return out
	}
	return out
}

// ---- shared helpers ----

func splitActions(s string) []string {
	var out []string
	depth := 0
	start := 0
	inS, inD, inB := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				inS = false
			}
		case inD:
			if c == '"' {
				inD = false
			}
		case inB:
			if c == '`' {
				inB = false
			}
		case c == '\'':
			inS = true
		case c == '"':
			inD = true
		case c == '`':
			inB = true
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, strings.TrimSpace(s[start:i]))
			start = i + 1
		}
	}
	if t := strings.TrimSpace(s[start:]); t != "" {
		out = append(out, t)
	}
	return out
}

func splitNameList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		for _, kw := range []string{" CASCADE", " RESTRICT", " IF EXISTS"} {
			if strings.HasSuffix(strings.ToUpper(p), kw) {
				p = strings.TrimSpace(p[:len(p)-len(kw)])
			}
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func probe(ctx context.Context, sess Session, sql string) (int64, error) {
	var n int64
	err := sess.DB.QueryRowContext(ctx, sql).Scan(&n)
	return n, err
}

func finding(sev Severity, msg, evidence, suggestion string) Finding {
	return Finding{Check: "data-safety", Severity: sev, Message: msg, Evidence: evidence, Suggestion: suggestion}
}

// q quotes a possibly-qualified name: schema.table stays verbatim; bare
// identifiers get dialect quoting.
func q(d engine.Dialect, name string) string {
	if strings.ContainsAny(name, ".\"`") {
		return name
	}
	return engine.QuoteIdent(d, name)
}

// ---- DROP / TRUNCATE ----

func (d DataSafety) dropProbe(ctx context.Context, sess Session, table string) []Finding {
	n, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s", q(d.Dialect, table)))
	if err != nil {
		return []Finding{finding(Warning, "could not count rows in "+table,
			"probe error: "+err.Error(), "")}
	}
	sev := Info
	if n > 0 {
		sev = Critical
	}
	return []Finding{finding(sev,
		fmt.Sprintf("%s contains %d rows that will be permanently lost", table, n),
		fmt.Sprintf("count(*) = %d", n),
		"take a backup or verify the data is reproducible before shipping")}
}

// ---- CREATE INDEX ----

func (d DataSafety) analyzeCreateIndex(ctx context.Context, sess Session, stmt, idxName, tableExpr string) []Finding {
	var out []Finding
	norm := strings.ToUpper(stmt)
	isUnique := strings.Contains(norm, "UNIQUE")

	if isUnique {
		if cols := indexColumns(stmt); cols != "" {
			dupSQL := fmt.Sprintf(
				`SELECT count(*) FROM (SELECT 1 FROM %s GROUP BY %s HAVING count(*) > 1) d`,
				q(d.Dialect, tableExpr), cols)
			if n, err := probe(ctx, sess, dupSQL); err == nil && n > 0 {
				out = append(out, finding(Critical,
					fmt.Sprintf("unique index %s will fail: %d duplicate groups exist", idxName, n),
					fmt.Sprintf("duplicate groups = %d", n),
					"deduplicate before creating the unique index"))
			}
		}
	}
	if d.Dialect == engine.Postgres && !strings.Contains(norm, "CONCURRENTLY") {
		out = append(out, finding(Warning,
			fmt.Sprintf("CREATE INDEX %s takes a SHARE lock that blocks writes for the build duration", idxName),
			"no CONCURRENTLY in statement",
			"use CREATE INDEX CONCURRENTLY (outside a transaction block)"))
	} else if d.Dialect == engine.MySQL {
		out = append(out, finding(Info,
			fmt.Sprintf("CREATE INDEX %s builds under a metadata lock that blocks writes", idxName),
			"MySQL has no CREATE INDEX CONCURRENTLY; expect brief blocking",
			"on large tables prefer gh-ost/pt-osc online schema change tools"))
	}
	return out
}

// indexColumns extracts the first parenthesized group from CREATE INDEX —
// the key column list.
func indexColumns(stmt string) string {
	i := strings.Index(stmt, "(")
	if i < 0 {
		return ""
	}
	depth := 0
	for j := i; j < len(stmt); j++ {
		switch stmt[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return stmt[i+1 : j]
			}
		}
	}
	return ""
}

// ---- ALTER TABLE actions ----

func (d DataSafety) analyzeAlterAction(ctx context.Context, sess Session, table, action string) []Finding {
	a := strings.TrimSpace(action)
	up := strings.ToUpper(a)

	switch {
	case strings.HasPrefix(up, "DROP COLUMN"):
		return d.dropColumnProbe(ctx, sess, table, a)
	case strings.HasPrefix(up, "ALTER COLUMN"):
		return d.alterColumnProbe(ctx, sess, table, a)
	case strings.HasPrefix(up, "MODIFY"), strings.HasPrefix(up, "CHANGE"):
		return d.modifyColumnProbe(ctx, sess, table, a)
	case strings.HasPrefix(up, "ADD COLUMN"):
		return d.addColumnProbe(ctx, sess, table, a)
	case d.Dialect == engine.MSSQL && strings.HasPrefix(up, "ADD ") && !isConstraintAction(up):
		// T-SQL: ALTER TABLE t ADD col type ... (no COLUMN keyword)
		return d.addColumnProbe(ctx, sess, table, "ADD COLUMN "+strings.TrimPrefix(a, "ADD "))
	case d.Dialect == engine.Oracle && strings.HasPrefix(up, "ADD ("):
		// Oracle: ALTER TABLE t ADD (col type, ...)
		inner := strings.TrimPrefix(a, "ADD")
		inner = strings.TrimSpace(inner)
		inner = strings.TrimPrefix(inner, "(")
		return d.addColumnProbe(ctx, sess, table, "ADD COLUMN "+inner)
	case strings.HasPrefix(up, "ALGORITHM=") || strings.Contains(up, " ALGORITHM="):
		return d.algorithmProbe(a)
	default:
		if isConstraintAction(up) {
			return d.addConstraintProbe(ctx, sess, table, a)
		}
	}
	return nil
}

func isConstraintAction(up string) bool {
	for _, p := range []string{"ADD CONSTRAINT", "ADD PRIMARY KEY", "ADD UNIQUE", "ADD CHECK", "ADD FOREIGN KEY"} {
		if strings.HasPrefix(up, p) {
			return true
		}
	}
	return false
}

func colName(rest string) string {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return ""
	}
	if rest[0] == '"' || rest[0] == '`' {
		q := rest[0]
		end := strings.Index(rest[1:], string(q))
		if end < 0 {
			return rest
		}
		return rest[:end+2]
	}
	fields := strings.Fields(rest)
	return fields[0]
}

func (d DataSafety) dropColumnProbe(ctx context.Context, sess Session, table, action string) []Finding {
	rest := strings.TrimSpace(action[len("DROP COLUMN"):])
	rest = regexp.MustCompile(`(?i)^IF\s+EXISTS\s+`).ReplaceAllString(rest, "")
	col := colName(rest)
	if col == "" {
		return nil
	}
	n, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NOT NULL",
		q(d.Dialect, table), q(d.Dialect, col)))
	if err != nil {
		return []Finding{finding(Info, "could not probe dropped column "+col,
			"probe error: "+err.Error(), "")}
	}
	sev := Info
	if n > 0 {
		sev = Warning
	}
	return []Finding{finding(sev,
		fmt.Sprintf("dropping column %s discards %d non-null values", col, n),
		fmt.Sprintf("non-null count = %d", n),
		"archive the column data first if it may be needed")}
}

// alterColumnProbe handles Postgres "ALTER COLUMN ..." actions.
func (d DataSafety) alterColumnProbe(ctx context.Context, sess Session, table, action string) []Finding {
	fields := strings.Fields(action)
	if len(fields) < 3 || !strings.EqualFold(fields[1], "COLUMN") {
		return nil
	}
	col := fields[2]
	rest := strings.Join(fields[3:], " ")
	up := strings.ToUpper(rest)

	if strings.HasPrefix(up, "SET NOT NULL") || strings.HasPrefix(up, "SET NOTNULL") {
		n, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NULL",
			q(d.Dialect, table), q(d.Dialect, col)))
		if err != nil {
			return nil
		}
		if n > 0 {
			return []Finding{finding(Critical,
				fmt.Sprintf("SET NOT NULL on %s.%s will fail: %d NULL rows", table, col, n),
				fmt.Sprintf("null count = %d", n),
				"backfill NULLs first, or add the constraint NOT VALID")}
		}
		return nil
	}

	newType := ""
	if m := regexp.MustCompile(`(?i)\bTYPE\s+(.+?)(?:\s+USING\b|\s+COLLATE\b|$)`).FindStringSubmatch(rest); m != nil {
		newType = strings.TrimSpace(m[1])
	}
	if newType == "" {
		return nil
	}
	return d.typeChangeProbe(ctx, sess, table, col, newType)
}

// modifyColumnProbe handles MySQL "MODIFY col type ..." and
// "CHANGE old new type ..." actions.
func (d DataSafety) modifyColumnProbe(ctx context.Context, sess Session, table, action string) []Finding {
	fields := strings.Fields(action)
	up := strings.ToUpper(fields[0])
	if up == "CHANGE" {
		// CHANGE old_name new_name type ...
		if len(fields) < 4 {
			return nil
		}
	} else if len(fields) < 3 { // MODIFY col type ...
		return nil
	}
	var col, typePart string
	if up == "CHANGE" {
		col = fields[1] // probe the old name: values live under it pre-change
		typePart = strings.Join(fields[3:], " ")
	} else {
		col = fields[1]
		typePart = strings.Join(fields[2:], " ")
	}

	upType := strings.ToUpper(typePart)
	if strings.Contains(upType, "NOT NULL") {
		if !strings.Contains(upType, "DEFAULT") {
			if n, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s", q(d.Dialect, table))); err == nil && n > 0 {
				return []Finding{finding(Critical,
					fmt.Sprintf("MODIFY %s.%s NOT NULL without DEFAULT will fail on a non-empty table", table, col),
					fmt.Sprintf("existing rows = %d", n),
					"backfill NULLs first or provide a DEFAULT")}
			}
		} else {
			// MySQL does not backfill existing NULLs on MODIFY ... DEFAULT.
			if n, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NULL",
				q(d.Dialect, table), q(d.Dialect, col))); err == nil && n > 0 {
				return []Finding{finding(Warning,
					fmt.Sprintf("%d NULL rows remain in %s.%s after MODIFY — NOT NULL may fail", n, table, col),
					fmt.Sprintf("null count = %d", n),
					"backfill NULLs before tightening nullability")}
			}
		}
	}

	newType := firstTypeToken(typePart)
	if newType == "" {
		return nil
	}
	return d.typeChangeProbe(ctx, sess, table, col, newType)
}

// firstTypeToken pulls the type out of a MODIFY/CHANGE tail, e.g.
// "varchar(50) NOT NULL DEFAULT 'x'" -> "varchar(50)".
func firstTypeToken(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	t := fields[0]
	up := strings.ToUpper(t)
	if up == "DOUBLE" && len(fields) > 1 && strings.EqualFold(fields[1], "precision") {
		return "double precision"
	}
	if (up == "CHARACTER" || up == "NATIONAL") && len(fields) > 1 {
		return t + " " + fields[1]
	}
	return t
}

// algorithmProbe reports on MySQL ALGORITHM= clauses: COPY rebuilds the
// whole table under an exclusive lock; INPLACE/INSTANT are online-ish.
func (d DataSafety) algorithmProbe(action string) []Finding {
	m := regexp.MustCompile(`(?i)ALGORITHM\s*=\s*(\w+)`).FindStringSubmatch(action)
	if m == nil {
		return nil
	}
	alg := strings.ToUpper(m[1])
	switch alg {
	case "COPY":
		return []Finding{finding(Warning,
			"ALGORITHM=COPY forces a full table rebuild under an exclusive metadata lock",
			"ALGORITHM=COPY in statement",
			"prefer ALGORITHM=INPLACE or INSTANT where supported; on huge tables use gh-ost/pt-osc")}
	case "INPLACE":
		return []Finding{finding(Info,
			"ALGORITHM=INPLACE rebuilds in place; still holds metadata locks during the operation",
			"ALGORITHM=INPLACE in statement", "")}
	case "INSTANT":
		return nil
	default:
		return []Finding{finding(Info,
			fmt.Sprintf("ALGORITHM=%s: MySQL chooses; may fall back to COPY on unsupported operations", alg),
			"ALGORITHM="+alg+" in statement",
			"pin ALGORITHM=INPLACE to avoid surprise table rebuilds")}
	}
}

func (d DataSafety) addColumnProbe(ctx context.Context, sess Session, table, action string) []Finding {
	if !strings.Contains(strings.ToUpper(action), "NOT NULL") {
		return nil
	}
	if strings.Contains(strings.ToUpper(action), "DEFAULT") {
		return nil
	}
	fields := strings.Fields(action)
	if len(fields) < 3 {
		return nil
	}
	idx := 2
	if strings.EqualFold(fields[idx], "IF") {
		idx += 3
	}
	if idx >= len(fields) {
		return nil
	}
	col := fields[idx]
	if n, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s", q(d.Dialect, table))); err == nil && n > 0 {
		return []Finding{finding(Critical,
			fmt.Sprintf("ADD COLUMN %s NOT NULL without DEFAULT will fail on a non-empty table", col),
			fmt.Sprintf("existing rows = %d", n),
			"add the column nullable, backfill, then SET NOT NULL — or provide a DEFAULT")}
	}
	return nil
}

// typeChangeProbe checks whether existing values survive a type change.
func (d DataSafety) typeChangeProbe(ctx context.Context, sess Session, table, col, newType string) []Finding {
	if m := regexp.MustCompile(`(?i)^(?:var)?char(?:acter)?\s*(?:varying)?\s*\(\s*(\d+)\s*\)`).FindStringSubmatch(newType); m != nil {
		// lengthExpr renders a full-length text form per dialect:
		// bare CHAR casts truncate to char(1) on Postgres.
		lengthExpr := fmt.Sprintf("length(%s::text)", q(d.Dialect, col))
		if d.Dialect == engine.MySQL {
			lengthExpr = fmt.Sprintf("CHAR_LENGTH(CAST(%s AS CHAR))", q(d.Dialect, col))
		} else if d.Dialect == engine.SQLite {
			lengthExpr = fmt.Sprintf("length(CAST(%s AS TEXT))", q(d.Dialect, col))
		} else if d.Dialect == engine.MSSQL {
			lengthExpr = fmt.Sprintf("LEN(CAST(%s AS NVARCHAR(MAX)))", q(d.Dialect, col))
		} else if d.Dialect == engine.Oracle {
			lengthExpr = fmt.Sprintf("LENGTH(TO_CHAR(%s))", q(d.Dialect, col))
		}
		if n, err := probe(ctx, sess,
			fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NOT NULL AND %s > %s",
				q(d.Dialect, table), q(d.Dialect, col), lengthExpr, m[1])); err == nil && n > 0 {
			return []Finding{finding(Critical,
				fmt.Sprintf("type change to %s will fail or truncate: %d values exceed length %s", newType, n, m[1]),
				fmt.Sprintf("over-length count = %d", n),
				"widen the column or clean the data first")}
		}
	}

	nonNull, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IS NOT NULL",
		q(d.Dialect, table), q(d.Dialect, col)))
	if err != nil {
		return nil
	}
	castSQL := fmt.Sprintf(`SELECT count(*) FROM (SELECT %s AS v FROM %s WHERE %s IS NOT NULL) s`,
		castExpr(d.Dialect, col, newType), q(d.Dialect, table), q(d.Dialect, col))
	if _, err := probe(ctx, sess, castSQL); err != nil {
		return []Finding{finding(Critical,
			fmt.Sprintf("type change on %s.%s to %s will fail on existing data", table, col, newType),
			"cast probe error: "+err.Error(),
			"add a USING/CONVERT clause that safely converts values, or clean data first")}
	}
	if nonNull > 0 {
		return []Finding{finding(Info,
			fmt.Sprintf("type change on %s.%s to %s: all %d non-null values cast cleanly", table, col, newType, nonNull),
			fmt.Sprintf("non-null count = %d", nonNull), "")}
	}
	return nil
}

// castExpr renders a dialect-appropriate cast of a column to a type.
func castExpr(d engine.Dialect, col, typ string) string {
	c := q(d, col)
	if d == engine.MySQL {
		return fmt.Sprintf("CAST(%s AS %s)", c, mysqlCastType(typ))
	}
	if d == engine.SQLite {
		return fmt.Sprintf("CAST(%s AS %s)", c, sqliteCastType(typ))
	}
	if d == engine.MSSQL {
		return fmt.Sprintf("CAST(%s AS %s)", c, typ)
	}
	if d == engine.Oracle {
		return fmt.Sprintf("CAST(%s AS %s)", c, oracleCastType(typ))
	}
	return fmt.Sprintf("(%s)::%s", c, typ)
}

// sqliteCastType maps common type spellings to CAST-compatible forms.
func sqliteCastType(t string) string {
	u := strings.ToUpper(strings.TrimSpace(t))
	switch {
	case strings.Contains(u, "INT"):
		return "INTEGER"
	case strings.Contains(u, "REAL") || strings.Contains(u, "FLOA") || strings.Contains(u, "DOUB"):
		return "REAL"
	case strings.Contains(u, "BLOB"):
		return "BLOB"
	default:
		return "TEXT"
	}
}

// mysqlCastType maps common type spellings to CAST-compatible forms.
func mysqlCastType(t string) string {
	u := strings.ToUpper(strings.TrimSpace(t))
	switch {
	case strings.HasPrefix(u, "VARCHAR") || strings.HasPrefix(u, "CHAR"):
		return t
	case strings.HasPrefix(u, "INT"):
		return "SIGNED"
	case strings.HasPrefix(u, "BIGINT"):
		return "SIGNED"
	case strings.HasPrefix(u, "DECIMAL") || strings.HasPrefix(u, "NUMERIC"):
		return "DECIMAL(65,30)"
	case strings.HasPrefix(u, "DOUBLE") || strings.HasPrefix(u, "FLOAT"):
		return "DOUBLE"
	case strings.HasPrefix(u, "DATE"):
		return "DATE"
	case strings.HasPrefix(u, "DATETIME") || strings.HasPrefix(u, "TIMESTAMP"):
		return "DATETIME"
	case strings.HasPrefix(u, "TIME"):
		return "TIME"
	case strings.HasPrefix(u, "TEXT"):
		return "CHAR"
	default:
		return t
	}
}

// oracleCastType maps common type spellings to CAST-compatible Oracle
// forms (Oracle CAST accepts its own type names; we normalize the few
// cross-dialect spellings).
func oracleCastType(t string) string {
	u := strings.ToUpper(strings.TrimSpace(t))
	switch {
	case strings.HasPrefix(u, "VARCHAR") || strings.HasPrefix(u, "NVARCHAR") || strings.HasPrefix(u, "CHAR"):
		if strings.HasPrefix(u, "VARCHAR") {
			return "VARCHAR2" + strings.TrimPrefix(t, t[:len("varchar")])
		}
		return t
	case strings.HasPrefix(u, "INT"):
		return "NUMBER"
	case strings.HasPrefix(u, "BIGINT"):
		return "NUMBER"
	case strings.HasPrefix(u, "DECIMAL") || strings.HasPrefix(u, "NUMERIC"):
		return t
	case strings.HasPrefix(u, "DOUBLE") || strings.HasPrefix(u, "FLOAT"):
		return "BINARY_DOUBLE"
	case strings.HasPrefix(u, "DATE"):
		return "DATE"
	case strings.HasPrefix(u, "TIME"):
		return "TIMESTAMP"
	case strings.HasPrefix(u, "TEXT") || strings.HasPrefix(u, "JSON"):
		return "CLOB"
	default:
		return t
	}
}

// ---- constraints ----

func (d DataSafety) addConstraintProbe(ctx context.Context, sess Session, table, action string) []Finding {
	up := strings.ToUpper(action)
	notValid := d.Dialect == engine.Postgres && strings.Contains(up, "NOT VALID")

	switch {
	case strings.Contains(up, "UNIQUE") || strings.Contains(up, "PRIMARY KEY"):
		cols := firstParen(action)
		if cols == "" {
			return nil
		}
		dupSQL := fmt.Sprintf(
			`SELECT count(*) FROM (SELECT 1 FROM %s GROUP BY %s HAVING count(*) > 1) d`,
			q(d.Dialect, table), cols)
		n, err := probe(ctx, sess, dupSQL)
		if err != nil {
			return []Finding{finding(Info,
				"could not probe duplicate groups for uniqueness constraint",
				"probe error: "+err.Error(), "")}
		}
		if n > 0 {
			sev := Critical
			if notValid {
				sev = Warning
			}
			return []Finding{finding(sev,
				fmt.Sprintf("new uniqueness constraint on %s will fail: %d duplicate groups", table, n),
				fmt.Sprintf("duplicate groups = %d", n),
				"deduplicate first, or add NOT VALID then VALIDATE CONSTRAINT")}
		}
	case strings.Contains(up, "CHECK"):
		expr := firstParen(action)
		if expr == "" {
			return nil
		}
		n, err := probe(ctx, sess, fmt.Sprintf("SELECT count(*) FROM %s WHERE NOT (%s)",
			q(d.Dialect, table), expr))
		if err != nil {
			return []Finding{finding(Info,
				"could not evaluate CHECK expression against existing rows",
				"probe error: "+err.Error(), "")}
		}
		if n > 0 {
			sev := Critical
			if notValid {
				sev = Warning
			}
			return []Finding{finding(sev,
				fmt.Sprintf("CHECK constraint on %s is violated by %d rows", table, n),
				fmt.Sprintf("violating rows = %d", n),
				"fix the data or relax the constraint")}
		}
	case strings.Contains(up, "FOREIGN KEY"):
		m := regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(([^)]+)\)\s*REFERENCES\s+(\S+)\s*\(([^)]+)\)`).FindStringSubmatch(action)
		if m == nil {
			return []Finding{finding(Info,
				"composite or unparsed FK on "+table+" — orphan check skipped",
				"unsupported FK shape", "")}
		}
		col, refTable, refCol := strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3])
		if strings.Contains(col, ",") {
			return []Finding{finding(Info,
				"composite FK on "+table+" — orphan check skipped", "", "")}
		}
		orphanSQL := fmt.Sprintf(
			`SELECT count(*) FROM %s t WHERE t.%s IS NOT NULL AND NOT EXISTS (SELECT 1 FROM %s r WHERE r.%s = t.%s)`,
			q(d.Dialect, table), col, q(d.Dialect, refTable), refCol, col)
		n, err := probe(ctx, sess, orphanSQL)
		if err != nil {
			return nil
		}
		if n > 0 {
			sev := Critical
			if notValid {
				sev = Warning
			}
			suggestion := "clean orphans first"
			if notValid {
				suggestion += ", or plan a two-step NOT VALID rollout"
			}
			return []Finding{finding(sev,
				fmt.Sprintf("foreign key on %s.%s has %d orphan rows", table, col, n),
				fmt.Sprintf("orphan rows = %d", n), suggestion)}
		}
	}
	return nil
}

// firstParen returns the contents of the first parenthesized group.
func firstParen(s string) string {
	i := strings.Index(s, "(")
	if i < 0 {
		return ""
	}
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[i+1 : j]
			}
		}
	}
	return ""
}
