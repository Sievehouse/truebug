package index

// Call graph construction: VTA where it succeeds, CHA where it doesn't.
//
// CHA (Class Hierarchy Analysis) is purely syntactic: at an interface call
// site, it proposes every concrete type in the program that implements the
// interface, whether or not that type's value could actually reach the call.
// VTA (Variable Type Analysis) tracks what concrete types actually flow into
// each interface value, and rejects the rest. That precision is why plan.md
// calls for it: a goroutine-root walk or a lockset propagation built on a
// CHA-only graph inherits every one of its false edges, and on a large
// interface (io.Writer, say) that is not a small error.
//
// VTA needs an initial call graph to refine and is documented to panic on
// code it does not model well (some reflection and generic-instantiation
// patterns). CHA never panics -- it is a bounded syntactic pass -- so it is
// always computed first and used as the fallback. Every edge VTA contributes
// is cross-checked against the CHA graph: an edge VTA keeps that CHA also
// proposed is the high-confidence case; an edge VTA adds that CHA did not
// propose should not happen for a sound pair of analyses, so it is kept but
// scored lower rather than silently trusted.

import (
	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/Sievehouse/truebug/internal/graph"
)

// Confidence scores for CALLS edges, by how they were produced. These are
// placeholders, not measured values -- plan.md's Phase 0 calibration applies
// to static-analysis rule priors, not yet to call-graph confidence, which
// has no suppression signal to learn from. Treat them as an ordering (VTA
// confirmed > VTA alone > CHA), not as calibrated probabilities.
const (
	confidenceVTAConfirmed = 0.92 // VTA kept an edge CHA also proposed
	confidenceVTAOnly      = 0.60 // VTA added an edge CHA did not propose
	confidenceCHAOnly      = 0.75 // VTA was unavailable; CHA's overapproximation stands alone
)

// CallGraphResult is the outcome of building a call graph for one program.
type CallGraphResult struct {
	Graph   *callgraph.Graph // function-level graph, for callers that want to walk *ssa.Function directly
	VTAUsed bool             // false if VTA panicked and CHA was used alone
	Nodes   int              // symbol nodes added to the property graph
	Edges   int              // CALLS edges added to the property graph
}

// BuildCallGraph constructs a call graph for prog and writes it into g as
// CALLS edges between symbol nodes, keyed by Moniker. It returns the
// function-level callgraph.Graph too, since Phase 2's goroutine-root and
// lockset analyses want to traverse *ssa.Function directly rather than
// re-resolve a moniker at every hop.
func BuildCallGraph(prog *ssa.Program, g *graph.Graph) CallGraphResult {
	funcs := ssautil.AllFunctions(prog)

	chaGraph := cha.CallGraph(prog)
	chaEdges := edgeSet(chaGraph)

	cg, vtaUsed := refineWithVTA(funcs, chaGraph)

	before, _ := g.Size()
	var edgeCount int

	for fn, node := range cg.Nodes {
		if fn == nil {
			continue // the synthetic root
		}
		callerID := Moniker(fn)
		ensureFuncNode(g, fn, callerID)

		for _, e := range node.Out {
			if e.Callee == nil || e.Callee.Func == nil {
				continue
			}
			calleeID := Moniker(e.Callee.Func)
			ensureFuncNode(g, e.Callee.Func, calleeID)

			producer, confidence := graph.FromCHA, confidenceCHAOnly
			if vtaUsed {
				if chaEdges[edgeKey{fn, e.Callee.Func}] {
					producer, confidence = graph.FromVTA, confidenceVTAConfirmed
				} else {
					producer, confidence = graph.FromVTA, confidenceVTAOnly
				}
			}

			g.AddEdge(graph.Edge{
				Kind:       graph.EdgeCalls,
				From:       callerID,
				To:         calleeID,
				Producer:   producer,
				Confidence: confidence,
			})
			edgeCount++
		}
	}

	after, _ := g.Size()
	return CallGraphResult{Graph: cg, VTAUsed: vtaUsed, Nodes: after - before, Edges: edgeCount}
}

type edgeKey struct {
	caller, callee *ssa.Function
}

// edgeSet flattens a callgraph.Graph into a lookup table, so checking
// whether VTA's graph agrees with CHA's is a map lookup per edge rather than
// a linear scan of CHA's edge list per VTA edge.
func edgeSet(cg *callgraph.Graph) map[edgeKey]bool {
	set := make(map[edgeKey]bool)
	for fn, node := range cg.Nodes {
		if fn == nil {
			continue
		}
		for _, e := range node.Out {
			if e.Callee == nil || e.Callee.Func == nil {
				continue
			}
			set[edgeKey{fn, e.Callee.Func}] = true
		}
	}
	return set
}

// refineWithVTA runs VTA over the CHA graph and recovers from the panics VTA
// is documented to raise on code it does not model. CHA's graph is always a
// safe fallback: it never panics, it only overapproximates.
func refineWithVTA(funcs map[*ssa.Function]bool, chaGraph *callgraph.Graph) (cg *callgraph.Graph, ok bool) {
	defer func() {
		if recover() != nil {
			cg, ok = chaGraph, false
		}
	}()
	return vta.CallGraph(funcs, chaGraph), true
}

// ensureFuncNode adds a symbol node for fn if one is not already present.
// Position comes from the SSA function itself, which tracks the declaration
// site even for methods and closures that go/packages never names directly.
func ensureFuncNode(g *graph.Graph, fn *ssa.Function, id string) {
	if _, ok := g.Node(id); ok {
		return
	}

	n := graph.Node{
		ID:   id,
		Kind: graph.KindSymbol,
		Name: QualifiedName(fn),
	}
	if fn.Prog != nil {
		if pos := fn.Prog.Fset.Position(fn.Pos()); pos.IsValid() {
			n.File = pos.Filename
			n.Line = pos.Line
		}
	}
	g.AddNode(n)
}
