// Package snapshot identifies what was analyzed, so a finding can be traced
// back to the exact (repository, commit, build configuration) that produced
// it, and so re-indexing an unchanged snapshot can be skipped.
//
// plan.md's Phase 1 asks for two things here: a snapshot model with
// content-addressed caching, and "graph tables in SQLite/Postgres; in-memory
// adjacency per job." This package provides the identity and a
// content-addressed cache keyed by that identity, serialized as JSON rather
// than a SQL schema -- the in-memory graph.Graph already satisfies the
// per-job adjacency requirement, and a real database is deferred until a
// corpus run shows the JSON cache is the bottleneck, not assumed in advance.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Sievehouse/truebug/internal/build"
	"github.com/Sievehouse/truebug/internal/graph"
)

// ID identifies one (repository, commit, build configuration) triple.
//
// Config is part of the identity, not metadata alongside it: internal/build
// already documents that the same commit under a different Go version can
// have a different meaning (loop-variable semantics, timer GC), so a
// snapshot keyed on commit alone would silently conflate two different sets
// of facts under one cache entry.
type ID struct {
	Repo   string      `json:"repo"`   // canonical remote, e.g. https://github.com/org/repo
	Commit string      `json:"commit"` // full commit SHA
	Config build.Config `json:"config"`
}

// Key returns the content-addressed cache key for this identity: a short
// hex digest of the fields that change its meaning. Two snapshots with the
// same key are, by construction, asking the same question of the same code.
func (id ID) Key() string {
	// Fields are joined with a separator byte that cannot appear in any of
	// them (commit SHAs and Go versions are both restricted alphabets, and a
	// repo URL cannot contain a newline), so this cannot collide the way
	// naive concatenation could (e.g. repo="a", commit="bc" colliding with
	// repo="ab", commit="c").
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%s\n%v\n",
		id.Repo, id.Commit, id.Config.Module, id.Config.GoVersion,
		id.Config.GOOS+"/"+id.Config.GOARCH, id.Config.Tags)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// cached is what Store writes and Load reads back: the snapshot's identity
// alongside its graph, so a cache hit can be verified against the identity
// that produced it rather than trusted on the key alone.
type cached struct {
	ID    ID          `json:"id"`
	Nodes []graph.Node `json:"nodes"`
	Edges []graph.Edge `json:"edges"`
}

// Store is a content-addressed cache of indexed snapshots on local disk.
//
// It is intentionally the simplest thing that is still correct: one JSON
// file per snapshot, named by its key, under a directory. A corpus scan
// across the repositories in bench/repos.txt produces at most a few hundred
// snapshots; a database earns its complexity past that, not before it.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir, creating it if necessary.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("snapshot store: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(key string) string {
	return filepath.Join(s.dir, key+".json")
}

// Has reports whether id is already cached, without reading the graph back.
// A corpus runner calls this before indexing to decide whether to skip a
// repository entirely -- the whole point of content-addressed caching being
// that an unchanged snapshot costs a stat call, not a rebuild.
func (s *Store) Has(id ID) bool {
	_, err := os.Stat(s.path(id.Key()))
	return err == nil
}

// Store writes g under id's content-addressed key, replacing any existing
// entry. The write is atomic: g is written to a temp file in the same
// directory and renamed into place, so a crash mid-write cannot leave a
// truncated cache entry that a later Load would silently accept as valid
// JSON up to the point it was cut off, or reject as corrupt in a way that
// blocks every future run until someone notices and deletes it by hand.
func (s *Store) Store(id ID, g *graph.Graph) error {
	nodes, edges := g.All()
	blob, err := json.Marshal(cached{ID: id, Nodes: nodes, Edges: edges})
	if err != nil {
		return fmt.Errorf("snapshot store: marshal: %w", err)
	}

	final := s.path(id.Key())
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return fmt.Errorf("snapshot store: write: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("snapshot store: rename: %w", err)
	}
	return nil
}

// Load reads back the graph cached under id's key. The second return value
// is false if nothing is cached for this exact identity -- not an error,
// since a cache miss is the expected outcome the first time any snapshot is
// seen.
func (s *Store) Load(id ID) (*graph.Graph, bool, error) {
	blob, err := os.ReadFile(s.path(id.Key()))
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("snapshot load: %w", err)
	}

	var c cached
	if err := json.Unmarshal(blob, &c); err != nil {
		return nil, false, fmt.Errorf("snapshot load: unmarshal: %w", err)
	}

	g := graph.New()
	for _, n := range c.Nodes {
		g.AddNode(n)
	}
	for _, e := range c.Edges {
		g.AddEdge(e)
	}
	return g, true, nil
}
