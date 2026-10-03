// Command truebug runs Go static analyzers over a repository and emits a
// single normalized JSON report.
//
// This is the candidate-generation stage: every finding here is a *candidate*,
// not a confirmed bug. Later stages (triage, verification) filter this down.
//
// Usage:
//
//	truebug -repo /path/to/go/repo
//	truebug -repo /path/to/go/repo -out findings.json
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Finding is one normalized result from any analyzer. Every tool we add gets
// mapped into this shape, so downstream stages never care which tool spoke.
type Finding struct {
	Fingerprint string `json:"fingerprint"`
	Tool        string `json:"tool"`
	Rule        string `json:"rule"`
	Category    string `json:"category"`
	Severity    string `json:"severity"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Column      int    `json:"column"`
	Message     string `json:"message"`
}

// ToolRun records whether an analyzer actually ran. Without this, a missing
// binary looks identical to a clean repo, which is the most dangerous kind of
// silent failure in a scanner.
type ToolRun struct {
	Name     string  `json:"name"`
	Ran      bool    `json:"ran"`
	Error    string  `json:"error,omitempty"`
	Findings int     `json:"findings"`
	Seconds  float64 `json:"seconds"`
}

type Report struct {
	Repo        string    `json:"repo"`
	Commit      string    `json:"commit,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	Tools       []ToolRun `json:"tools"`
	Findings    []Finding `json:"findings"`
}

type analyzer interface {
	Name() string
	Run(repoDir string) ([]Finding, error)
}

func main() {
	repoFlag := flag.String("repo", ".", "path to the Go module root to analyze")
	outFlag := flag.String("out", "", "write the JSON report here (default: stdout)")
	flag.Parse()

	repoDir, err := filepath.Abs(*repoFlag)
	if err != nil {
		fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		fatal(fmt.Errorf("no go.mod in %s: point -repo at a Go module root", repoDir))
	}

	report := Report{
		Repo:        repoDir,
		Commit:      gitCommit(repoDir),
		GeneratedAt: time.Now().UTC(),
	}

	analyzers := []analyzer{vetAnalyzer{}, staticcheckAnalyzer{}}
	for _, a := range analyzers {
		start := time.Now()
		findings, err := a.Run(repoDir)
		run := ToolRun{Name: a.Name(), Seconds: round2(time.Since(start).Seconds())}
		if err != nil {
			run.Error = err.Error()
		} else {
			run.Ran = true
			run.Findings = len(findings)
			report.Findings = append(report.Findings, findings...)
		}
		report.Tools = append(report.Tools, run)
	}

	report.Findings = dedupe(report.Findings)
	sort.Slice(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Rule < b.Rule
	})

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

func (vetAnalyzer) Run(repoDir string) ([]Finding, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return nil, fmt.Errorf("go not found on PATH")
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = repoDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// A non-zero exit means vet found something, which is not an error here.
	// Only a missing binary (checked above) is a real failure.
	_ = cmd.Run()

	return parseVet(repoDir, stderr.String()+stdout.String()), nil
}

// go vet emits "file:line:col: message", interleaved with "# package" headers.
var vetLineRe = regexp.MustCompile(`^(.*?):(\d+):(\d+):\s+(.*)$`)

func parseVet(repoDir, output string) []Finding {
	var findings []Finding
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

		findings = append(findings, newFinding(
			"go vet", rule, categoryForVet(rule), "medium",
			relPath(repoDir, m[1]), lineNo, colNo, msg,
		))
	}
	return findings
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
	case strings.Contains(lower, "non-constant format string"):
		return "printf"
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

func (staticcheckAnalyzer) Run(repoDir string) ([]Finding, error) {
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

func parseStaticcheck(repoDir, output string) []Finding {
	var findings []Finding
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
		findings = append(findings, newFinding(
			"staticcheck", r.Code, categoryForStaticcheck(r.Code),
			severityForStaticcheck(r.Severity),
			relPath(repoDir, r.Location.File), r.Location.Line, r.Location.Column,
			r.Message,
		))
	}
	return findings
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

func severityForStaticcheck(s string) string {
	switch strings.ToLower(s) {
	case "error":
		return "high"
	case "warning":
		return "medium"
	default:
		return "low"
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newFinding builds a Finding with a fingerprint that deliberately excludes the
// line number, so unrelated edits above a finding do not make it look new.
func newFinding(tool, rule, category, severity, file string, line, col int, msg string) Finding {
	key := strings.Join([]string{tool, rule, file, msg}, "|")
	sum := sha256.Sum256([]byte(key))

	return Finding{
		Fingerprint: hex.EncodeToString(sum[:])[:16],
		Tool:        tool,
		Rule:        rule,
		Category:    category,
		Severity:    severity,
		File:        file,
		Line:        line,
		Column:      col,
		Message:     msg,
	}
}

// dedupe drops exact repeats, which happen when a package is analyzed under
// several build configurations.
func dedupe(in []Finding) []Finding {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, f := range in {
		key := fmt.Sprintf("%s:%d:%d", f.Fingerprint, f.Line, f.Column)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

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

// summarize writes a short human-readable digest to stderr, so stdout stays
// pure JSON and remains pipeable.
func summarize(r Report, outPath string) {
	byCategory := map[string]int{}
	for _, f := range r.Findings {
		byCategory[f.Category]++
	}
	cats := make([]string, 0, len(byCategory))
	for c := range byCategory {
		cats = append(cats, c)
	}
	sort.Slice(cats, func(i, j int) bool { return byCategory[cats[i]] > byCategory[cats[j]] })

	fmt.Fprintf(os.Stderr, "\n%s\n", strings.Repeat("-", 46))
	fmt.Fprintf(os.Stderr, "repo:     %s\n", r.Repo)
	if r.Commit != "" {
		fmt.Fprintf(os.Stderr, "commit:   %s\n", r.Commit[:min(12, len(r.Commit))])
	}
	for _, t := range r.Tools {
		if t.Ran {
			fmt.Fprintf(os.Stderr, "%-14s %4d findings  (%.2fs)\n", t.Name, t.Findings, t.Seconds)
		} else {
			fmt.Fprintf(os.Stderr, "%-14s SKIPPED: %s\n", t.Name, t.Error)
		}
	}
	fmt.Fprintf(os.Stderr, "%s\n", strings.Repeat("-", 46))
	fmt.Fprintf(os.Stderr, "total candidates: %d\n", len(r.Findings))
	for _, c := range cats {
		fmt.Fprintf(os.Stderr, "  %-18s %d\n", c, byCategory[c])
	}
	if outPath != "" {
		fmt.Fprintf(os.Stderr, "\nwrote %s\n", outPath)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "truebug: %v\n", err)
	os.Exit(1)
}
