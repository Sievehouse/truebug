// Command truebug runs Go static analyzers over a repository and emits a
// single normalized, ranked JSON report.
//
// Everything it reports is a *candidate*, not a confirmed bug. Ranking decides
// reading order; later stages (triage, verification) decide what is real.
//
// Usage:
//
//	truebug -repo /path/to/go/repo
//	truebug -repo /path/to/go/repo -out findings.json
//	truebug -repo /path/to/go/repo -min-priority high
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sievehouse/truebug/internal/findings"
)

type analyzer interface {
	Name() string
	Run(repoDir string) ([]findings.Finding, error)
}

var priorityRank = map[string]int{"low": 0, "medium": 1, "high": 2, "critical": 3}

func main() {
	repoFlag := flag.String("repo", ".", "path to the Go module root to analyze")
	outFlag := flag.String("out", "", "write the JSON report here (default: stdout)")
	minFlag := flag.String("min-priority", "low", "drop findings below this priority (low|medium|high|critical)")
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

	report := findings.Report{
		Repo:        repoDir,
		Commit:      gitCommit(repoDir),
		GeneratedAt: time.Now().UTC(),
	}

	var all []findings.Finding
	for _, a := range []analyzer{vetAnalyzer{}, staticcheckAnalyzer{}} {
		start := time.Now()
		got, err := a.Run(repoDir)
		run := findings.ToolRun{Name: a.Name(), Seconds: round2(time.Since(start).Seconds())}
		if err != nil {
			run.Error = err.Error()
		} else {
			run.Ran = true
			run.Findings = len(got)
			all = append(all, got...)
		}
		report.Tools = append(report.Tools, run)
	}

	kept, suppressed := findings.Rank(repoDir, findings.Dedupe(all))

	if minRank > 0 {
		filtered := kept[:0]
		for _, f := range kept {
			if priorityRank[f.Priority] >= minRank {
				filtered = append(filtered, f)
			}
		}
		kept = filtered
	}
	report.Findings = kept
	report.Suppressed = suppressed

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
}

// ---------------------------------------------------------------------------
// go vet
// ---------------------------------------------------------------------------

type vetAnalyzer struct{}

func (vetAnalyzer) Name() string { return "go vet" }

func (vetAnalyzer) Run(repoDir string) ([]findings.Finding, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return nil, fmt.Errorf("go not found on PATH")
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = repoDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// A non-zero exit means vet found something, which is not a failure here.
	_ = cmd.Run()

	return parseVet(repoDir, stderr.String()+stdout.String()), nil
}

// go vet emits "file:line:col: message", interleaved with "# package" headers.
var vetLineRe = regexp.MustCompile(`^(.*?):(\d+):(\d+):\s+(.*)$`)

func parseVet(repoDir, output string) []findings.Finding {
	var out []findings.Finding
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := vetLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lineNo, _ := strconv.Atoi(m[2])
		colNo, _ := strconv.Atoi(m[3])
		msg := m[4]
		rule := vetRule(msg)

		out = append(out, findings.New(
			"go vet", rule, categoryForVet(rule), "medium",
			relPath(repoDir, m[1]), lineNo, colNo, msg,
		))
	}
	return out
}

// go vet's plain output drops the analyzer name, so recover it from the
// message. Anything unmatched stays "vet.other" rather than being guessed at.
func vetRule(msg string) string {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "cancel function"), strings.Contains(lower, "lost cancel"):
		return "lostcancel"
	case strings.Contains(lower, "passes lock by value"), strings.Contains(lower, "copies lock value"):
		return "copylocks"
	case strings.Contains(lower, "loop variable"):
		return "loopclosure"
	case strings.Contains(lower, "unreachable code"):
		return "unreachable"
	case strings.Contains(lower, "format"), strings.Contains(lower, "printf"):
		return "printf"
	case strings.Contains(lower, "self-assignment"):
		return "assign"
	case strings.Contains(lower, "possible misuse of"):
		return "unsafeptr"
	case strings.Contains(lower, "struct field"), strings.Contains(lower, "struct tag"):
		return "structtag"
	default:
		return "vet.other"
	}
}

func categoryForVet(rule string) string {
	switch rule {
	case "lostcancel":
		return "CTX.PROPAGATION"
	case "copylocks", "loopclosure":
		return "CONC.MISUSE"
	case "printf", "structtag", "unsafeptr":
		return "API.MISUSE"
	case "unreachable", "assign":
		return "CODE.SMELL"
	default:
		return "UNCATEGORIZED"
	}
}

// ---------------------------------------------------------------------------
// staticcheck
// ---------------------------------------------------------------------------

type staticcheckAnalyzer struct{}

func (staticcheckAnalyzer) Name() string { return "staticcheck" }

func (staticcheckAnalyzer) Run(repoDir string) ([]findings.Finding, error) {
	if _, err := exec.LookPath("staticcheck"); err != nil {
		return nil, fmt.Errorf(
			"staticcheck not found on PATH (install: go install honnef.co/go/tools/cmd/staticcheck@latest)")
	}
	cmd := exec.Command("staticcheck", "-f", "json", "./...")
	cmd.Dir = repoDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run()

	return parseStaticcheck(repoDir, stdout.String()), nil
}

type scResult struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Location struct {
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
	} `json:"location"`
}

func parseStaticcheck(repoDir, output string) []findings.Finding {
	var out []findings.Finding
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var r scResult
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		out = append(out, findings.New(
			"staticcheck", r.Code, categoryForStaticcheck(r.Code), r.Severity,
			relPath(repoDir, r.Location.File), r.Location.Line, r.Location.Column,
			r.Message,
		))
	}
	return out
}

func categoryForStaticcheck(code string) string {
	switch {
	case strings.HasPrefix(code, "SA1"):
		return "API.MISUSE"
	case strings.HasPrefix(code, "SA2"):
		return "CONC.MISUSE"
	case strings.HasPrefix(code, "SA3"):
		return "TEST.MISUSE"
	case strings.HasPrefix(code, "SA4"):
		return "CODE.SMELL"
	case strings.HasPrefix(code, "SA5"):
		return "CORRECTNESS"
	case strings.HasPrefix(code, "SA6"):
		return "PERF"
	case strings.HasPrefix(code, "SA9"):
		return "CORRECTNESS"
	case strings.HasPrefix(code, "S1"):
		return "CODE.SMELL"
	case strings.HasPrefix(code, "ST1"), strings.HasPrefix(code, "QF1"):
		return "CODE.STYLE"
	default:
		return "UNCATEGORIZED"
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func relPath(repoDir, p string) string {
	p = strings.TrimPrefix(p, "./")
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	if rel, err := filepath.Rel(repoDir, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

func gitCommit(repoDir string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
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

	fmt.Fprintf(os.Stderr, "%s\n", bar)
	fmt.Fprintf(os.Stderr, "reported: %d   (suppressed %d vendored/generated, %d in tests)\n",
		len(r.Findings), len(r.Suppressed), tests)
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
