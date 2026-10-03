# ADR 0001: Record architecture decisions

- **Date:** 2026-10-03
- **Status:** accepted

## Context

This project makes a long series of choices whose reasons stop being obvious
within weeks: which analyzers to trust, what a rule prior is based on, why a
category exists. Six months in, the code shows what was decided and nothing
shows why.

## Decision

Every decision that would be expensive to reverse gets a numbered ADR in
`docs/adr/`. Short, dated, with the evidence that motivated it.

## Consequences

Small ongoing cost. In exchange, a decision can be revisited on its merits
instead of being treated as load-bearing because nobody remembers it.