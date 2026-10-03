// Command truebug-batch clones a list of Go repositories, scans each one, and
// aggregates the results into a corpus.
//
// It produces four things:
//
//	<out>/repos/<owner>__<name>.json   the full report per repository
//	<out>/summary.csv                  cost and volume per repository
//	<out>/rule_frequency.csv           how often each rule fires, and how often
//	                                   a human suppressed it
//	<out>/negatives.jsonl              every mined hard negative, merged
//
// rule_frequency.csv is the point of the exercise. The rule priors in the
// findings package are currently guesses; a rule that maintainers suppress 60%
// of the time has earned a lower prior, and this file is the evidence.
//
// Usage:
//
//	truebug-batch -repos bench/repos.txt -work ./work -out ./bench/results
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sievehouse/truebug/internal/analyzers"
	"github.com/Sievehouse/truebug/internal/findings"
)

type result struct {
	URL       string
	Name      string
	Dir       string
	Report    findings.Report
	Negatives []findings.HardNegative
	Err       error
	Seconds   float64
}

func main() {
	reposFlag := flag.String("repos", "bench/repos.txt", "file with one git URL per line; # starts a comment")
	workFlag := flag.String("work", "work", "directory to clone repositories into")
	outFlag := flag.String("out", filepath.Join("bench", "results"), "directory to write the corpus into")
	jobsFlag := flag.Int("jobs", 3, "repositories to analyze in parallel")
	keepFlag := flag.Bool("keep", true, "reuse an existing clone instead of re-cloning")
	flag.Parse()

	urls, err := readRepoList(*reposFlag)
	if err != nil {
		fatal(err)
	}
	if len(urls) == 0 {
		fatal(fmt.Errorf("no repositories listed in %s", *reposFlag))
	}

	for _, d := range []string{*workFlag, *outFlag, filepath.Join(*outFlag, "repos")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fatal(err)
		}
	}

	fmt.Fprintf(os.Stderr, "scanning %d repositories with %d workers\n\n", len(urls), *jobsFlag)
	started := time.Now()

	results := runAll(urls, *workFlag, *jobsFlag, *keepFlag)

	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })

	writeReports(results, filepath.Join(*outFlag, "repos"))
	writeSummary(results, filepath.Join(*outFlag, "summary.csv"))
	writeRuleFrequency(results, filepath.Join(*outFlag, "rule_frequency.csv"))
	writeNegatives(results, filepath.Join(*outFlag, "negatives.jsonl"))

	printTotals(results, *outFlag, time.Since(started))
}

// ---------------------------------------------------------------------------
// running
// ---------------------------------------------------------------------------

func runAll(urls []string, workDir string, jobs int, keep bool) []result {
	if jobs < 1 {
		jobs = 1
	}

	queue := make(chan string)
	var mu sync.Mutex
	var out []result
	var wg sync.WaitGroup

	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for url := range queue {
				r := scanOne(url, workDir, keep)

				mu.Lock()
				out = append(out, r)
				done := len(out)
				mu.Unlock()

				if r.Err != nil {
					fmt.Fprintf(os.Stderr, "[%3d] %-34s FAILED: %v\n", done, r.Name, r.Err)
					continue
				}
				fmt.Fprintf(os.Stderr, "[%3d] %-34s %4d reported  %4d suppressed  %3d negatives  (%.0fs)\n",
					done, r.Name, len(r.Report.Findings), len(r.Report.Suppressed), len(r.Negatives), r.Seconds)
			}
		}()
	}

	for _, u := range urls {
		queue <- u
	}
	close(queue)
	wg.Wait()

	return out
}

func scanOne(url, workDir string, keep bool) result {
	start := time.Now()
	name := repoName(url)
	dir := filepath.Join(workDir, name)

	r := result{URL: url, Name: name, Dir: dir}

	if err := ensureClone(url, dir, keep); err != nil {
		r.Err = err
		r.Seconds = round1(time.Since(start).Seconds())
		return r
	}

	// Repositories whose module lives in a subdirectory are skipped rather than
	// guessed at; mislabeling the module root produces garbage findings.
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		r.Err = fmt.Errorf("no go.mod at repository root")
		r.Seconds = round1(time.Since(start).Seconds())
		return r
	}

	r.Report = analyzers.Scan(dir)
	r.Negatives = findings.MineNegatives(dir, r.Report)
	r.Seconds = round1(time.Since(start).Seconds())
	return r
}

func ensureClone(url, dir string, keep bool) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if keep {
			return nil
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}

	cmd := exec.Command("git", "clone", "--depth", "1", "--quiet", url, dir)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("clone: %s", strings.TrimSpace(firstLine(stderr.String())))
	}
	return nil
}

// ---------------------------------------------------------------------------
// outputs
// ---------------------------------------------------------------------------

func writeReports(results []result, dir string) {
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		blob, err := json.MarshalIndent(r.Report, "", "  ")
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dir, r.Name+".json"), blob, 0o644)
	}
}

