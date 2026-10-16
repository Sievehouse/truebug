# ADR 0005: Call graph via VTA, falling back to CHA

- **Date:** 2026-10-16
- **Status:** accepted

## Context

Phase 2's goroutine-root walks and lockset propagation both need to answer
"what can call this function." CHA (Class Hierarchy Analysis) answers that
syntactically: at an interface call site, it proposes every concrete type in
the program that implements the interface, whether or not a value of that
type can actually reach the call. On an interface with many implementations
(`io.Writer`, `error`, anything in a plugin-style registry) that overapproximation
is large, and every later analysis built on top of it inherits the noise.

VTA (Variable Type Analysis) tracks what concrete types actually flow into
each interface value and rejects the rest. `golang.org/x/tools/go/callgraph/vta`
implements it, but it needs an initial call graph to refine, and it is
documented to panic on code it does not model well: some reflection and
generic-instantiation patterns.

## Decision

Always compute CHA first (`internal/index/callgraph.go`). Attempt to refine
it with VTA, recovering from a panic rather than losing the whole package --
CHA is a safe, always-available fallback, so a VTA failure degrades
precision, not availability. Every edge is recorded with a `Producer`
(`cha` or `vta`) and a `Confidence`:

| Edge came from | Confidence |
| --- | --- |
| VTA, and CHA also proposed it | 0.92 |
| VTA, but CHA did not propose it | 0.60 (should not happen for a sound pair of analyses; kept, not trusted) |
| CHA alone (VTA panicked) | 0.75 |

These are placeholders, not measured values -- there is no suppression
signal for call-graph edges the way ADR 0003's rule priors have one. They
establish an ordering, not a calibrated probability.

Symbol identity comes from `internal/index/moniker.go`: a SHA-256 of the
function's SSA-canonical qualified name (`fn.RelString(nil)`), which is
stable across reformatting and position changes but changes if the function
is renamed -- the same property plan.md asks SCIP-style monikers for.

Indexed snapshots are cached by content-addressed key
(`internal/snapshot`): `(repo, commit, build config)` hashed together, since
`internal/build.Config` already establishes that the same commit under a
different Go version is a different set of facts. The cache is one JSON file
per snapshot, not a SQL schema -- plan.md's "graph tables in SQLite/Postgres"
item is deferred until a real corpus run shows the JSON cache is the
bottleneck.

## Consequences

Verified against `internal/index/testdata/fixture`, a module with real
interface dispatch, and against a real unmodified clone of `gorilla/mux`
(8,642 nodes, 2,862 edges, VTA confirmed running rather than falling back).
Confidence numbers above are still guesses; the honest fix mirrors ADR
0003's -- once Phase 3's fix-corpus exists, check whether findings reached
through VTA-only or low-confidence edges correlate with real bugs, and
recalibrate from that, not from intuition.

Not done: per-package load timeouts (the timeout in `internal/index.Load`
bounds the whole `go list` call, since go/packages has no per-package
boundary to interrupt independently), memory caps (would need OS-level
enforcement -- cgroups or rlimit -- around the indexing process, not
implemented), and the sandboxing Phase 1 calls for generally. `go/packages`
shells out to the target module's own build tooling; nothing here isolates
that yet.
