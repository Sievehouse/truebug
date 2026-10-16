// Package index loads a Go module's packages, builds their SSA
// representation, and constructs a call graph -- the facts that goroutine,
// lockset, and context-propagation analysis in Phase 2 build on.
//
// This is the first stage in the pipeline that executes code. go/packages
// shells out to `go list`, which runs the module's own build system: a
// replace directive pointing at a local path, a build tag, or a generated
// file already on disk is honored exactly as it would be for a real build.
// go:generate directives are not run here. Treat this stage as untrusted
// until the sandboxing plan.md calls for in Phase 1 exists; nothing in this
// package provides it yet.
package index

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// loadMode is the packages.Load mode this project needs: enough to
// type-check and build SSA, nothing more. Dropping NeedDeps loads faster but
// leaves every cross-package call a dead end, since ssautil.Packages can
// only build bodies for packages whose type information it was given.
const loadMode = packages.NeedName |
	packages.NeedFiles |
	packages.NeedCompiledGoFiles |
	packages.NeedImports |
	packages.NeedDeps |
	packages.NeedTypes |
	packages.NeedSyntax |
	packages.NeedTypesInfo |
	packages.NeedModule

// Result is one loaded module: its SSA program, the packages that built
// successfully, and every error the loader reported along the way.
//
// Errors are kept rather than surfaced as a single failure because a module
// with one broken package (a missing build tag, an unresolved internal
// import) still has real facts in its other packages, and a corpus scan over
// dozens of repositories cannot afford to discard a whole repository for one
// package's mistake.
type Result struct {
	Program  *ssa.Program
	Packages []*ssa.Package
	Pkgs     []*packages.Package
	Errors   []packages.Error
}

// Load loads every package under patterns (typically "./...") rooted at dir,
// builds their SSA form, and returns the result.
//
// timeout bounds the whole load, not each package individually: go/packages
// asks `go list` to describe the entire package graph in one process, and
// there is no per-package boundary inside that call to time out at. A single
// pathological package -- cgo with runaway macro expansion, a build-tag
// combinatorial explosion -- stalls the whole call, and this deadline is
// what recovers the caller from that. A true per-package limit would need to
// run `go list` once per package in its own subprocess, which trades this
// function's single-process simplicity for N process start-ups; left for
// when a real corpus run shows the coarse timeout isn't enough.
func Load(dir string, patterns []string, timeout time.Duration) (*Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cfg := &packages.Config{
		Context: ctx,
		Dir:     dir,
		Mode:    loadMode,
		Tests:   true, // _test.go code is part of the call graph, not noise to skip
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("load packages: %w (timeout %s)", ctx.Err(), timeout)
	}

	var loadErrs []packages.Error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		loadErrs = append(loadErrs, p.Errors...)
	})

	// ssautil.Packages builds SSA for the initial packages and creates stub
	// (body-less) packages for their dependencies. NeedDeps above is what
	// makes those stubs carry real type information instead of opaque
	// placeholders.
	prog, ssaPkgs := ssautil.Packages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	kept := ssaPkgs[:0]
	for _, p := range ssaPkgs {
		if p != nil {
			kept = append(kept, p)
		}
	}

	return &Result{Program: prog, Packages: kept, Pkgs: pkgs, Errors: loadErrs}, nil
}
