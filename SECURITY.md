# Security policy

## Reporting a vulnerability in truebug

Open a private security advisory on this repository. Do not open a public
issue.

## Findings this tool produces about other projects

This scanner analyses third-party repositories. Anything it surfaces that looks
like a real, exploitable vulnerability in someone else's project is handled as
a coordinated disclosure, not a public issue:

- Report privately to the project's own security contact, following its
  `SECURITY.md`.
- For NVIDIA projects, use NVIDIA PSIRT.
- For Kubernetes and related projects, use the Kubernetes Security Response
  Committee.
- Never file a public GitHub issue describing an unfixed vulnerability.
- Never post automated vulnerability reports to a project that has not opted in.

Unsolicited bot reports get tools banned, and public disclosure of an unfixed
flaw causes harm. Both are worse than the finding going unreported for a week.

## Running the scanner safely

Indexing is not passive reading: Go package loading can invoke cgo, `go
generate` runs arbitrary commands, and dependency installs run lifecycle
scripts. Scan untrusted repositories inside a sandbox with no network access
beyond a pinned module proxy, and no credentials mounted.