// Package analyzers wraps external Go static analysis tools and normalizes
// whatever they emit into the shared finding type.
//
// Each tool is an adapter. Adding a new one means implementing Analyzer and
// writing a parser plus a category mapping; nothing downstream changes.
package analyzers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Sievehouse/truebug/internal/findings"
)

// Analyzer is one external tool.
type Analyzer interface {
	Name() string
	Run(repoDir string) ([]findings.Finding, error)
}

// Default is the analyzer set every scan runs.
func Default() []Analyzer {
	return []Analyzer{Vet{}, Staticcheck{}}
}

// Scan runs every analyzer over a repository and returns a ranked report.
// A tool that is missing is recorded as not-run rather than silently producing
// zero findings, since the two look identical in the output otherwise.
func Scan(repoDir string) findings.Report {
	report := findings.Report{
		Repo:        repoDir,
		Commit:      GitCommit(repoDir),
		GeneratedAt: time.Now().UTC(),
	}

	var all []findings.Finding
	for _, a := range Default() {
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

	report.Findings, report.Suppressed = findings.Rank(repoDir, findings.Dedupe(all))
	return report
}

// ---------------------------------------------------------------------------
// go vet
// ---------------------------------------------------------------------------

type Vet struct{}

func (Vet) Name() string { return "go vet" }

func (Vet) Run(repoDir string) ([]findings.Finding, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return nil, fmt.Errorf("go not found on PATH")
	}

	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = repoDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// A non-zero exit means vet found something, which is not a failure.
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
			RelPath(repoDir, m[1]), lineNo, colNo, msg,
		))
	}
	return out
}

// vetRule recovers the analyzer name, which go vet's plain output drops.
// Anything unmatched stays "vet.other" rather than being guessed at.
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

type Staticcheck struct{}

func (Staticcheck) Name() string { return "staticcheck" }

func (Staticcheck) Run(repoDir string) ([]findings.Finding, error) {
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
			RelPath(repoDir, r.Location.File), r.Location.Line, r.Location.Column,
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

// RelPath normalizes a tool-reported path to a repo-relative, slash-separated
// one, so findings are comparable across machines and operating systems.
func RelPath(repoDir, p string) string {
	p = strings.TrimPrefix(p, "./")
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	if rel, err := filepath.Rel(repoDir, p); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(p)
}

// GitCommit returns the checked-out commit, or "" if the directory is not a
// git repository.
func GitCommit(repoDir string) string {
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
