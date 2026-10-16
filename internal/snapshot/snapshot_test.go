package snapshot

import (
	"reflect"
	"testing"

	"github.com/Sievehouse/truebug/internal/build"
	"github.com/Sievehouse/truebug/internal/graph"
)

func testID() ID {
	return ID{
		Repo:   "https://github.com/example/widget",
		Commit: "abc123",
		Config: build.Config{Module: "example.com/widget", GoVersion: "1.26", GOOS: "linux", GOARCH: "amd64"},
	}
}

func TestKeyStableAndDistinct(t *testing.T) {
	id := testID()
	if id.Key() != testID().Key() {
		t.Error("Key() not stable for two equal IDs")
	}

	other := testID()
	other.Commit = "def456"
	if id.Key() == other.Key() {
		t.Error("Key() collided for two different commits")
	}

	tagged := testID()
	tagged.Config.Tags = []string{"integration"}
	if id.Key() == tagged.Key() {
		t.Error("Key() ignored build tags; a tagged and untagged build of the same commit must differ")
	}
}

func TestStoreLoadRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	id := testID()

	if store.Has(id) {
		t.Fatal("Has reported a hit before anything was stored")
	}
	if _, ok, err := store.Load(id); err != nil || ok {
		t.Fatalf("Load on empty store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	g := graph.New()
	g.AddNode(graph.Node{ID: "fn:1", Kind: graph.KindSymbol, Name: "pkg.Foo"})
	g.AddNode(graph.Node{ID: "fn:2", Kind: graph.KindSymbol, Name: "pkg.Bar"})
	g.AddEdge(graph.Edge{Kind: graph.EdgeCalls, From: "fn:1", To: "fn:2", Producer: graph.FromVTA, Confidence: 0.92})

	if err := store.Store(id, g); err != nil {
		t.Fatalf("Store: %v", err)
	}

	if !store.Has(id) {
		t.Fatal("Has reported a miss immediately after Store")
	}

	got, ok, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !ok {
		t.Fatal("Load reported a miss for a key that was just stored")
	}

	wantNodes, wantEdges := g.All()
	gotNodes, gotEdges := got.All()

	if len(gotNodes) != len(wantNodes) {
		t.Fatalf("round-tripped %d nodes, want %d", len(gotNodes), len(wantNodes))
	}
	for i := range wantNodes {
		if !reflect.DeepEqual(gotNodes[i], wantNodes[i]) {
			t.Errorf("node %d: got %+v, want %+v", i, gotNodes[i], wantNodes[i])
		}
	}

	if len(gotEdges) != len(wantEdges) {
		t.Fatalf("round-tripped %d edges, want %d", len(gotEdges), len(wantEdges))
	}
	for i := range wantEdges {
		if !reflect.DeepEqual(gotEdges[i], wantEdges[i]) {
			t.Errorf("edge %d: got %+v, want %+v", i, gotEdges[i], wantEdges[i])
		}
	}
}

func TestStoreOverwrites(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id := testID()

	g1 := graph.New()
	g1.AddNode(graph.Node{ID: "fn:1", Kind: graph.KindSymbol, Name: "old"})
	if err := store.Store(id, g1); err != nil {
		t.Fatalf("Store (first): %v", err)
	}

	g2 := graph.New()
	g2.AddNode(graph.Node{ID: "fn:1", Kind: graph.KindSymbol, Name: "new"})
	if err := store.Store(id, g2); err != nil {
		t.Fatalf("Store (second): %v", err)
	}

	got, ok, err := store.Load(id)
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	nodes, _ := got.All()
	if len(nodes) != 1 || nodes[0].Name != "new" {
		t.Errorf("second Store did not overwrite the first: got %+v", nodes)
	}
}
