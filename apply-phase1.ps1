# apply-phase1.ps1 - applies Phase 1 (indexing and the graph) to the truebug
# repo: real go/packages + go/ssa loading, a VTA-with-CHA-fallback call
# graph, SCIP-style symbol monikers, and a content-addressed snapshot cache.
#
# Run from the truebug folder, with phase1-indexing.zip in your Downloads
# folder:
#   powershell -ExecutionPolicy Bypass -File .\apply-phase1.ps1

$ErrorActionPreference = "Stop"
$dl = Join-Path $env:USERPROFILE "Downloads"
$zip = Join-Path $dl "phase1-indexing.zip"

if (-not (Test-Path "go.mod")) {
    Write-Host "no go.mod here - run this from inside the truebug folder" -ForegroundColor Red
    exit 1
}
if (-not (Test-Path $zip)) {
    Write-Host "missing $zip - download phase1-indexing.zip first" -ForegroundColor Red
    exit 1
}

# --- 1. Unpack --------------------------------------------------------------
Write-Host "unpacking phase1-indexing.zip ..."
Expand-Archive -Path $zip -DestinationPath . -Force
Write-Host "  done"

# --- 2. Resolve the new dependency (needs real internet: go.sum checksums
#        come from sum.golang.org, which this environment could not reach
#        when this was built and tested) -------------------------------------
Write-Host "`ngo mod tidy (fetches golang.org/x/tools and its own dependencies) ..."
go mod tidy
if ($LASTEXITCODE -ne 0) { Write-Host "go mod tidy failed" -ForegroundColor Red; exit 1 }

# --- 3. Build, vet, test ------------------------------------------------------
Write-Host "`ngo build ..."
go build ./...
if ($LASTEXITCODE -ne 0) { Write-Host "build failed - not committing" -ForegroundColor Red; exit 1 }

Write-Host "go vet ..."
go vet ./...
if ($LASTEXITCODE -ne 0) { Write-Host "vet failed - not committing" -ForegroundColor Red; exit 1 }

Write-Host "go test ./... (includes the new index + snapshot packages) ..."
go test ./...
if ($LASTEXITCODE -ne 0) { Write-Host "tests failed - not committing" -ForegroundColor Red; exit 1 }

# --- 4. Prove it on this repo, not just the test fixture ---------------------
Write-Host "`nbuilding truebug-index and running it on this repo, as a live check ..."
go build -o truebug-index.exe ./cmd/index
if ($LASTEXITCODE -eq 0) {
    .\truebug-index.exe -repo . -cache .truebug-cache
}

# --- 5. Keep the snapshot cache out of git -----------------------------------
$ignore = @(".truebug-cache/")
$current = if (Test-Path .gitignore) { Get-Content .gitignore } else { @() }
foreach ($p in $ignore) {
    if ($current -notcontains $p) { Add-Content .gitignore $p }
}

# --- 6. Commit and push -------------------------------------------------------
git add -A
git commit -m "Phase 1: real indexing - go/packages+SSA loading, VTA/CHA call graph, symbol monikers, snapshot cache" -m "internal/index: go/packages + go/ssa loading (internal/index/load.go), call graph via VTA with CHA fallback and per-edge confidence (callgraph.go), SCIP-style symbol monikers (moniker.go). internal/snapshot: content-addressed cache keyed on (repo, commit, build config). cmd/index: CLI that indexes a repo or serves it from cache. internal/graph: added All() for serialization. See docs/adr/0005. Verified against a real clone of gorilla/mux (8,642 nodes, 2,862 edges, VTA confirmed running, not falling back to CHA) in addition to the new test fixture. Not done: per-package timeouts (go/packages has no per-package boundary - the timeout bounds the whole load), memory caps, incremental re-index, sandboxing - all still open in plan.md."
git push origin main

Write-Host "`ndone." -ForegroundColor Green
