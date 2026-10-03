// Package findings holds the normalized finding type shared by every analyzer,
// plus the ranking that decides what a human should look at first.
//
// The key idea: analyzer-reported severity is not comparable across tools, and
// no tool knows anything about the repository it is running on. So we keep what
// the tool said in Severity, and compute our own Priority from a rule table
// plus context about the file the finding lives in.
package findings

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Finding is one normalized result from any analyzer.
type Finding struct {
	Fingerprint string `json:"fingerprint"`
	Tool        string `json:"tool"`
	Rule        string `json:"rule"`
	Category    string `json:"category"`

	// Severity is whatever the tool claimed. Priority and Score are ours.
	Severity string `json:"severity"`
	Priority string `json:"priority"`
	Score    int    `json:"score"`

	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`

	IsTest      bool `json:"is_test"`
	IsGenerated bool `json:"is_generated"`
	IsVendored  bool `json:"is_vendored"`

	// SuppressReason is why this finding was held back: "vendored",
	// "generated", or the maintainer's own directive text.
	SuppressReason string `json:"suppress_reason,omitempty"`
}

// ToolRun records whether an analyzer actually ran. Without it, a missing
// binary is indistinguishable from a clean repository.
type ToolRun struct {
	Name     string  `json:"name"`
	Ran      bool    `json:"ran"`
	Error    string  `json:"error,omitempty"`
	Findings int     `json:"findings"`
	Seconds  float64 `json:"seconds"`
}

// Report is the full output of one scan.
type Report struct {
	Repo        string    `json:"repo"`
	Commit      string    `json:"commit,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
	Tools       []ToolRun `json:"tools"`
	Findings    []Finding `json:"findings"`
	Suppressed  []Finding `json:"suppressed"`
}

