package findings

import "testing"

func TestIsTestFile(t *testing.T) {
    cases := map[string]bool{
        "pkg/foo_test.go":   true,
        "pkg/foo.go":        false,
        "testdata/sample.go": true,
        "pkg/testdata/x.go": true,
        "pkg/contest.go":    false,
    }
    for path, want := range cases {
        if got := isTestFile(path); got != want {
            t.Errorf("isTestFile(%q) = %v, want %v", path, got, want)
        }
    }
}

func TestIsVendored(t *testing.T) {
    cases := map[string]bool{
        "vendor/github.com/x/y.go": true,
        "pkg/vendor/x.go":          true,
        "pkg/vendored.go":          false,
        "pkg/foo.go":               false,
    }
    for path, want := range cases {
        if got := isVendored(path); got != want {
            t.Errorf("isVendored(%q) = %v, want %v", path, got, want)
        }
    }
}

func TestRulePriorFallsBackToFamily(t *testing.T) {
    if got := rulePrior("lostcancel"); got != 85 {
        t.Errorf("known rule: got %d, want 85", got)
    }
    // Unlisted SA2xxx rule should inherit the concurrency family prior.
    if got := rulePrior("SA2999"); got != 70 {
        t.Errorf("SA2 family: got %d, want 70", got)
    }
    if got := rulePrior("ZZ9999"); got != 40 {
        t.Errorf("unknown rule: got %d, want 40", got)
    }
}

func TestPriorityFor(t *testing.T) {
    cases := map[int]string{
        85: "critical",
        70: "high",
        40: "medium",
        14: "low",
    }
    for score, want := range cases {
        if got := priorityFor(score); got != want {
            t.Errorf("priorityFor(%d) = %q, want %q", score, got, want)
        }
    }
}

// A finding in a test file must score below the same finding in production
// code. This is the whole point of ADR 0003.
func TestScoreDiscountsTestFiles(t *testing.T) {
    prod := Finding{Rule: "lostcancel"}
    test := Finding{Rule: "lostcancel", IsTest: true}

    if score(test) >= score(prod) {
        t.Errorf("test file scored %d, production scored %d; test should be lower",
            score(test), score(prod))
    }
}

// Fingerprints must survive edits above the finding, or every unrelated change
// makes old findings look new.
func TestFingerprintIgnoresLineNumber(t *testing.T) {
    a := New("staticcheck", "SA2001", "CONC.MISUSE", "error", "a.go", 10, 2, "empty critical section")
    b := New("staticcheck", "SA2001", "CONC.MISUSE", "error", "a.go", 99, 5, "empty critical section")

    if a.Fingerprint != b.Fingerprint {
        t.Errorf("fingerprint changed with line number: %s vs %s", a.Fingerprint, b.Fingerprint)
    }
}

func TestFingerprintDiffersByFile(t *testing.T) {
    a := New("staticcheck", "SA2001", "CONC.MISUSE", "error", "a.go", 10, 2, "msg")
    b := New("staticcheck", "SA2001", "CONC.MISUSE", "error", "b.go", 10, 2, "msg")

    if a.Fingerprint == b.Fingerprint {
        t.Error("findings in different files share a fingerprint")
    }
}

func TestDedupe(t *testing.T) {
    f := New("go vet", "lostcancel", "CTX.PROPAGATION", "medium", "a.go", 7, 1, "lost cancel")

    if got := len(Dedupe([]Finding{f, f, f})); got != 1 {
        t.Errorf("Dedupe kept %d copies, want 1", got)
    }
}

func TestMatchNolint(t *testing.T) {
    f := Finding{Tool: "staticcheck", Rule: "SA2001"}

    cases := []struct {
        line       string
        suppressed bool
    }{
        {"p.mtx.RUnlock() //nolint:staticcheck // SA2001: intentional", true},
        {"x := 1 //nolint", true},
        {"x := 1 //nolint:all", true},
        {"x := 1 //nolint:gosec", false},
        {"x := 1", false},
    }

    for _, c := range cases {
        got := matchNolint(c.line, f) != ""
        if got != c.suppressed {
            t.Errorf("matchNolint(%q) suppressed=%v, want %v", c.line, got, c.suppressed)
        }
    }
}

// go vet findings are suppressed by "govet", not "vet" alone in every config,
// and never by an unrelated linter name.
func TestMatchNolintToolNames(t *testing.T) {
    vet := Finding{Tool: "go vet", Rule: "lostcancel"}

    if matchNolint("x := 1 //nolint:govet", vet) == "" {
        t.Error("govet should suppress a go vet finding")
    }
    if matchNolint("x := 1 //nolint:staticcheck", vet) != "" {
        t.Error("staticcheck should not suppress a go vet finding")
    }
}

func TestRulesMatch(t *testing.T) {
    if !rulesMatch("SA2001", "SA2001") {
        t.Error("exact rule should match")
    }
    if !rulesMatch("SA1019,SA2001", "SA2001") {
        t.Error("rule in a list should match")
    }
    if rulesMatch("SA1019", "SA2001") {
        t.Error("unrelated rule should not match")
    }
}