# install.ps1 - Windows installer script for entire-tail
#
# Builds entire-tail.exe in place, then:
#   1. Installs entire-tail.exe and et.exe into $HOME\.local\bin
#   2. Ensures $HOME\.local\bin is present in User PATH
#   3. Best-effort registers as an entire CLI plugin if available.

$ErrorActionPreference = "Stop"

$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
if (-not $Here) { $Here = Get-Location }
$Bin = Join-Path $Here "entire-tail.exe"

# 1. Build binary
$goCmd = Get-Command go -ErrorAction SilentlyContinue
if (-not $goCmd) {
    $machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $env:Path = "$machinePath;$userPath"
    $goCmd = Get-Command go -ErrorAction SilentlyContinue
}

if (-not $goCmd) {
    Write-Error "Go toolchain not found. Install Go (https://go.dev/dl/) and re-run."
    exit 1
}

Write-Host "Building entire-tail.exe..." -ForegroundColor Cyan
Push-Location $Here
try {
    & go build -o $Bin .
    if ($LASTEXITCODE -ne 0) {
        Write-Error "Failed to build entire-tail.exe"
        exit 1
    }
} finally {
    Pop-Location
}
Write-Host "Built: $Bin" -ForegroundColor Green

# 2. Install executables: entire-tail.exe & et.exe
$LocalBin = Join-Path $HOME ".local\bin"
if (-not (Test-Path $LocalBin)) {
    New-Item -ItemType Directory -Path $LocalBin | Out-Null
}

$DestEntireTail = Join-Path $LocalBin "entire-tail.exe"
$DestEt = Join-Path $LocalBin "et.exe"

Copy-Item -Path $Bin -Destination $DestEntireTail -Force
Copy-Item -Path $Bin -Destination $DestEt -Force

Write-Host "Installed: $DestEntireTail" -ForegroundColor Green
Write-Host "Installed shortcut: $DestEt (run as 'et')" -ForegroundColor Green

# 3. Add $LocalBin to User PATH if missing
$currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pathItems = ($currentPath -split ";") | Where-Object { $_ -ne "" } | ForEach-Object { $_.TrimEnd("\") }
$targetNorm = $LocalBin.TrimEnd("\")

if ($pathItems -notcontains $targetNorm) {
    Write-Host "Adding $LocalBin to User PATH..." -ForegroundColor Yellow
    $newPath = if ([string]::IsNullOrWhiteSpace($currentPath)) { $LocalBin } else { "$currentPath;$LocalBin" }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    $env:Path = "$env:Path;$LocalBin"
    Write-Host "Added $LocalBin to User PATH." -ForegroundColor Green
} else {
    Write-Host "$LocalBin is already in User PATH." -ForegroundColor Gray
}

# 4. Plugin registration (best-effort)
$entireCmd = Get-Command entire -ErrorAction SilentlyContinue
if ($entireCmd) {
    try {
        & entire plugin install $Bin --force 2>&1 | Out-Null
        Write-Host "Registered as entire plugin: invoke with 'entire tail'." -ForegroundColor Green
    } catch {
        Write-Warning "'entire plugin install' failed - standalone 'et' and 'entire-tail' commands remain available."
    }
} else {
    Write-Host "Note: 'entire' CLI not found on PATH. Skipping plugin registration." -ForegroundColor Gray
    Write-Host "      You can run 'et' or 'entire-tail' directly." -ForegroundColor Gray
}

Write-Host ""
Write-Host "Installation complete! You can now run 'et' or 'entire-tail' from any terminal." -ForegroundColor Cyan
