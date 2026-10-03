# ADR 0003: Compute our own priority, ignore tool-reported severity

- **Date:** 2026-10-03
- **Status:** accepted

## Context

Severity labels are not comparable across tools, and no tool knows anything
about the repository it is scanning.

Concrete case: staticcheck reported two SA1029 findings in spf13/cobra as
`severity: high`. Both were in `_test.go` files, where a context key collision
is close to harmless. A maintainer would dismiss both.

## Decision

Keep the tool's label in `Severity` for the record, and compute our own `Score`
and `Priority` from a per-rule prior multiplied by context discounts: 0.30 for
test files, 0.10 for generated code, 0.05 for vendored code.

## Consequences

The ranking is only as good as the priors, and **the priors are currently
guesses**. The batch runner's `rule_frequency.csv` measures how often humans
suppress each rule; those rates replace the guesses in Phase 0.

Until that happens, the numbers in `rulePriors` are opinion, and the code says
so.