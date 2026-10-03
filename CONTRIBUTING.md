# Contributing

## Building

```
go build ./...
go test ./...
go vet ./...
```

External tools the scanner shells out to:

```
go install honnef.co/go/tools/cmd/staticcheck@latest
```

## Running

```
go run ./cmd/cli   -repo /path/to/go/repo -out findings.json
go run ./cmd/batch -repos bench/repos.txt -jobs 1
```

## Adding an analyzer

Implement `analyzers.Analyzer` in `internal/analyzers`:

1. Check the binary exists with `exec.LookPath` and return an error if not.
   A missing tool must be visible in the report, never silently zero findings.
2. Parse its output into `findings.Finding` via `findings.New`.
3. Map its rules onto the internal taxonomy (`CONC.*`, `CTX.*`, `ERR.*`,
   `RES.*`, `API.MISUSE`, ...). Do not invent a category to make a rule fit.
4. Add rule priors to `rulePriors` in `internal/findings/finding.go`, and say
   in the commit message what the number is based on.
5. If the tool has its own suppression comment syntax, teach
   `internal/findings/suppress.go` about it.

## Rules of the house

- **Never report a finding a maintainer already dismissed.** Suppression
  comments are human judgement; honour them and mine them.
- **A guessed number is labelled as a guess.** The rule priors are currently
  guesses. When measured data replaces one, say so in the commit.
- **Findings are candidates until verified.** Nothing claims to be a bug
  without evidence attached.
- Analysis executes untrusted code. Anything that builds or runs a scanned
  repository belongs in a sandbox.