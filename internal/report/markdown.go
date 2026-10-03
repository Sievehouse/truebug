package report

import (
    "fmt"
    "io"

    "github.com/Sievehouse/truebug/internal/findings"
)

// Markdown writes a report for a pull request comment or an issue.
//
// It always states which tools ran and which did not. A scanner that silently
// skipped staticcheck and reported nothing looks exactly like a clean
// repository, and that is the most dangerous output this tool can produce.
func Markdown(w io.Writer, r findings.Report, limit int) error {
    if _, err := fmt.Fprint(w, "# truebug report\n\n"); err != nil {
        return err
    }

    fmt.Fprintf(w, "**Repository:** `%s`\n\n", r.Repo)
    if r.Commit != "" {
        fmt.Fprintf(w, "**Commit:** `%s`\n\n", truncate(r.Commit, 12))
    }

    fmt.Fprint(w, "## Tools\n\n| Tool | Ran | Raw findings | Seconds |\n| --- | --- | --- | --- |\n")
    for _, t := range r.Tools {
        status := "yes"
        if !t.Ran {
            status = "**no** — " + t.Error
        }
        fmt.Fprintf(w, "| %s | %s | %d | %.2f |\n", t.Name, status, t.Findings, t.Seconds)
    }

    byPriority := map[string]int{}
    tests := 0
    for _, f := range r.Findings {
        byPriority[f.Priority]++
        if f.IsTest {
            tests++
        }
    }

    fmt.Fprint(w, "\n## Summary\n\n")
    fmt.Fprintf(w, "- reported: **%d** (%d in test files)\n", len(r.Findings), tests)
    fmt.Fprintf(w, "- suppressed: %d\n", len(r.Suppressed))
    for _, p := range []string{"critical", "high", "medium", "low"} {
        if byPriority[p] > 0 {
            fmt.Fprintf(w, "- %s: %d\n", p, byPriority[p])
        }
    }

    top := r.Findings
    if limit > 0 && len(top) > limit {
        top = top[:limit]
    }

    if len(top) == 0 {
        fmt.Fprint(w, "\nNothing above the reporting threshold.\n")
        return nil
    }

    fmt.Fprint(w, "\n## Findings\n\n")
    for i, f := range top {
        marker := ""
        if f.IsTest {
            marker = " _(test file)_"
        }
        fmt.Fprintf(w, "### %d. `%s` — %s%s\n\n", i+1, f.Rule, f.Priority, marker)
        fmt.Fprintf(w, "`%s:%d` · %s · score %d · via %s\n\n", f.File, f.Line, f.Category, f.Score, f.Tool)
        fmt.Fprintf(w, "%s\n\n", f.Message)
    }

    fmt.Fprint(w, "\n---\n\nEvery item above is a *candidate*. None has been verified by execution.\n")
    return nil
}

func truncate(s string, n int) string {
    if len(s) <= n {
        return s
    }
    return s[:n]
}
