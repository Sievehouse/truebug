// Package scip holds the precise-symbol layer: definitions, references and
// implementations, with identifiers stable across commits.
//
// Stable monikers are what make everything else possible. Without them a
// finding cannot be tracked between two commits, a dataset example cannot
// point at a symbol, and the graph cannot be diffed.
//
// No indexer is wired up yet. These are the types the rest of the system will
// be written against; scip-go comes first, other languages later.
package scip

// Moniker is a stable, cross-commit symbol identifier, in the style of
// "scip-go gomod github.com/org/repo v1.4.0 pkg/Type#Method().".
type Moniker string

type Symbol struct {
    Moniker   Moniker `json:"moniker"`
    Package   string  `json:"package"`
    Name      string  `json:"name"`
    Kind      string  `json:"kind"`
    Signature string  `json:"signature"`
    File      string  `json:"file"`
    LineStart int     `json:"line_start"`
    LineEnd   int     `json:"line_end"`
    Exported  bool    `json:"exported"`
    Doc       string  `json:"doc,omitempty"`
}

// Occurrence is one mention of a symbol.
type Occurrence struct {
    Moniker    Moniker `json:"moniker"`
    File       string  `json:"file"`
    Line       int     `json:"line"`
    Column     int     `json:"column"`
    Definition bool    `json:"definition"`
}

type Index struct {
    Symbols     []Symbol     `json:"symbols"`
    Occurrences []Occurrence `json:"occurrences"`
}

// Indexer produces a SCIP index for one repository.
//
// Implementations shell out to the language's indexer binary. Like every
// analyzer, a missing indexer must be reported, never treated as an empty
// index.
type Indexer interface {
    Name() string
    Languages() []string
    Index(repoDir string) (*Index, error)
}

// Definition returns the symbol a moniker names.
func (ix *Index) Definition(m Moniker) (Symbol, bool) {
    for _, s := range ix.Symbols {
        if s.Moniker == m {
            return s, true
        }
    }
    return Symbol{}, false
}

// References returns every non-definition occurrence of a moniker.
func (ix *Index) References(m Moniker) []Occurrence {
    var out []Occurrence
    for _, o := range ix.Occurrences {
        if o.Moniker == m && !o.Definition {
            out = append(out, o)
        }
    }
    return out
}
