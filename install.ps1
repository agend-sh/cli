# agend installer for Windows.
#   irm agend.sh/i.ps1 | iex
# Installs agend.exe to %LOCALAPPDATA%\agend\bin and adds it to the user PATH.

$ErrorActionPreference = 'Stop'

$Repo = 'agend-sh/cli'
$InstallDir = Join-Path $env:LOCALAPPDATA 'agend\bin'
$Binary = 'agend.exe'

# Windows PowerShell 5 defaults to TLS 1.0, which GitHub rejects.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { $Arch = 'amd64' }
    'ARM64' { $Arch = 'arm64' }
    default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

# Latest release tag
$Release = Invoke-RestMethod -UseBasicParsing -Uri "https://api.github.com/repos/$Repo/releases/latest"
$Tag = $Release.tag_name
if (-not $Tag) { throw 'Failed to fetch latest release.' }
$Version = $Tag.TrimStart('v')

$Archive = "agend-$Version-windows-$Arch.zip"
$Url = "https://github.com/$Repo/releases/download/$Tag/$Archive"
$ChecksumsUrl = "https://github.com/$Repo/releases/download/$Tag/checksums.txt"

Write-Host "Installing agend $Tag (windows/$Arch)..."

$TmpDir = Join-Path ([IO.Path]::GetTempPath()) "agend-install-$([Guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Path $TmpDir | Out-Null
try {
    $ArchivePath = Join-Path $TmpDir $Archive
    Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $ArchivePath
    Invoke-WebRequest -UseBasicParsing -Uri $ChecksumsUrl -OutFile (Join-Path $TmpDir 'checksums.txt')

    # The pinned amd64 verifier also runs under Windows 11 ARM64 emulation.
    # The agend archive itself remains native to the selected architecture.
    $Cosign = Join-Path $TmpDir 'cosign.exe'
    Invoke-WebRequest -UseBasicParsing -Uri 'https://github.com/sigstore/cosign/releases/download/v3.1.3/cosign-windows-amd64.exe' -OutFile $Cosign
    if ((Get-FileHash -Algorithm SHA256 -Path $Cosign).Hash.ToLower() -ne '9fe59be0eca1271873ce019061335eb1ac419b7059202e797828467ddabe33be') {
        throw 'Signature verifier checksum mismatch - refusing to install.'
    }
    $Bundle = Join-Path $TmpDir 'checksums.txt.sigstore.json'
    Invoke-WebRequest -UseBasicParsing -Uri "$ChecksumsUrl.sigstore.json" -OutFile $Bundle
    & $Cosign verify-blob --bundle $Bundle `
        --certificate-identity "https://github.com/agend-sh/cli/.github/workflows/release.yml@refs/tags/$Tag" `
        --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' (Join-Path $TmpDir 'checksums.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Release signature verification failed - refusing to install.' }

    # Verify checksum (fail closed: a missing entry aborts)
    $Expected = (Get-Content (Join-Path $TmpDir 'checksums.txt') |
        Where-Object { $_ -match "^([0-9a-fA-F]{64})\s+$([regex]::Escape($Archive))$" } |
        ForEach-Object { $Matches[1] } | Select-Object -First 1)
    if (-not $Expected) {
        throw "No checksum entry for $Archive in checksums.txt - refusing to install."
    }
    $Actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash
    if ($Actual.ToLower() -ne $Expected.ToLower()) {
        throw "Checksum verification failed!`n  Expected: $Expected`n  Got:      $Actual"
    }
    Write-Host 'Release signature and checksum verified.'

    Expand-Archive -Path $ArchivePath -DestinationPath $TmpDir -Force
    $Extracted = Get-ChildItem -Path $TmpDir -Recurse -Filter $Binary | Select-Object -First 1
    if (-not $Extracted) { throw "$Binary not found in archive." }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $Target = Join-Path $InstallDir $Binary

    # A running agend.exe (e.g. an active MCP server) can't be overwritten,
    # but it can be renamed aside; agend cleans up the .old on next launch.
    $Old = "$Target.old"
    if (Test-Path $Target) {
        Remove-Item $Old -Force -ErrorAction SilentlyContinue
        Move-Item -Path $Target -Destination $Old -Force
    }
    Move-Item -Path $Extracted.FullName -Destination $Target -Force
    Remove-Item $Old -Force -ErrorAction SilentlyContinue
}
finally {
    Remove-Item $TmpDir -Recurse -Force -ErrorAction SilentlyContinue
}

# Add the install dir to the user PATH (persistent) and this session.
$UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not ($UserPath -split ';' | Where-Object { $_ -eq $InstallDir })) {
    [Environment]::SetEnvironmentVariable('Path', "$UserPath;$InstallDir", 'User')
    Write-Host "Added $InstallDir to your user PATH."
}
if (-not ($env:Path -split ';' | Where-Object { $_ -eq $InstallDir })) {
    $env:Path = "$env:Path;$InstallDir"
}

Write-Host "Installed agend $Tag to $Target"
Write-Host ''
Write-Host 'Get started (new terminals pick up PATH automatically):'
Write-Host '  agend login'
Write-Host '  agend config claude-code'
Write-Host ''
Write-Host 'Update later with:'
Write-Host '  agend update'
