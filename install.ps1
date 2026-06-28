# xcx install script (PowerShell) — downloads the latest release binary for
# Windows and installs it to a directory on PATH.
#
# Usage (one-liner, runs in current process):
#   iwr -useb https://raw.githubusercontent.com/bulanzade/xcx/main/install.ps1 | iex
#
# Force a reinstall even if already latest:
#   Save the file and run:  ./install.ps1 -Force
#
# Override the install dir:
#   ./install.ps1 -InstallDir "C:\Tools\xcx"
#
# Uninstall the binary installed by this script:
#   ./install.ps1 -Uninstall

[CmdletBinding()]
param(
    # Default install dir: a per-user Programs folder (no admin needed).
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA "Programs\xcx"),
    [switch]$Force,
    [switch]$Uninstall
)

$ErrorActionPreference = "Stop"

# --- detect arch ------------------------------------------------------------
# Windows releases are always xcx-windows-<arch>.zip
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    { $_ -in 'AMD64','x64' }  { 'amd64'; break }
    { $_ -in 'ARM64' }        { 'arm64'; break }
    default { Write-Error "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE"; exit 1 }
}
$Archive = "xcx-windows-$arch.zip"
$Bin     = "xcx.exe"
$Dest    = Join-Path $InstallDir $Bin

if ($Uninstall) {
    $Backup = "$Dest.old"
    if (Test-Path $Dest) {
        Remove-Item $Dest -Force
        Write-Host "Removed $Dest"
    } else {
        Write-Host "xcx is not installed at $Dest"
    }
    if (Test-Path $Backup) {
        Remove-Item $Backup -Force
        Write-Host "Removed $Backup"
    }
    Write-Host "Configuration was left untouched: $env:APPDATA\xcx"
    exit 0
}

$Url = "https://github.com/bulanzade/xcx/releases/latest/download/$Archive"
$Api = "https://api.github.com/repos/bulanzade/xcx/releases/latest"

# --- latest version (tolerate API failure -> forced upgrade) ----------------
function Get-LatestTag {
    try {
        $resp = Invoke-RestMethod -Uri $Api -Headers @{ 'User-Agent' = 'xcx-installer' } -ErrorAction Stop
        return $resp.tag_name
    } catch {
        return $null
    }
}
$LatestTag = Get-LatestTag

# --- installed version + up-to-date check -----------------------------------
$Current = $null
if (Test-Path $Dest) {
    try { $Current = (& $Dest -version 2>$null).Trim() } catch { $Current = $null }
}

if (-not $Force -and $Current -and $LatestTag) {
    if ($Current -eq $LatestTag) {
        Write-Host "xcx $Current is already the latest ($LatestTag). Nothing to do."
        Write-Host "Re-run with -Force to reinstall anyway."
        exit 0
    }
    Write-Host "Upgrading xcx $Current -> $LatestTag"
}
elseif (-not $Force -and $Current -and -not $LatestTag) {
    Write-Host "Could not determine latest release tag; proceeding with reinstall."
}
else {
    $label = if ($LatestTag) { $LatestTag } else { 'latest' }
    Write-Host "Installing xcx $label"
}

# --- backup existing binary for rollback ------------------------------------
$Backup = $null
if (Test-Path $Dest) {
    $Backup = "$Dest.old"
    Copy-Item $Dest $Backup -Force
}

# --- download + extract -----------------------------------------------------
$Tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("xcx-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $Tmp -Force | Out-Null
try {
    Write-Host "Downloading $Url"
    $ZipPath = Join-Path $Tmp $Archive
    Invoke-WebRequest -Uri $Url -OutFile $ZipPath -UseBasicParsing

    Expand-Archive -Path $ZipPath -DestinationPath $Tmp -Force

    $Extracted = Join-Path $Tmp $Bin
    if (-not (Test-Path $Extracted)) {
        throw "$Bin not found in archive"
    }

    # --- install ------------------------------------------------------------
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    try {
        Move-Item $Extracted $Dest -Force
    } catch {
        if ($Backup) { Move-Item $Backup $Dest -Force }
        throw "install failed: $_"
    }

    # verify the new binary runs before declaring success; roll back if not
    try {
        & $Dest -version | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "exit $LASTEXITCODE" }
    } catch {
        if ($Backup) {
            Move-Item $Backup $Dest -Force
            Write-Error "new binary failed to run; restored previous version"
        }
        exit 1
    }
    # success — drop the backup
    if ($Backup) { Remove-Item $Backup -Force -ErrorAction SilentlyContinue }
    Write-Host "Installed $Dest"

    # --- PATH hint ----------------------------------------------------------
    $pathParts = ($env:PATH -split ';') | Where-Object { $_ -ne '' }
    if ($InstallDir -notin $pathParts) {
        $env:PATH = "$InstallDir;$env:PATH"
        Write-Host ""
        Write-Host "note: $InstallDir is not on your PATH."
        Write-Host "Added it to PATH for this PowerShell session."
        Write-Host "To add it permanently for the current user, run:"
        Write-Host "  [Environment]::SetEnvironmentVariable('PATH', `"$InstallDir;`$([Environment]::GetEnvironmentVariable('PATH','User'))`", 'User')"
    }
}
finally {
    Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host "Run: xcx"
