// Package retrieval assembles the context a model sees.
//
// The same assembler must build training examples and production prompts. If
// they differ even slightly, the model meets a context shape at serving time
// that it never saw during training, and the measured accuracy stops
// predicting the real one.
package retrieval

import (
    "sort"
    "strings"
)

// Unit is one piece of context: a function body, a signature, a facts block.
type Unit struct {
    ID       string `json:"id"`
    Kind     string `json:"kind"`
    Priority int    `json:"priority"`
    Tokens   int    `json:"tokens"`
    Text     string `json:"text"`
}

const (
    KindFacts     = "facts"
    KindFunction  = "function"
    KindSignature = "signature"
    KindConfig    = "config"
    KindTest      = "test"
    KindExemplar  = "exemplar"
)

// Priorities. The deterministic facts block always goes first: it is short,
// it is true, and it is the part the model is least able to reconstruct from
// source alone.
const (
    PriorityFacts     = 100
    PrioritySeed      = 90
    PriorityCaller    = 70
    PriorityCallee    = 60
    PriorityType      = 50
    PriorityConfig    = 40
    PriorityTest      = 30
    PriorityExemplar  = 20
    PrioritySignature = 10
)

// Packed is the result of fitting units into a token budget.
type Packed struct {
    Units   []Unit `json:"units"`
    Tokens  int    `json:"tokens"`
    Dropped int    `json:"dropped"`
}

// EstimateTokens approximates a tokenizer at roughly four characters per
// token. Good enough for budgeting; replace it with the real tokenizer before
// any number from it goes in a paper.
func EstimateTokens(s string) int {
    if len(s) == 0 {
        return 0
    }
    if n := len(s) / 4; n > 0 {
        return n
    }
    return 1
}

// Pack fills a token budget by priority, highest first.
//
// It keeps filling after the first unit that does not fit, so a single huge
// function body does not block every small high-value unit behind it.
//
// Bigger budgets are not automatically better: prefill cost grows with every
// token and small models degrade in the presence of distractors. The
// accuracy-versus-budget curve is something to measure, not assume.
func Pack(units []Unit, budget int) Packed {
    sorted := make([]Unit, len(units))
    copy(sorted, units)

    for i := range sorted {
        if sorted[i].Tokens == 0 {
            sorted[i].Tokens = EstimateTokens(sorted[i].Text)
        }
    }

    sort.SliceStable(sorted, func(i, j int) bool {
        return sorted[i].Priority > sorted[j].Priority
    })

    var out Packed
    for _, u := range sorted {
        if out.Tokens+u.Tokens > budget {
            out.Dropped++
            continue
        }
        out.Units = append(out.Units, u)
        out.Tokens += u.Tokens
    }
    return out
}

// Render joins packed units into the prompt body. Every unit is labelled so
// the model can cite file and line, and so a citation can be checked against
// the graph afterwards.
func (p Packed) Render() string {
    var b strings.Builder
    for i, u := range p.Units {
        if i > 0 {
            b.WriteString("\n\n")
        }
        b.WriteString("### ")
        b.WriteString(u.ID)
        b.WriteString("\n")
        b.WriteString(u.Text)
    }
    return b.String()
}

// Recall is the share of the symbols a fix actually touched that made it into
// the packed context.
//
// This metric bounds everything downstream: a bug whose root cause never
// reaches the prompt cannot be found, no matter how good the model is. Measure
// it before blaming the model for a miss.
func Recall(packed Packed, required []string) float64 {
    if len(required) == 0 {
        return 1
    }

    present := make(map[string]bool, len(packed.Units))
    for _, u := range packed.Units {
        present[u.ID] = true
    }

    hit := 0
    for _, id := range required {
        if present[id] {
            hit++
        }
    }
    return float64(hit) / float64(len(required))
}
