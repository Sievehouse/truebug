# ADR 0002: Reuse existing analyzers; build only what is missing

- **Date:** 2026-10-03
- **Status:** accepted

## Context

`go vet` and `staticcheck` already encode years of work on Go correctness
checks. Reimplementing them would consume the entire project budget and produce
something worse.

What they do not do: cross-artifact reasoning (code against Kubernetes
manifests), lockset analysis over SSA with concurrency roots, or any form of
verification that a reported finding is real.

## Decision

Shell out to existing analyzers and normalise their output into one finding
type. Build only the fact graph, the custom concurrency and code-to-config
analyzers, the verification ladder, the dataset and the benchmark.

Tools are invoked as separate binaries, never linked, which also keeps
GPL-licensed tools (golangci-lint, hadolint) usable without licence contagion.

## Consequences

Startup cost per scan and dependence on each tool's output format. A tool that
is not installed must be reported as not-run, because zero findings and a
missing binary look identical otherwise.

## Alternatives considered

Writing analyzers from scratch on `go/analysis` — rejected for v1 except where
no existing linter covers the check.