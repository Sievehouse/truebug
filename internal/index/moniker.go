package index

// Symbol monikers: stable identifiers for functions that survive across
// commits, in the spirit of SCIP (plan.md: "Symbol monikers (SCIP-style) so
// findings diff across commits").
//
// A moniker is built from the function's fully qualified name -- package
// import path, receiver, and declared name -- never from source position, so
// a finding tagged against a moniker still resolves after someone reformats
// the file or an unrelated function is added above it. Move the function to
// a different file in the same package, and the moniker is unchanged; rename
// it, and the moniker changes too, which is correct: a renamed function is a
// different symbol for diffing purposes, same as it would be to a human
// reviewer comparing two versions of a file.
//
// This is not full SCIP. It has no cross-repository package-manager
// coordinates, no distinction between a definition and a reference, and two
// instantiations of the same generic function with different type arguments
// currently collide. It is the minimum stable-identity scheme Phase 1 needs;
// the rest is deferred.

import (
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/tools/go/ssa"
)

// Moniker returns a stable identifier for fn.
func Moniker(fn *ssa.Function) string {
	return "fn:" + hashString(QualifiedName(fn))
}

// QualifiedName returns the human-readable name a moniker is derived from.
// It is kept alongside the moniker on every graph node (graph.Node.Name) so
// a reader never has to reverse a hash to know what a node is.
//
// RelString(nil) is ssa's own canonical qualified form, e.g.
// "(*github.com/foo/bar.Server).Handle" for a method or
// "github.com/foo/bar.helper" for a package-level function. It already folds
// in package path, receiver and name, which is what makes it suitable as a
// moniker's input.
func QualifiedName(fn *ssa.Function) string {
	return fn.RelString(nil)
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}
