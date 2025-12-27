# Gridlock Installation Script for Windows
# This script downloads the latest version of Gridlock and installs it.

$ErrorActionPreference = "Stop"

$Repo = "esaiaswestberg/gridlock"
$GithubApi = "https://api.github.com/repos/$Repo/releases/latest"

# Detect Architecture
$Arch = "amd64" # Default for most Windows users
if ([IntPtr]::Size -eq 4) {
    $Arch = "386"
}

Write-Host "Detecting latest version..."
try {
    $LatestRelease = (Invoke-RestMethod -Uri $GithubApi).tag_name
} catch {
    Write-Host "Failed to fetch latest release version: $_" -ForegroundColor Red
    exit 1
}

Write-Host "Latest version: $LatestRelease"

$Filename = "gridlock_windows_$Arch.zip"
$DownloadUrl = "https://github.com/$Repo/releases/download/$LatestRelease/$Filename"

Write-Host "Downloading $DownloadUrl..."
$TmpDir = [System.IO.Path]::GetTempFileName()
Remove-Item $TmpDir
New-Item -ItemType Directory -Path $TmpDir | Out-Null

$ZipPath = Join-Path $TmpDir $Filename
Invoke-WebRequest -Uri $DownloadUrl -OutFile $ZipPath

Write-Host "Extracting..."
Expand-Archive -Path $ZipPath -DestinationPath $TmpDir

$InstallDir = Join-Path $Home "bin"
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir | Out-Null
}

$BinaryName = "gridlock.exe"
$SourcePath = Join-Path $TmpDir $BinaryName
$TargetPath = Join-Path $InstallDir $BinaryName

Write-Host "Installing to $TargetPath..."
Move-Item -Path $SourcePath -Destination $TargetPath -Force

# Cleanup
Remove-Item -Path $TmpDir -Recurse -Force

Write-Host "Gridlock installed successfully!" -ForegroundColor Green

# Path checking
$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
    Write-Host ""
    Write-Host "Warning: $InstallDir is not in your PATH." -ForegroundColor Yellow
    Write-Host "To add it, run the following command in an Administrator PowerShell:"
    Write-Host "    [Environment]::SetEnvironmentVariable('Path', `$env:Path + ';$InstallDir', 'User')"
    Write-Host "After running this, you may need to restart your terminal."
}

Write-Host "Run 'gridlock --help' to get started."
