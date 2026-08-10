$ErrorActionPreference = "Stop"

function Write-InstallerMessage([string]$Message) {
    Write-Host "dora installer: $Message"
}

$Version = if ($env:DORA_VERSION) { $env:DORA_VERSION } else { "__DORA_VERSION__" }
if ($Version -notmatch '^v[0-9A-Za-z._+-]+$') {
    throw "invalid version $Version"
}

$Architecture = if ($env:PROCESSOR_ARCHITEW6432) {
    $env:PROCESSOR_ARCHITEW6432
} else {
    $env:PROCESSOR_ARCHITECTURE
}
switch ($Architecture.ToUpperInvariant()) {
    "AMD64" { $Arch = "amd64" }
    "ARM64" { $Arch = "arm64" }
    default { throw "unsupported architecture: $Architecture" }
}

$InstallDir = if ($env:DORA_INSTALL_DIR) {
    $env:DORA_INSTALL_DIR
} elseif ($HOME) {
    Join-Path $HOME ".local\bin"
} else {
    throw "HOME is unset; set DORA_INSTALL_DIR"
}

$Archive = "dora-windows-$Arch.zip"
$ReleaseBase = if ($env:DORA_RELEASE_BASE_URL) {
    $env:DORA_RELEASE_BASE_URL.TrimEnd('/')
} else {
    "https://github.com/lgxz/dora/releases/download"
}
$ReleaseUrl = "$ReleaseBase/$Version"
$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("dora-installer-" + [guid]::NewGuid())
$TempTarget = $null
$TempMarker = $null

try {
    New-Item -ItemType Directory -Path $TempDir | Out-Null
    $ArchivePath = Join-Path $TempDir $Archive
    $ChecksumsPath = Join-Path $TempDir "checksums.txt"

    Write-InstallerMessage "downloading $Version for windows/$Arch"
    Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseUrl/$Archive" -OutFile $ArchivePath
    Invoke-WebRequest -UseBasicParsing -Uri "$ReleaseUrl/checksums.txt" -OutFile $ChecksumsPath

    $EscapedArchive = [regex]::Escape($Archive)
    $ChecksumLine = Get-Content $ChecksumsPath | Where-Object { $_ -match "\s+\*?$EscapedArchive$" } | Select-Object -First 1
    if (-not $ChecksumLine) {
        throw "checksum for $Archive is missing"
    }
    $Expected = ($ChecksumLine -split '\s+')[0].ToLowerInvariant()
    $Actual = (Get-FileHash -Algorithm SHA256 $ArchivePath).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected) {
        throw "checksum verification failed for $Archive"
    }

    Expand-Archive -Path $ArchivePath -DestinationPath $TempDir -Force
    $Binary = Join-Path $TempDir "dora.exe"
    if (-not (Test-Path -Path $Binary -PathType Leaf)) {
        throw "archive does not contain dora.exe"
    }

    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
    $InstallDir = (Resolve-Path $InstallDir).Path
    $Destination = Join-Path $InstallDir "dora.exe"
    $TempTarget = Join-Path $InstallDir ".dora-installer-$PID.exe"
    $TempMarker = Join-Path $InstallDir ".dora-install-$PID"
    Copy-Item $Binary $TempTarget -Force
    [System.IO.File]::WriteAllText(
        $TempMarker,
        '{"schema":1,"repository":"lgxz/dora"}',
        [System.Text.UTF8Encoding]::new($false)
    )
    Move-Item $TempTarget $Destination -Force
    $TempTarget = $null
    Move-Item $TempMarker (Join-Path $InstallDir ".dora-install.json") -Force
    $TempMarker = $null

    Write-InstallerMessage "installed $Destination"
    & $Destination --version

    $PathEntries = $env:PATH -split ';'
    if ($InstallDir -notin $PathEntries) {
        Write-InstallerMessage "add $InstallDir to PATH to run dora directly"
    }
} finally {
    if ($TempTarget -and (Test-Path $TempTarget)) {
        Remove-Item $TempTarget -Force
    }
    if ($TempMarker -and (Test-Path $TempMarker)) {
        Remove-Item $TempMarker -Force
    }
    if (Test-Path $TempDir) {
        Remove-Item $TempDir -Recurse -Force
    }
}
