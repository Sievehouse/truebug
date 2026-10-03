// Package verify holds the evidence ladder: the difference between a tool
// saying something and a tool having shown it.
//
// This is the part of the project that is not a linter. A finding at
// LevelHypothesis is a guess, however fluently it is phrased. A finding at
// LevelReproduced comes with a test that fails under the race detector. Only
// the second one has earned a maintainer's time.
package verify

import (
    "fmt"
    "sort"

    "github.com/Sievehouse/truebug/internal/findings"
)

// Level is the strength of the strongest evidence behind a claim.
type Level int

const (
    // LevelHypothesis: a model asserted it. Worth nothing on its own.
    LevelHypothesis Level = iota

    // LevelStaticFact: an analyzer proved something concrete, e.g. two
    // accesses to a field with no lock in common.
    LevelStaticFact

    // LevelCorroborated: independent signals agree. Self-consistency from one
    // model does not count; repeated sampling produces correlated error.
    LevelCorroborated

    // LevelReproduced: the behaviour was observed by running the code, under
    // -race, goleak, or a generated test.
    LevelReproduced

    // LevelPatchVerified: a fix compiles, tests pass, and the finding is gone.
    LevelPatchVerified
)

func (l Level) String() string {
    switch l {
    case LevelStaticFact:
        return "static_fact"
    case LevelCorroborated:
        return "corroborated"
    case LevelReproduced:
        return "reproduced"
    case LevelPatchVerified:
        return "patch_verified"
    default:
        return "hypothesis"
    }
}

type Verdict string

const (
    Confirmed Verdict = "CONFIRMED"
    Likely    Verdict = "LIKELY"
    Possible  Verdict = "POSSIBLE"
    Rejected  Verdict = "REJECTED"
)

// Evidence is one piece of support for a claim.
type Evidence struct {
    Level    Level  `json:"level"`
    Kind     string `json:"kind"`
    Detail   string `json:"detail"`
    File     string `json:"file,omitempty"`
    Line     int    `json:"line,omitempty"`
    Producer string `json:"producer"`
}

// Claim is a finding plus everything known about whether it is real.
type Claim struct {
    Finding  findings.Finding `json:"finding"`
    Evidence []Evidence       `json:"evidence"`
    Verdict  Verdict          `json:"verdict"`

    // Confidence is only meaningful once calibrated against adjudicated data.
    // Until then it is a number, not a probability, and must not be shown to a
    // user as a percentage.
    Confidence float64 `json:"confidence"`
    Calibrated bool    `json:"calibrated"`
}

// Level returns the strongest rung this claim has reached.
func (c Claim) Level() Level {
    best := LevelHypothesis
    for _, e := range c.Evidence {
        if e.Level > best {
            best = e.Level
        }
    }
    return best
}

// VerdictFor maps an evidence level onto a verdict. Note that no level below
// reproduction can yield CONFIRMED: static analysis alone does not confirm.
func VerdictFor(l Level) Verdict {
    switch l {
    case LevelPatchVerified, LevelReproduced:
        return Confirmed
    case LevelCorroborated:
        return Likely
    default:
        return Possible
    }
}

// Exists reports whether a cited location is real in the snapshot.
type Exists func(file string, line int) bool

// Grounded checks that every location a claim cites actually exists.
//
// This is the cheapest and most effective check against a model inventing a
// file or a line number. A claim that cites something absent is rejected
// outright, no matter how convincing its prose.
func Grounded(c Claim, exists Exists) (bool, []string) {
    var bad []string
    for _, e := range c.Evidence {
        if e.File == "" {
            continue
        }
        if !exists(e.File, e.Line) {
            bad = append(bad, fmt.Sprintf("%s:%d", e.File, e.Line))
        }
    }
    sort.Strings(bad)
    return len(bad) == 0, bad
}

// Publishable decides whether a claim may be shown to a maintainer.
//
// Uncalibrated confidence never passes. "94% confident" means something only
// if 94% of such claims turn out real on held-out adjudicated data, and until
// that set exists the number is decoration.
func Publishable(c Claim, minLevel Level, minConfidence float64) bool {
    if c.Verdict == Rejected {
        return false
    }
    if !c.Calibrated {
        return false
    }
    return c.Level() >= minLevel && c.Confidence >= minConfidence
}
