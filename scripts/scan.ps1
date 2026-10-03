# scan.ps1 — build and scan one repository.
#
#   .\scripts\scan.ps1 -Repo ..\prometheus

param(
    [Parameter(Mandatory = $true)][string]$Repo,
    [string]$MinPriority = "low"
)

$ErrorActionPreference = "Stop"

go build -o truebug.exe .\cmd\cli
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$name = Split-Path -Leaf (Resolve-Path $Repo)

.\truebug.exe -repo $Repo `
    -out "$name-findings.json" `
    -negatives "$name-negatives.jsonl" `
    -min-priority $MinPriority