// New builds a Finding with a fingerprint that deliberately excludes the line
// number, so unrelated edits above a finding do not make it look like a new
// one. This is what lets findings be diffed between commits.
func New(tool, rule, category, severity, file string, line, col int, msg string) Finding {
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

// Dedupe drops exact repeats, which occur when a package is analyzed under
// several build configurations or reported by two tools at once.
func Dedupe(in []Finding) []Finding {
	seen := make(map[string]bool, len(in))
	out := make([]Finding, 0, len(in))
	for _, f := range in {
		key := f.Fingerprint + ":" + itoa(f.Line) + ":" + itoa(f.Column)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// Rank classifies each finding's file, scores it, and splits the results into
// what to show and what to hold back. Vendored and generated code is still
// analyzed (it affects name resolution) but is never reported by default: the
// maintainer cannot fix code they did not write.
func Rank(repoDir string, in []Finding) (kept, suppressed []Finding) {
	genCache := map[string]bool{}
	src := newSourceCache(repoDir)

	for _, f := range in {
		f.IsTest = isTestFile(f.File)
		f.IsVendored = isVendored(f.File)
		f.IsGenerated = isGenerated(repoDir, f.File, genCache)

		f.Score = score(f)
		f.Priority = priorityFor(f.Score)

		// Structural filters first, then the maintainer's own judgement.
		switch {
		case f.IsVendored:
			f.SuppressReason = "vendored"
		case f.IsGenerated:
			f.SuppressReason = "generated"
		default:
			f.SuppressReason = src.suppressedBy(f)
		}

		if f.SuppressReason != "" {
			suppressed = append(suppressed, f)
			continue
		}
		kept = append(kept, f)
	}

	sortByScore(kept)
	sortByScore(suppressed)
	return kept, suppressed
}

func sortByScore(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Score != fs[j].Score {
			return fs[i].Score > fs[j].Score
		}
		if fs[i].File != fs[j].File {
			return fs[i].File < fs[j].File
		}
		return fs[i].Line < fs[j].Line
	})
}

// ---------------------------------------------------------------------------
// scoring
// ---------------------------------------------------------------------------

// score starts from the rule's own prior and discounts it by where the finding
// lives. A context leak in production code and the same leak in a test are not
// the same problem.
func score(f Finding) int {
	s := float64(rulePrior(f.Rule))

	switch {
	case f.IsVendored:
		s *= 0.05
	case f.IsGenerated:
		s *= 0.10
	case f.IsTest:
		s *= 0.30
	}

	out := int(s + 0.5)
	if out < 1 {
		out = 1
	}
	return out
}

func priorityFor(s int) string {
	switch {
	case s >= 80:
		return "critical"
	case s >= 55:
		return "high"
	case s >= 30:
		return "medium"
	default:
		return "low"
	}
}

// rulePriors is our own judgement of how often a rule fires on a real bug, not
// the tool's. These numbers are priors to be replaced by measured per-rule
// precision once there is an adjudicated benchmark to measure against.
var rulePriors = map[string]int{
	// Concurrency and context: the expensive, hard-to-spot failures.
	"lostcancel":  85, // leaked context cancel func
	"copylocks":   85, // mutex copied by value
	"loopclosure": 80, // loop variable captured by goroutine
	"SA2000":      85, // WaitGroup.Add called inside the goroutine
	"SA2001":      80, // empty critical section
	"SA2002":      70, // testing.T used from a goroutine
	"SA2003":      85, // deferred Lock right after Lock
	"SA1012":      80, // nil context passed
	"SA1015":      75, // time.Tick leaks in non-terminating use

	// Correctness.
	"SA5001": 85, // defer before checking the error
	"SA5007": 80, // infinite recursive call
	"SA5011": 80, // possible nil dereference
	"SA4006": 55, // value never read
	"SA4010": 55, // append result never used
	"SA1019": 40, // deprecated API
	"SA1029": 45, // built-in type as context key
	"SA9003": 25, // empty branch

	// Hygiene.
	"unreachable": 30,
	"assign":      35,
	"printf":      50,
	"structtag":   45,
	"unsafeptr":   70,
	"vet.other":   40,
}

// rulePrior falls back to the rule family when an individual rule is unlisted,
// so adding a new analyzer does not silently score everything at the default.
func rulePrior(rule string) int {
	if p, ok := rulePriors[rule]; ok {
		return p
	}
	switch {
	case strings.HasPrefix(rule, "SA2"): // concurrency
		return 70
	case strings.HasPrefix(rule, "SA5"): // correctness
		return 65
	case strings.HasPrefix(rule, "SA1"): // API misuse
		return 45
	case strings.HasPrefix(rule, "SA4"): // dead or redundant code
		return 45
	case strings.HasPrefix(rule, "SA6"): // performance
		return 25
	case strings.HasPrefix(rule, "SA9"): // dubious constructs
		return 40
	case strings.HasPrefix(rule, "S1"): // simplifications
		return 15
	case strings.HasPrefix(rule, "ST1"), strings.HasPrefix(rule, "QF1"): // style
		return 10
	default:
		return 40
	}
}

// ---------------------------------------------------------------------------
// file classification
// ---------------------------------------------------------------------------

func isTestFile(path string) bool {
	p := filepath.ToSlash(path)
	return strings.HasSuffix(p, "_test.go") ||
		strings.Contains(p, "/testdata/") ||
		strings.HasPrefix(p, "testdata/")
}

func isVendored(path string) bool {
	p := filepath.ToSlash(path)
	return strings.HasPrefix(p, "vendor/") || strings.Contains(p, "/vendor/")
}

// generatedMarker is the convention every Go code generator is expected to
// follow: https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source
var generatedMarker = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// isGenerated reads the head of the file looking for the standard marker. The
// cache matters because one file usually carries many findings.
func isGenerated(repoDir, relPath string, cache map[string]bool) bool {
	if v, ok := cache[relPath]; ok {
		return v
	}

	result := false
	f, err := os.Open(filepath.Join(repoDir, filepath.FromSlash(relPath)))
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		// The marker must appear before the package clause, so a short read is
		// enough; stop as soon as real code begins.
		for i := 0; i < 20 && sc.Scan(); i++ {
			line := strings.TrimSpace(sc.Text())
			if generatedMarker.MatchString(line) {
				result = true
				break
			}
			if strings.HasPrefix(line, "package ") {
				break
			}
		}
	}

	cache[relPath] = result
	return result
}

// itoa avoids pulling strconv in for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
