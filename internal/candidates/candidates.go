// Package candidates turns raw analyzer output into the shortlist a human or a
// model actually looks at.
//
// Two jobs: group findings that share a root cause, and enforce a report
// budget. The budget is not politeness. At realistic bug rates an unbounded
// scanner buries the one real finding under a hundred plausible ones, and the
// maintainer stops reading at the third false positive.
package candidates

import (
    "sort"

    "github.com/Sievehouse/truebug/internal/findings"
)

// Cluster is a set of findings that probably share one cause.
type Cluster struct {
    Key      string             `json:"key"`
    Rule     string             `json:"rule"`
    Category string             `json:"category"`
    File     string             `json:"file"`
    TopScore int                `json:"top_score"`
    Findings []findings.Finding `json:"findings"`
}

// Group clusters by rule and file. Twenty instances of one rule in one file is
// one conversation with the maintainer, not twenty.
func Group(in []findings.Finding) []Cluster {
    byKey := map[string]*Cluster{}

    for _, f := range in {
        key := f.Rule + "|" + f.File
        c, ok := byKey[key]
        if !ok {
            c = &Cluster{Key: key, Rule: f.Rule, Category: f.Category, File: f.File}
            byKey[key] = c
        }
        c.Findings = append(c.Findings, f)
        if f.Score > c.TopScore {
            c.TopScore = f.Score
        }
    }

    out := make([]Cluster, 0, len(byKey))
    for _, c := range byKey {
        out = append(out, *c)
    }

    sort.Slice(out, func(i, j int) bool {
        if out[i].TopScore != out[j].TopScore {
            return out[i].TopScore > out[j].TopScore
        }
        return out[i].Key < out[j].Key
    })
    return out
}

// Budget keeps the highest-scoring findings within an overall cap and a
// per-file cap.
//
// The per-file cap is the important one: without it, a single noisy file fills
// the entire report and everything else is invisible.
func Budget(in []findings.Finding, total, perFile int) []findings.Finding {
    sorted := make([]findings.Finding, len(in))
    copy(sorted, in)
    sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })

    seenInFile := map[string]int{}
    out := make([]findings.Finding, 0, total)

    for _, f := range sorted {
        if total > 0 && len(out) >= total {
            break
        }
        if perFile > 0 && seenInFile[f.File] >= perFile {
            continue
        }
        seenInFile[f.File]++
        out = append(out, f)
    }
    return out
}

// Diff returns findings present in head but not in base, by fingerprint.
//
// This is what a pull request comment should be built from. Fingerprints
// exclude line numbers, so a finding does not look new just because something
// above it moved.
func Diff(base, head []findings.Finding) (added, fixed []findings.Finding) {
    inBase := make(map[string]bool, len(base))
    for _, f := range base {
        inBase[f.Fingerprint] = true
    }

    inHead := make(map[string]bool, len(head))
    for _, f := range head {
        inHead[f.Fingerprint] = true
        if !inBase[f.Fingerprint] {
            added = append(added, f)
        }
    }

    for _, f := range base {
        if !inHead[f.Fingerprint] {
            fixed = append(fixed, f)
        }
    }
    return added, fixed
}
