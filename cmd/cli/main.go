// Command truebug scans one Go repository and emits a ranked JSON report.
//
// Everything it reports is a *candidate*, not a confirmed bug. Ranking decides
// reading order; later stages decide what is real.
//
// Usage:
//
//	truebug -repo /path/to/go/repo
//	truebug -repo /path/to/go/repo -out findings.json
//	truebug -repo /path/to/go/repo -min-priority high
//	truebug -repo /path/to/go/repo -negatives negatives.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Sievehouse/truebug/internal/analyzers"
	"github.com/Sievehouse/truebug/internal/findings"
)

var priorityRank = map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}

func main() {
	repoFlag := flag.String("repo", ".", "path to the Go module root to analyze")
	outFlag := flag.String("out", "", "write the JSON report here (default: stdout)")
	minFlag := flag.String("min-priority", "low", "drop findings below this priority (low|medium|high|critical)")
	negFlag := flag.String("negatives", "", "mine maintainer suppressions into this JSONL file as labeled hard negatives")
	flag.Parse()

	minRank, ok := priorityRank[strings.ToLower(*minFlag)]
	if !ok {
		fatal(fmt.Errorf("unknown -min-priority %q", *minFlag))
	}

	repoDir, err := filepath.Abs(*repoFlag)
	if err != nil {
		fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		fatal(fmt.Errorf("no go.mod in %s: point -repo at a Go module root", repoDir))
	}

	report := analyzers.Scan(repoDir)

	if minRank > 0 {
		kept := report.Findings[:0]
		for _, f := range report.Findings {
			if priorityRank[f.Priority] >= minRank {
				kept = append(kept, f)
			}
		}
		report.Findings = kept
	}

	blob, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
	}
	if *outFlag != "" {
		if err := os.WriteFile(*outFlag, blob, 0o644); err != nil {
			fatal(err)
		}
	} else {
		fmt.Println(string(blob))
	}

	summarize(report, *outFlag)

	if *negFlag != "" {
		writeNegatives(repoDir, report, *negFlag)
	}
}

// writeNegatives dumps mined suppressions as newline-delimited JSON, the format
// the training pipeline reads.
func writeNegatives(repoDir string, report findings.Report, path string) {
	negs := findings.MineNegatives(repoDir, report)

	var buf strings.Builder
	for _, n := range negs {
		line, err := json.Marshal(n)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "mined %d hard negatives -> %s\n", len(negs), path)
}

// summarize writes a human-readable digest to stderr, so stdout stays pure
// JSON and remains pipeable.
func summarize(r findings.Report, outPath string) {
	bar := strings.Repeat("-", 62)

	fmt.Fprintf(os.Stderr, "\n%s\n", bar)
	fmt.Fprintf(os.Stderr, "repo:   %s\n", r.Repo)
	if len(r.Commit) >= 12 {
		fmt.Fprintf(os.Stderr, "commit: %s\n", r.Commit[:12])
	}
	for _, t := range r.Tools {
		if t.Ran {
			fmt.Fprintf(os.Stderr, "  %-14s %4d raw  (%.2fs)\n", t.Name, t.Findings, t.Seconds)
		} else {
			fmt.Fprintf(os.Stderr, "  %-14s SKIPPED: %s\n", t.Name, t.Error)
		}
	}

	byPriority := map[string]int{}
	tests := 0
	for _, f := range r.Findings {
		byPriority[f.Priority]++
		if f.IsTest {
			tests++
		}
	}

	structural, byComment := 0, 0
	for _, f := range r.Suppressed {
		if f.SuppressReason == "vendored" || f.SuppressReason == "generated" {
			structural++
		} else {
			byComment++
		}
	}

	fmt.Fprintf(os.Stderr, "%s\n", bar)
	fmt.Fprintf(os.Stderr, "reported: %d   (%d in tests)\n", len(r.Findings), tests)
	fmt.Fprintf(os.Stderr, "suppressed: %d   (%d vendored/generated, %d by maintainer comment)\n",
		len(r.Suppressed), structural, byComment)
	for _, p := range []string{"critical", "high", "medium", "low"} {
		if byPriority[p] > 0 {
			fmt.Fprintf(os.Stderr, "  %-9s %d\n", p, byPriority[p])
		}
	}

	top := r.Findings
	if len(top) > 10 {
		top = top[:10]
	}
	if len(top) > 0 {
		fmt.Fprintf(os.Stderr, "%s\ntop findings:\n", bar)
		for i, f := range top {
			marker := ""
			if f.IsTest {
				marker = "  [test]"
			}
			fmt.Fprintf(os.Stderr, "%2d. [%3d %-8s] %-9s %s:%d%s\n     %s\n",
				i+1, f.Score, f.Priority, f.Rule, f.File, f.Line, marker, truncate(f.Message, 72))
		}
	}

	if outPath != "" {
		fmt.Fprintf(os.Stderr, "\nwrote %s\n", outPath)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "..."
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "truebug: %v\n", err)
	os.Exit(1)
}
