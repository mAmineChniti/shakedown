// Package report renders migration rehearsal results as text, JSON, or
// markdown (for PR comments).
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/mAmineChniti/shakedown/internal/checks"
	"github.com/mAmineChniti/shakedown/internal/runner"
)

// ansi colors; disabled automatically for non-terminals and NO_COLOR.
const (
	cReset  = "\x1b[0m"
	cBold   = "\x1b[1m"
	cDim    = "\x1b[2m"
	cRed    = "\x1b[31m"
	cGreen  = "\x1b[32m"
	cYellow = "\x1b[33m"
	cCyan   = "\x1b[36m"
)

func useColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func paint(color, s string, on bool) string {
	if !on {
		return s
	}
	return color + s + cReset
}

func sevColor(s checks.Severity, on bool) string {
	switch s {
	case checks.Critical:
		return paint(cRed+cBold, string(s), on)
	case checks.Warning:
		return paint(cYellow, string(s), on)
	default:
		return paint(cCyan, string(s), on)
	}
}

// Render writes results in the requested format.
func Render(w io.Writer, results []runner.MigrationResult, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	case "markdown", "md":
		return renderMarkdown(w, results)
	default:
		return renderText(w, results, useColor(w))
	}
}

// MaxSeverity returns the highest severity across all findings and
// migration errors, used for threshold exit codes.
func MaxSeverity(results []runner.MigrationResult) checks.Severity {
	max := checks.Info
	consider := func(fs []checks.Finding) {
		for _, f := range fs {
			if f.Severity.Rank() > max.Rank() {
				max = f.Severity
			}
		}
	}
	for _, r := range results {
		if r.Err != "" && checks.Critical.Rank() > max.Rank() {
			max = checks.Critical
		}
		consider(r.Findings)
		consider(r.Rollback)
	}
	return max
}

func renderText(w io.Writer, results []runner.MigrationResult, color bool) error {
	for _, r := range results {
		status := paint(cGreen, "ok", color)
		if r.Err != "" {
			status = paint(cRed+cBold, "FAILED", color) + ": " + r.Err
		}
		fmt.Fprintf(w, "%s  %s  %s\n",
			paint(cBold, r.File, color), status,
			paint(cDim, fmt.Sprintf("(%s, %d rows affected, %d lock observations)",
				r.Duration.Round(1e6), r.RowsAffected, len(r.Locks)), color))
		for _, l := range r.Locks {
			fmt.Fprintf(w, "    %s %s on %s (granted=%v)\n",
				paint(cDim, "lock:", color), l.Mode, l.Relation, l.Granted)
		}
		for _, f := range r.Findings {
			fmt.Fprintf(w, "    [%s] %s", sevColor(f.Severity, color), f.Message)
			if f.Evidence != "" {
				fmt.Fprintf(w, " %s %s", paint(cDim, "—", color), paint(cDim, f.Evidence, color))
			}
			fmt.Fprintln(w)
			if f.Suggestion != "" {
				fmt.Fprintf(w, "        %s %s\n",
					paint(cDim, "suggestion:", color), paint(cDim, f.Suggestion, color))
			}
		}
		for _, f := range r.Rollback {
			fmt.Fprintf(w, "    [%s/%s] %s", paint(cDim, "rollback", color),
				sevColor(f.Severity, color), f.Message)
			if f.Evidence != "" {
				fmt.Fprintf(w, " %s %s", paint(cDim, "—", color), paint(cDim, f.Evidence, color))
			}
			fmt.Fprintln(w)
		}
	}
	return nil
}

func renderMarkdown(w io.Writer, results []runner.MigrationResult) error {
	fmt.Fprintln(w, "### shakedown migration report")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Migration | Result | Duration | Rows | Findings |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, r := range results {
		status := "ok"
		if r.Err != "" {
			status = "**FAILED**: " + r.Err
		}
		n := len(r.Findings) + len(r.Rollback)
		fmt.Fprintf(w, "| `%s` | %s | %s | %d | %d |\n",
			r.File, status, r.Duration.Round(1e6), r.RowsAffected, n)
	}
	fmt.Fprintln(w)

	sevIcon := func(s checks.Severity) string {
		switch s {
		case checks.Critical:
			return "🔴"
		case checks.Warning:
			return "🟡"
		default:
			return "🔵"
		}
	}
	for _, r := range results {
		var lines []string
		for _, f := range r.Findings {
			lines = append(lines, fmt.Sprintf("- %s **%s**: %s%s",
				sevIcon(f.Severity), f.Severity, f.Message, evSuffix(f)))
			if f.Suggestion != "" {
				lines = append(lines, fmt.Sprintf("  - Suggestion: %s", f.Suggestion))
			}
		}
		for _, l := range r.Locks {
			lines = append(lines, fmt.Sprintf("- lock observed: `%s` on `%s`", l.Mode, l.Relation))
		}
		for _, f := range r.Rollback {
			lines = append(lines, fmt.Sprintf("- %s **rollback/%s**: %s%s",
				sevIcon(f.Severity), f.Severity, f.Message, evSuffix(f)))
		}
		if len(lines) == 0 {
			continue
		}
		fmt.Fprintf(w, "**`%s`**\n\n", r.File)
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func evSuffix(f checks.Finding) string {
	if f.Evidence == "" {
		return ""
	}
	return " (" + f.Evidence + ")"
}

// SummaryLine is used by callers that want a one-line verdict.
func SummaryLine(results []runner.MigrationResult) string {
	var b strings.Builder
	for _, r := range results {
		if r.Err != "" {
			fmt.Fprintf(&b, "%s: FAILED; ", r.File)
			continue
		}
		counts := map[checks.Severity]int{}
		for _, f := range r.Findings {
			counts[f.Severity]++
		}
		for _, f := range r.Rollback {
			counts[f.Severity]++
		}
		fmt.Fprintf(&b, "%s: %d critical, %d warnings, %d info; ",
			r.File, counts[checks.Critical], counts[checks.Warning], counts[checks.Info])
	}
	return strings.TrimSuffix(b.String(), "; ")
}
