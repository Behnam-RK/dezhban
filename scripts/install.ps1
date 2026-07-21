#Requires -Version 5.1
# Installs dezhban from a GitHub release on Windows.
#
#   irm https://raw.githubusercontent.com/Behnam-RK/dezhban/main/scripts/install.ps1 | iex
#   $env:VERSION = "0.2.0"; irm .../install.ps1 | iex    # pin an exact version
#
# Must run from an elevated (Administrator) PowerShell: it installs to Program
# Files, writes to the machine PATH, and registers a Windows service.
#
# Checksum-verified against SHA256SUMS; deliberately NOT ed25519-signature
# checked — see scripts/install.sh's matching comment: the stronger guarantee
# lives in `dezhban upgrade` (Go, real crypto/ed25519), which is also the only
# place it matters here, since Windows gets no self-update path at all
# (`dezhban upgrade` is notify-only on Windows — see docs/upgrade.md). No code
# signing either: an EV certificate stopped bypassing SmartScreen in 2024, so
# it buys reputation over time, not a warning-free first run, regardless of
# whether this script exists.

$ErrorActionPreference = "Stop"

$Repo = "Behnam-RK/dezhban"
$GH = "https://github.com/$Repo"

function Die($msg) { Write-Host "error: $msg" -ForegroundColor Red; exit 1 }
function Note($msg) { Write-Host "==> $msg" }

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
	Die "run this from an elevated (Administrator) PowerShell"
}

# --- 1. detect platform -------------------------------------------------------
# Only windows-amd64 is a real release asset (build:all's target matrix has no
# windows/arm64) — Windows on Arm gets a clear error, not a silent wrong-arch
# download.
if ($env:PROCESSOR_ARCHITECTURE -ne "AMD64") {
	Die "unsupported architecture '$($env:PROCESSOR_ARCHITECTURE)' — only windows-amd64 is published (no windows/arm64 build yet)"
}
$asset = "dezhban-windows-amd64.exe"
Note "platform: windows/amd64"

# --- 2. resolve version --------------------------------------------------------
# VERSION pins an exact tag. Otherwise ask the GitHub API for the latest
# release — PowerShell's Invoke-RestMethod parses the JSON natively, which is
# more robust here than fighting Invoke-WebRequest's redirect/WebException
# handling across Windows PowerShell 5.1 vs. PowerShell 7 (install.sh instead
# follows the /releases/latest redirect, the more natural trick in bash). Same
# guarantee either way: GitHub's "latest" already excludes the --prerelease
# (rc) builds the release workflow tags.
if ($env:VERSION) {
	$version = $env:VERSION -replace '^v', ''
	Note "version: $version (pinned via VERSION=)"
} else {
	$release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
	if (-not $release.tag_name) { Die "could not resolve the latest release from the GitHub API — pass VERSION=X.Y.Z to skip this lookup" }
	$version = $release.tag_name -replace '^v', ''
	Note "version: $version (latest)"
}
$tag = "v$version"

$tmp = Join-Path $env:TEMP "dezhban-install-$([guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $tmp | Out-Null

try {
	# --- 3. download + verify --------------------------------------------------
	Note "downloading $asset $tag"
	$assetPath = Join-Path $tmp $asset
	Invoke-WebRequest -Uri "$GH/releases/download/$tag/$asset" -OutFile $assetPath -UseBasicParsing
	$sumsPath = Join-Path $tmp "SHA256SUMS"
	Invoke-WebRequest -Uri "$GH/releases/download/$tag/SHA256SUMS" -OutFile $sumsPath -UseBasicParsing

	Note "verifying checksum"
	$sumsLine = Select-String -Path $sumsPath -Pattern " $([regex]::Escape($asset))$" | Select-Object -First 1
	if (-not $sumsLine) { Die "no checksum entry for $asset in SHA256SUMS" }
	$expected = ($sumsLine.Line -split '\s+')[0].ToLower()
	$actual = (Get-FileHash -Path $assetPath -Algorithm SHA256).Hash.ToLower()
	if ($expected -ne $actual) {
		Die "checksum mismatch for $asset (expected $expected, got $actual) — aborting install. This may mean a bad mirror or a tampered download; do not retry blindly."
	}

	# --- 4. install -------------------------------------------------------------
	# Idempotent upgrade: stop if running, replace, restart only if it WAS
	# running. A fresh install never touches this — wasRunning stays false, so
	# enforcement is never armed here, matching the .pkg postinstall's invariant.
	$installDir = Join-Path $env:ProgramFiles "dezhban"
	New-Item -ItemType Directory -Force -Path $installDir | Out-Null
	$binPath = Join-Path $installDir "dezhban.exe"

	$wasRunning = $false
	if (Test-Path $binPath) {
		try {
			$status = & $binPath status --json 2>$null | ConvertFrom-Json -ErrorAction Stop
			if ($status.service -like "*running*") {
				$wasRunning = $true
				Note "existing installation is running — stopping for the upgrade"
				& $binPath --no-sudo stop | Out-Null
			}
		} catch {
			# No prior install, or status --json failed to parse — treat as fresh.
		}
	}

	Note "installing to $binPath"
	Copy-Item -Path $assetPath -Destination $binPath -Force

	$machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
	if ($machinePath -notlike "*$installDir*") {
		[Environment]::SetEnvironmentVariable("Path", "$machinePath;$installDir", "Machine")
		Note "added $installDir to the system PATH (open a new shell to pick it up)"
	}

	$configDir = Join-Path $env:ProgramData "dezhban"
	New-Item -ItemType Directory -Force -Path $configDir | Out-Null
	$configPath = Join-Path $configDir "dezhban.json"

	Note "registering the service (not starting it — see 'next steps' below)"
	& $binPath --no-sudo install --config $configPath
	if ($LASTEXITCODE -ne 0) {
		Die "could not register the service; the CLI is installed at $binPath — retry with 'dezhban install' from an elevated shell"
	}

	if ($wasRunning) {
		Note "restarting the service"
		& $binPath --no-sudo start
	}

	Write-Host ""
	Write-Host "dezhban $version installed."
	Write-Host ""
	Write-Host "next steps (open a NEW elevated PowerShell so PATH picks up dezhban.exe):"
	Write-Host "  dezhban setup   # configure: VPN, tunnel interfaces, blocked countries"
	Write-Host "  dezhban start   # arm the kill switch"
} finally {
	Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