func writeSummary(results []result, path string) {
	f, err := os.Create(path)
	if err != nil {
		fatal(err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	_ = w.Write([]string{
		"repo", "commit", "status", "raw", "reported", "suppressed_structural",
		"suppressed_by_comment", "negatives", "in_tests", "critical", "high",
		"medium", "low", "seconds",
	})

	for _, r := range results {
		if r.Err != nil {
			_ = w.Write([]string{r.Name, "", "error: " + r.Err.Error(),
				"", "", "", "", "", "", "", "", "", "", ftoa(r.Seconds)})
			continue
		}

		raw := 0
		for _, t := range r.Report.Tools {
			raw += t.Findings
		}

		structural, byComment := countSuppressions(r.Report)
		prio := map[string]int{}
		tests := 0
		for _, fd := range r.Report.Findings {
			prio[fd.Priority]++
			if fd.IsTest {
				tests++
			}
		}

		_ = w.Write([]string{
			r.Name, shortCommit(r.Report.Commit), "ok",
			itoa(raw), itoa(len(r.Report.Findings)),
			itoa(structural), itoa(byComment), itoa(len(r.Negatives)), itoa(tests),
			itoa(prio["critical"]), itoa(prio["high"]), itoa(prio["medium"]), itoa(prio["low"]),
			ftoa(r.Seconds),
		})
	}
}

// ruleStat accumulates evidence about one rule across the whole corpus.
type ruleStat struct {
	Tool     string
	Rule     string
	Category string

	// Reported is how often the rule fired and nobody dismissed it.
	Reported int
	// Suppressed counts only human suppressions. Vendored and generated code
	// is excluded: nobody judged those, they were filtered structurally, so
	// counting them would make every rule look equally doubted.
	Suppressed int

	Repos map[string]bool
}

func writeRuleFrequency(results []result, path string) {
	stats := map[string]*ruleStat{}

	get := func(fd findings.Finding, repo string) *ruleStat {
		key := fd.Tool + "|" + fd.Rule
		s, ok := stats[key]
		if !ok {
			s = &ruleStat{Tool: fd.Tool, Rule: fd.Rule, Category: fd.Category, Repos: map[string]bool{}}
			stats[key] = s
		}
		s.Repos[repo] = true
		return s
	}

	for _, r := range results {
		if r.Err != nil {
			continue
		}
		for _, fd := range r.Report.Findings {
			get(fd, r.Name).Reported++
		}
		for _, fd := range r.Report.Suppressed {
			if fd.SuppressReason == "vendored" || fd.SuppressReason == "generated" {
				continue
			}
			get(fd, r.Name).Suppressed++
		}
	}

	rows := make([]*ruleStat, 0, len(stats))
	for _, s := range stats {
		rows = append(rows, s)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Reported+a.Suppressed != b.Reported+b.Suppressed {
			return a.Reported+a.Suppressed > b.Reported+b.Suppressed
		}
		return a.Rule < b.Rule
	})

	f, err := os.Create(path)
	if err != nil {
		fatal(err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	_ = w.Write([]string{"tool", "rule", "category", "reported", "suppressed_by_human", "suppression_rate", "repos"})
	for _, s := range rows {
		total := s.Reported + s.Suppressed
		rate := 0.0
		if total > 0 {
			rate = float64(s.Suppressed) / float64(total)
		}
		_ = w.Write([]string{
			s.Tool, s.Rule, s.Category,
			itoa(s.Reported), itoa(s.Suppressed),
			strconv.FormatFloat(rate, 'f', 3, 64),
			itoa(len(s.Repos)),
		})
	}
}

func writeNegatives(results []result, path string) {
	f, err := os.Create(path)
	if err != nil {
		fatal(err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	defer w.Flush()

	for _, r := range results {
		for _, n := range r.Negatives {
			line, err := json.Marshal(n)
			if err != nil {
				continue
			}
			w.Write(line)
			w.WriteByte('\n')
		}
	}
}

func printTotals(results []result, outDir string, elapsed time.Duration) {
	bar := strings.Repeat("=", 62)

	var ok, failed, reported, suppressed, negatives int
	for _, r := range results {
		if r.Err != nil {
			failed++
			continue
		}
		ok++
		reported += len(r.Report.Findings)
		suppressed += len(r.Report.Suppressed)
		negatives += len(r.Negatives)
	}

	fmt.Fprintf(os.Stderr, "\n%s\n", bar)
	fmt.Fprintf(os.Stderr, "scanned %d repositories (%d failed) in %s\n", ok, failed, elapsed.Round(time.Second))
	fmt.Fprintf(os.Stderr, "  reported findings:  %d\n", reported)
	fmt.Fprintf(os.Stderr, "  suppressed:         %d\n", suppressed)
	fmt.Fprintf(os.Stderr, "  hard negatives:     %d\n", negatives)
	fmt.Fprintf(os.Stderr, "%s\n", bar)
	fmt.Fprintf(os.Stderr, "corpus written to %s\n", outDir)
	fmt.Fprintf(os.Stderr, "  look at rule_frequency.csv first: a high suppression_rate\n")
	fmt.Fprintf(os.Stderr, "  means maintainers routinely dismiss that rule.\n")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func readRepoList(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var urls []string
	seen := map[string]bool{}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		urls = append(urls, line)
	}
	return urls, sc.Err()
}

// repoName turns a clone URL into "owner__name", which is unique enough to be
// a filename and still readable.
func repoName(url string) string {
	u := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(url), "/"), ".git")
	parts := strings.Split(u, "/")
	if len(parts) >= 2 {
		return sanitize(parts[len(parts)-2]) + "__" + sanitize(parts[len(parts)-1])
	}
	return sanitize(u)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func countSuppressions(r findings.Report) (structural, byComment int) {
	for _, f := range r.Suppressed {
		if f.SuppressReason == "vendored" || f.SuppressReason == "generated" {
			structural++
		} else {
			byComment++
		}
	}
	return structural, byComment
}

func shortCommit(c string) string {
	if len(c) >= 12 {
		return c[:12]
	}
	return c
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "truebug-batch: %v\n", err)
	os.Exit(1)
}
