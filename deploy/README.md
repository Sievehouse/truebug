# Deployment

Nothing here is built yet. Intended shape, from the design doc:

- **CLI** — single static binary, CPU only, no GPU required. The static tier
  must work standalone; the model tier is an upgrade, not a dependency.
- **GitHub Action** — the lightest integration. Runs the scanner, uploads SARIF
  to code scanning. Build this before the App.
- **GitHub App** — webhook, queue, sandboxed workers, differential analysis of
  the merge base against the head.
- **Helm chart** — self-hosted install, including the inference server.

## Sandboxing is not optional

Workers build and run untrusted repositories. gVisor or Firecracker, no network
beyond a pinned module proxy, no credentials or app keys inside the sandbox,
and fork pull requests treated as hostile.