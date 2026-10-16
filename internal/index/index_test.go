package index

import (
	"testing"
	"time"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/Sievehouse/truebug/internal/graph"
)

// testdata/fixture is a tiny, self-contained module (its own go.mod, no
// external dependencies) built to exercise real interface dispatch: two
// concrete types implement Greeter, main calls say() with both -- one
// directly, one inside a goroutine. It is not a stand-in for a real
// repository; it is small enough that the expected call graph can be stated
// outright and checked exactly, which a real repository's graph is too
// large for.

func loadFixture(t *testing.T) *Result {
	t.Helper()
	res, err := Load("testdata/fixture", []string{"./..."}, 30*time.Second)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("Load reported package errors: %v", res.Errors)
	}
	if len(res.Packages) == 0 {
		t.Fatal("Load returned no packages")
	}
	return res
}

// findFunc locates a function in prog by its qualified name (the same form
// QualifiedName returns), so tests can name expectations the way a human
// would read them rather than by SSA internals.
func findFunc(prog *ssa.Program, qualifiedName string) *ssa.Function {
	for fn := range ssautil.AllFunctions(prog) {
		if fn != nil && QualifiedName(fn) == qualifiedName {
			return fn
		}
	}
	return nil
}

func TestLoadFixture(t *testing.T) {
	res := loadFixture(t)

	if findFunc(res.Program, "fixture.main") == nil {
		t.Fatal("fixture.main not found after Load")
	}
}

func TestBuildCallGraph(t *testing.T) {
	res := loadFixture(t)

	g := graph.New()
	result := BuildCallGraph(res.Program, g)

	if result.Nodes == 0 {
		t.Fatal("BuildCallGraph added no nodes")
	}
	if result.Edges == 0 {
		t.Fatal("BuildCallGraph added no edges")
	}

	nodes, edges := g.All()

	want := []string{
		"fixture.main",
		"fixture.say",
		"(fixture.English).Greet",
		"(fixture.French).Greet",
	}
	for _, name := range want {
		if nodeIDFor(nodes, name) == "" {
			t.Errorf("missing expected node %q in graph of %d nodes", name, len(nodes))
		}
	}

	// say() is called twice in the fixture (once directly, once inside a
	// goroutine). Both call sites must show up as incoming CALLS edges.
	sayID := nodeIDFor(nodes, "fixture.say")
	if sayID == "" {
		t.Fatal("could not resolve node for fixture.say")
	}
	if calls := g.In(sayID, graph.EdgeCalls); len(calls) == 0 {
		t.Error("fixture.say has no incoming CALLS edge")
	}

	for _, e := range edges {
		if e.Kind != graph.EdgeCalls {
			t.Errorf("unexpected edge kind %q; BuildCallGraph should only emit CALLS", e.Kind)
		}
		if e.Confidence <= 0 || e.Confidence > 1 {
			t.Errorf("edge %s -> %s has out-of-range confidence %v", e.From, e.To, e.Confidence)
		}
		if e.Producer != graph.FromCHA && e.Producer != graph.FromVTA {
			t.Errorf("edge %s -> %s has unexpected producer %q", e.From, e.To, e.Producer)
		}
	}

	t.Logf("fixture call graph: %d nodes, %d edges, VTA used: %v", result.Nodes, result.Edges, result.VTAUsed)
}

func TestMonikerStableAcrossIndependentLoads(t *testing.T) {
	// Loading the fixture twice and taking the moniker of "the function named
	// fixture.say" both times must agree: a moniker is a function of the
	// qualified name, not of load order or pointer identity, since two
	// separate processes indexing the same commit must produce the same ID.
	res1 := loadFixture(t)
	res2 := loadFixture(t)

	fn1 := findFunc(res1.Program, "fixture.say")
	fn2 := findFunc(res2.Program, "fixture.say")
	if fn1 == nil || fn2 == nil {
		t.Fatal("could not find fixture.say in one of the two loads")
	}

	if Moniker(fn1) != Moniker(fn2) {
		t.Errorf("Moniker not stable across independent loads: %s vs %s", Moniker(fn1), Moniker(fn2))
	}
}

func TestMonikerDistinguishesReceivers(t *testing.T) {
	res := loadFixture(t)

	english := findFunc(res.Program, "(fixture.English).Greet")
	french := findFunc(res.Program, "(fixture.French).Greet")
	if english == nil || french == nil {
		t.Fatal("could not find both Greet methods")
	}
	if Moniker(english) == Moniker(french) {
		t.Error("two different receivers' methods produced the same moniker")
	}
}

func nodeIDFor(nodes []graph.Node, name string) string {
	for _, n := range nodes {
		if n.Name == name {
			return n.ID
		}
	}
	return ""
}
