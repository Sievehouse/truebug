// Command truebug-index builds a call graph for a Go repository and reports
// its size, or serves it straight from the content-addressed cache if the
// same (repository, commit, build config) was already indexed.
//
// It is the first command in the pipeline that runs code rather than only
// reading source: go/packages shells out to `go list`, which executes the
// target module's build system. Point it at code you trust, the same as you
// would `go build` it.
//
// Usage:
//
//	truebug-index -repo /path/to/go/repo
//	truebug-index -repo /path/to/go/repo -cache .truebug-cache
//	truebug-index -repo /path/to/go/repo -timeout 10m
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sievehouse/truebug/internal/analyzers"
	"github.com/Sievehouse/truebug/internal/build"
	"github.com/Sievehouse/truebug/internal/graph"
	"github.com/Sievehouse/truebug/internal/index"
	"github.com/Sievehouse/truebug/internal/snapshot"
)

func main() {
	repoFlag := flag.String("repo", ".", "path to the Go module root to index")
	cacheFlag := flag.String("cache", ".truebug-cache", "content-addressed snapshot cache directory")
	timeoutFlag := flag.Duration("timeout", 5*time.Minute, "timeout for the whole package load")
	remoteFlag := flag.String("remote", "", "repository URL recorded in the snapshot identity (default: git remote \"origin\")")
	forceFlag := flag.Bool("force", false, "re-index even if a cache entry already exists")
	flag.Parse()

	repoDir, err := filepath.Abs(*repoFlag)
	if err != nil {
		fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		fatal(fmt.Errorf("no go.mod in %s: point -repo at a Go module root", repoDir))
	}

	cfg, err := build.Detect(repoDir)
	if err != nil {
		fatal(fmt.Errorf("detect build config: %w", err))
	}

	commit := analyzers.GitCommit(repoDir)
	remote := *remoteFlag
	if remote == "" {
		remote = gitRemote(repoDir)
	}
	id := snapshot.ID{Repo: remote, Commit: commit, Config: cfg}

	store, err := snapshot.NewStore(*cacheFlag)
	if err != nil {
		fatal(err)
	}

	start := time.Now()

	if !*forceFlag {
		if g, ok, err := store.Load(id); err != nil {
			fatal(fmt.Errorf("read cache: %w", err))
		} else if ok {
			nodes, edges := g.All()
			fmt.Printf("cache hit for %s\n", id.Key())
			fmt.Printf("  repo:   %s\n", remote)
			fmt.Printf("  commit: %s\n", shortCommit(commit))
			fmt.Printf("  graph:  %d nodes, %d edges\n", len(nodes), len(edges))
			fmt.Printf("  (use -force to re-index anyway)\n")
			return
		}
	}

	res, err := index.Load(repoDir, []string{"./..."}, *timeoutFlag)
	if err != nil {
		fatal(fmt.Errorf("load packages: %w", err))
	}
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "package error: %s\n", e)
	}

	g := graph.New()
	result := index.BuildCallGraph(res.Program, g)

	if err := store.Store(id, g); err != nil {
		fatal(fmt.Errorf("write cache: %w", err))
	}

	fmt.Printf("indexed %s\n", remote)
	fmt.Printf("  commit:          %s\n", shortCommit(commit))
	fmt.Printf("  go version:      %s\n", valueOr(cfg.GoVersion, "(unknown)"))
	fmt.Printf("  packages loaded: %d (load errors: %d)\n", len(res.Packages), len(res.Errors))
	fmt.Printf("  call graph:      %d nodes, %d edges (VTA used: %v)\n", result.Nodes, result.Edges, result.VTAUsed)
	fmt.Printf("  elapsed:         %s\n", time.Since(start).Round(10*time.Millisecond))
	fmt.Printf("  cached under:    %s (%s)\n", id.Key(), *cacheFlag)
}

// gitRemote returns the "origin" remote's URL, or "" if the directory is not
// a git checkout or has no such remote. It is best-effort: an indexed
// directory without a usable remote still gets a valid snapshot identity, it
// is simply keyed on an empty repo string plus its commit, which still
// distinguishes it from every other commit of every other empty-remote repo.
func gitRemote(repoDir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	if c == "" {
		return "(unknown)"
	}
	return c
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "truebug-index: %v\n", err)
	os.Exit(1)
}
