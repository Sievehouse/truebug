// Package graph holds the typed property graph that joins code, config, tests
// and findings for one snapshot.
//
// Two design choices worth stating.
//
// First, every edge carries its producer and a confidence. A call edge from
// VTA and one guessed by a model are not the same claim, and a high-severity
// finding may never rest on a model-inferred edge alone.
//
// Second, there is no graph database. A Kubernetes-size repository yields on
// the order of 10^6 nodes and 10^7 edges, which fits in memory, and every
// query is a bounded walk from a seed.
package graph

import "sort"

type NodeKind string

const (
    KindRepository      NodeKind = "repository"
    KindFile            NodeKind = "file"
    KindPackage         NodeKind = "package"
    KindSymbol          NodeKind = "symbol"
    KindField           NodeKind = "field"
    KindCallSite        NodeKind = "callsite"
    KindConcurrencyRoot NodeKind = "concurrency_root"
    KindSyncObject      NodeKind = "sync_object"
    KindTest            NodeKind = "test"
    KindConfigKey       NodeKind = "config_key"
    KindInfraObject     NodeKind = "infra_object"
    KindFinding         NodeKind = "finding"
)

type EdgeKind string

const (
    EdgeContains         EdgeKind = "CONTAINS"
    EdgeDefines          EdgeKind = "DEFINES"
    EdgeReferences       EdgeKind = "REFERENCES"
    EdgeCalls            EdgeKind = "CALLS"
    EdgeImplements       EdgeKind = "IMPLEMENTS"
    EdgeReads            EdgeKind = "READS"
    EdgeWrites           EdgeKind = "WRITES"
    EdgeSpawns           EdgeKind = "SPAWNS"
    EdgeGuards           EdgeKind = "GUARDS"
    EdgeSends            EdgeKind = "SENDS"
    EdgeReceives         EdgeKind = "RECEIVES"
    EdgeTests            EdgeKind = "TESTS"
    EdgeReadsConfig      EdgeKind = "READS_CONFIG"
    EdgeNeedsPermission  EdgeKind = "NEEDS_PERMISSION"
    EdgeGrantsPermission EdgeKind = "GRANTS_PERMISSION"
    EdgeEvidences        EdgeKind = "EVIDENCES"
)

// Producer records what asserted an edge. Provenance is what lets a finding be
// audited, and what stops a model's guess from being treated as a fact.
type Producer string

const (
    FromSCIP       Producer = "scip"
    FromSSA        Producer = "ssa"
    FromVTA        Producer = "vta"
    FromCHA        Producer = "cha"
    FromTreeSitter Producer = "treesitter"
    FromHelmRender Producer = "helm"
    FromModel      Producer = "model"
)

type Node struct {
    ID    string            `json:"id"`
    Kind  NodeKind          `json:"kind"`
    Name  string            `json:"name"`
    File  string            `json:"file,omitempty"`
    Line  int               `json:"line,omitempty"`
    Attrs map[string]string `json:"attrs,omitempty"`
}

type Edge struct {
    Kind       EdgeKind `json:"kind"`
    From       string   `json:"from"`
    To         string   `json:"to"`
    Producer   Producer `json:"producer"`
    Confidence float64  `json:"confidence"`

    // Locks is the lockset held at a READS or WRITES access. It is the single
    // most important field for race detection: two accesses to the same field
    // with no lock in common, from two concurrency roots, is the candidate.
    Locks []string `json:"locks,omitempty"`
}

// Graph is an in-memory property graph for one snapshot.
type Graph struct {
    nodes map[string]Node
    out   map[string][]Edge
    in    map[string][]Edge
}

func New() *Graph {
    return &Graph{
        nodes: map[string]Node{},
        out:   map[string][]Edge{},
        in:    map[string][]Edge{},
    }
}

func (g *Graph) AddNode(n Node) { g.nodes[n.ID] = n }

func (g *Graph) AddEdge(e Edge) {
    g.out[e.From] = append(g.out[e.From], e)
    g.in[e.To] = append(g.in[e.To], e)
}

func (g *Graph) Node(id string) (Node, bool) {
    n, ok := g.nodes[id]
    return n, ok
}

func (g *Graph) Size() (nodes, edges int) {
    for _, es := range g.out {
        edges += len(es)
    }
    return len(g.nodes), edges
}

// Out and In return edges leaving or entering a node, optionally filtered by
// kind.
func (g *Graph) Out(id string, kinds ...EdgeKind) []Edge { return filterEdges(g.out[id], kinds) }
func (g *Graph) In(id string, kinds ...EdgeKind) []Edge  { return filterEdges(g.in[id], kinds) }

func filterEdges(es []Edge, kinds []EdgeKind) []Edge {
    if len(kinds) == 0 {
        return es
    }
    want := make(map[EdgeKind]bool, len(kinds))
    for _, k := range kinds {
        want[k] = true
    }

    out := make([]Edge, 0, len(es))
    for _, e := range es {
        if want[e.Kind] {
            out = append(out, e)
        }
    }
    return out
}

// Reachable walks forward from a node along one edge kind, bounded by hops and
// a confidence floor.
//
// Both bounds matter. Unbounded walks on a large repository return most of the
// call graph, and low-confidence edges compound: five hops of 0.6-confidence
// edges is not a claim worth putting in front of a maintainer.
func (g *Graph) Reachable(from string, kind EdgeKind, maxHops int, minConfidence float64) []string {
    seen := map[string]bool{from: true}
    frontier := []string{from}

    for hop := 0; hop < maxHops && len(frontier) > 0; hop++ {
        var next []string
        for _, id := range frontier {
            for _, e := range g.out[id] {
                if e.Kind != kind || e.Confidence < minConfidence || seen[e.To] {
                    continue
                }
                seen[e.To] = true
                next = append(next, e.To)
            }
        }
        frontier = next
    }

    out := make([]string, 0, len(seen))
    for id := range seen {
        if id != from {
            out = append(out, id)
        }
    }
    sort.Strings(out)
    return out
}

// Accessors returns every read and write of a field, each carrying the lockset
// held at that access.
func (g *Graph) Accessors(fieldID string) []Edge {
    return g.In(fieldID, EdgeReads, EdgeWrites)
}

// CommonLocks returns the locks held at every one of the given accesses. An
// empty result on a field touched from two concurrency roots is the race
// candidate; a non-empty one means the field is consistently guarded.
func CommonLocks(accesses []Edge) []string {
    if len(accesses) == 0 {
        return nil
    }

    counts := map[string]int{}
    for _, a := range accesses {
        for _, lock := range uniqueStrings(a.Locks) {
            counts[lock]++
        }
    }

    var out []string
    for lock, n := range counts {
        if n == len(accesses) {
            out = append(out, lock)
        }
    }
    sort.Strings(out)
    return out
}

// HasWrite reports whether any access writes. Concurrent reads are not a race.
func HasWrite(accesses []Edge) bool {
    for _, a := range accesses {
        if a.Kind == EdgeWrites {
            return true
        }
    }
    return false
}

func uniqueStrings(in []string) []string {
    seen := make(map[string]bool, len(in))
    out := make([]string, 0, len(in))
    for _, s := range in {
        if seen[s] {
            continue
        }
        seen[s] = true
        out = append(out, s)
    }
    return out
}
