package findings

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Suppression directives are human judgement already written into the source.
// A maintainer who writes `//nolint:staticcheck // SA2001: intentional` has
// told us this rule fires here and is wrong here. Reporting it anyway is the
// fastest way to get a tool ignored; mining it is free labeled data.
//
// Two conventions are honored:
//
//	//nolint                       golangci-lint, all linters, this line
//	//nolint:staticcheck,gosec     golangci-lint, named linters, this line
//	//lint:ignore SA2001 reason    staticcheck, the line that follows
//	//lint:file-ignore SA2001 r    staticcheck, the whole file
var (
	nolintRe         = regexp.MustCompile(`//\s*nolint(?::\s*([\w\-,\s]+))?`)
	lintIgnoreRe     = regexp.MustCompile(`//\s*lint:ignore\s+([\w,\s]+)`)
	lintFileIgnoreRe = regexp.MustCompile(`//\s*lint:file-ignore\s+([\w,\s]+)`)
)

// sourceCache holds the lines of files already read, because one file usually
// carries many findings.
type sourceCache struct {
	repoDir string
	files   map[string][]string
}

func newSourceCache(repoDir string) *sourceCache {
	return &sourceCache{repoDir: repoDir, files: map[string][]string{}}
}

func (c *sourceCache) lines(relPath string) []string {
	if l, ok := c.files[relPath]; ok {
		return l
	}

	var lines []string
	f, err := os.Open(filepath.Join(c.repoDir, filepath.FromSlash(relPath)))
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
	}

	c.files[relPath] = lines
	return lines
}

// suppressedBy returns the directive that dismisses this finding, or "".
func (c *sourceCache) suppressedBy(f Finding) string {
	lines := c.lines(f.File)
	if len(lines) == 0 || f.Line < 1 {
		return ""
	}

	// File-wide ignores sit near the top of the file by convention.
	head := len(lines)
	if head > 50 {
		head = 50
	}
	for i := 0; i < head; i++ {
		if m := lintFileIgnoreRe.FindStringSubmatch(lines[i]); m != nil && rulesMatch(m[1], f.Rule) {
			return "lint:file-ignore " + strings.TrimSpace(m[1])
		}
	}

	// golangci-lint directives apply to their own line, or to the line below
	// when they sit alone above it.
	if d := matchNolint(lineAt(lines, f.Line), f); d != "" {
		return d
	}
	if d := matchNolint(lineAt(lines, f.Line-1), f); d != "" {
		return d
	}

	// staticcheck's own directive always precedes the offending line.
	if m := lintIgnoreRe.FindStringSubmatch(lineAt(lines, f.Line-1)); m != nil && rulesMatch(m[1], f.Rule) {
		return "lint:ignore " + strings.TrimSpace(m[1])
	}

	return ""
}

func matchNolint(line string, f Finding) string {
	m := nolintRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	// A bare //nolint silences every linter on that line.
	if strings.TrimSpace(m[1]) == "" {
		return "nolint"
	}

	names := linterNames(f.Tool, f.Rule)
	for _, name := range strings.Split(m[1], ",") {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "all" || names[name] {
			return "nolint:" + name
		}
	}
	return ""
}

// linterNames maps our tool names onto the names golangci-lint users write in
// their directives, which are not the same strings.
func linterNames(tool, rule string) map[string]bool {
	switch tool {
	case "go vet":
		return map[string]bool{"govet": true, "vet": true}
	case "staticcheck":
		n := map[string]bool{"staticcheck": true}
		switch {
		case strings.HasPrefix(rule, "ST1"):
			n["stylecheck"] = true
		case strings.HasPrefix(rule, "S1"):
			n["gosimple"] = true
		}
		return n
	default:
		return map[string]bool{strings.ToLower(tool): true}
	}
}

func rulesMatch(list, rule string) bool {
	for _, r := range strings.Split(list, ",") {
		r = strings.TrimSpace(r)
		if r == rule || r == "all" {
			return true
		}
	}
	return false
}

func lineAt(lines []string, n int) string {
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}

// ---------------------------------------------------------------------------
// mining suppressions as training data
// ---------------------------------------------------------------------------

// HardNegative is a labeled example mined from a suppression the repository's
// own maintainers wrote. The rule fired, a human looked, and the human said no.
// That is exactly the kind of near-miss a classifier needs and almost never
// gets from bug-fix mining, which only ever yields positives.
type HardNegative struct {
	Label     string `json:"label"`  // always NOT_A_BUG
	Source    string `json:"source"` // always maintainer_suppression
	Repo      string `json:"repo"`
	Commit    string `json:"commit,omitempty"`
	Tool      string `json:"tool"`
	Rule      string `json:"rule"`
	Category  string `json:"category"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Message   string `json:"message"`
	Directive string `json:"directive"`
	Rationale string `json:"rationale,omitempty"`
	Snippet   string `json:"snippet"`
}

// MineNegatives turns comment-suppressed findings into labeled records. It
// deliberately skips vendored and generated suppressions: nobody judged those,
// they were filtered structurally.
func MineNegatives(repoDir string, r Report) []HardNegative {
	src := newSourceCache(repoDir)
	out := make([]HardNegative, 0, len(r.Suppressed))

	for _, f := range r.Suppressed {
		if f.SuppressReason == "" ||
			f.SuppressReason == "vendored" ||
			f.SuppressReason == "generated" {
			continue
		}

		lines := src.lines(f.File)
		out = append(out, HardNegative{
			Label:     "NOT_A_BUG",
			Source:    "maintainer_suppression",
			Repo:      r.Repo,
			Commit:    r.Commit,
			Tool:      f.Tool,
			Rule:      f.Rule,
			Category:  f.Category,
			File:      f.File,
			Line:      f.Line,
			Message:   f.Message,
			Directive: f.SuppressReason,
			Rationale: rationale(lines, f.Line),
			Snippet:   snippet(lines, f.Line, 6),
		})
	}
	return out
}

// rationale pulls the human explanation that follows a directive, which is the
// most valuable part of the record: it says *why* the rule was wrong here.
func rationale(lines []string, lineNo int) string {
	for _, n := range []int{lineNo, lineNo - 1} {
		line := lineAt(lines, n)
		loc := nolintRe.FindStringIndex(line)
		if loc == nil {
			loc = lintIgnoreRe.FindStringIndex(line)
		}
		if loc == nil {
			continue
		}
		// The explanation is whatever trails the directive, usually after a
		// second comment marker.
		rest := line[loc[1]:]
		if i := strings.Index(rest, "//"); i >= 0 {
			rest = rest[i+2:]
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			return rest
		}
	}
	return ""
}

func snippet(lines []string, lineNo, radius int) string {
	if len(lines) == 0 {
		return ""
	}
	lo := lineNo - radius
	if lo < 1 {
		lo = 1
	}
	hi := lineNo + radius
	if hi > len(lines) {
		hi = len(lines)
	}
	return strings.Join(lines[lo-1:hi], "\n")
}
