# ADR 0004: Honour suppression comments, and mine them as hard negatives

- **Date:** 2026-10-03
- **Status:** accepted

## Context

Scanning prometheus/prometheus surfaced `SA2001 empty critical section` in
`tsdb/index/postings.go` as the top-ranked critical finding. The line reads:

```go
p.mtx.RUnlock() //nolint:staticcheck // SA2001: this is an intentionally empty critical section.
```

The authors knew. The construct is deliberate — it lets waiting readers acquire
the lock. Reporting it is a false positive of the most damaging kind, because
it tells the maintainer the tool did not read their code.

The cause: the `staticcheck` binary ignores `//nolint` directives, which are a
golangci-lint convention. Invoking it directly surfaces findings the authors
explicitly dismissed.

## Decision

Two things, from the same observation.

1. Honour `//nolint`, `//nolint:<linter>`, `//lint:ignore <rule>` and
   `//lint:file-ignore <rule>`, mapping tool names to the names users actually
   write. Suppressed findings move to `Suppressed` with the reason recorded;
   they are not deleted.

2. Mine them. A suppression is a human saying *this rule fired here and is
   wrong here*, usually with a written reason. That is a labelled hard negative,
   the exact class of example bug-fix mining never produces, since bug-fix
   mining only ever yields positives.

## Consequences

Some suppressions hide real bugs, so these are medium-noise labels and must not
be treated as ground truth without sampling.

Vendored and generated code is excluded from the mined set: nobody judged that
code, it was filtered structurally, and counting it would make every rule look
equally doubted.