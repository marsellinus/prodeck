<#
.SYNOPSIS
    Install the MobileDeck host agent on Windows.

.DESCRIPTION
    Two modes:

      from source    needs go. Builds a checkout - this one, or a directory
                     given with -Source - and installs the resulting binary.
                     This works today, with no network at all when run from
                     inside the repository.

      from release   downloads the published archive, verifies its SHA-256
                     against the checksums.txt in the same release, and
                     installs it. This works once the repository is published;
                     until then the script says so instead of failing with a
                     bare 404.

    The script never writes into the repository and never touches the agent's
    configuration directory (%APPDATA%\mobiledeck).

    The agent runs as the logged-in user and never elevates (docs/SECURITY.md
    section 4). The installer refuses to run from an elevated prompt unless
    -AllowElevated is given.

.PARAMETER FromSource
    Build from a checkout. The default when run from inside the repository.

.PARAMETER FromRelease
    Download and verify a published release.

.PARAMETER Source
    Directory of the checkout to build from.

.PARAMETER Version
    Pin a release tag, or set the build label reported by `mobiledeck version`
    in source mode.

.PARAMETER Prefix
    Directory that receives mobiledeck.exe. Default:
    %LOCALAPPDATA%\Programs\mobiledeck.

.PARAMETER WithGui
    Also build the desktop control panel. Implies CGO and needs a C toolchain
    (MinGW-w64 gcc) on PATH. Source builds only.

.PARAMETER NoService
    Do not install anything at logon. This is the default and exists so a
    caller can be explicit; Windows has no user-unit equivalent, and a Windows
    Service cannot inject input (docs/DEPLOYMENT.md section 3.2).

.PARAMETER DryRun
    Print every step without changing anything.

.PARAMETER AllowElevated
    Allow running from an elevated prompt. Intended for container or image
    builds only.

.PARAMETER Help
    Print usage.

.EXAMPLE
    .\scripts\install.ps1
    .\scripts\install.ps1 -FromRelease -Version v1.2.3
    .\scripts\install.ps1 -Prefix "$env:LOCALAPPDATA\Programs\mobiledeck" -DryRun

.NOTES
    See docs/INSTALL.md.
#>
[CmdletBinding()]
param(
    [switch]$FromSource,
    [switch]$FromRelease,
    [string]$Source,
    [string]$Version,
    [string]$Prefix,
    [switch]$WithGui,
    [switch]$NoService,
    [switch]$DryRun,
    [switch]$AllowElevated,
    [Alias('h')]
    [switch]$Help
)

$ErrorActionPreference = 'Stop'

# --- repository identity -----------------------------------------------------
# Publishing this repository is a two-line edit: owner and name. The clone URL
# and the release URL are both derived from them.
$REPO_OWNER = 'mobiledeck'
$REPO_NAME  = 'mobiledeck'

# The one place the release download URL is written.
$RELEASE_BASE_URL = "https://github.com/$REPO_OWNER/$REPO_NAME/releases"
$CLONE_URL        = "https://github.com/$REPO_OWNER/$REPO_NAME.git"

$CONFIG_DIR = Join-Path $env:APPDATA 'mobiledeck'

function Show-Usage {
    @"
install.ps1 - install the MobileDeck host agent (Windows)

usage: .\scripts\install.ps1 [-FromSource | -FromRelease] [options]

Modes (the default is source inside a checkout, release otherwise):
  -FromSource          build from a checkout (needs go)
  -FromRelease         download a published release and verify its checksum
  -Source DIR          build from DIR instead of the detected checkout

Options:
  -Version V           pin a release tag, or set the build label reported by
                       ``mobiledeck version`` in source mode
  -Prefix DIR          directory that receives mobiledeck.exe
                       (default: %LOCALAPPDATA%\Programs\mobiledeck)
  -WithGui             also build the desktop control panel (implies CGO; needs
                       a C toolchain on PATH; source builds only)
  -NoService           install nothing at logon (the default; Windows has no
                       user unit and a Windows Service cannot inject input)
  -DryRun              print every step without changing anything
  -AllowElevated       allow running from an elevated prompt (container builds)
  -Help, -h            this message

The installer adds the install directory to your user PATH; open a new terminal
afterwards. The agent runs as the logged-in user and never elevates
(docs/SECURITY.md section 4).
"@
}

function Die([string]$Message) {
    [Console]::Error.WriteLine("install: $Message")
    exit 1
}

function Note([string]$Message) {
    Write-Host $Message
}

if ($Help) {
    Show-Usage
    exit 0
}

if ($FromSource -and $FromRelease) {
    Die '-FromSource and -FromRelease are mutually exclusive'
}

# --- elevation ---------------------------------------------------------------

$principal = New-Object -TypeName Security.Principal.WindowsPrincipal -ArgumentList ([Security.Principal.WindowsIdentity]::GetCurrent())
$elevated = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if ($elevated -and -not $AllowElevated) {
    Die 'refusing to run from an elevated prompt. The agent must run as the logged-in user and never elevates (docs/SECURITY.md section 4). Pass -AllowElevated only to build an image.'
}

# --- platform ----------------------------------------------------------------

# PROCESSOR_ARCHITEW6432 is set when a 32-bit process runs on a 64-bit OS (WOW64)
# and carries the real OS architecture; PROCESSOR_ARCHITECTURE then describes the
# process. Prefer the OS one, so an ARM64 machine is not handed an emulated x64
# binary just because it launched a 32-bit PowerShell.
$processArch = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) {
    $processArch = $env:PROCESSOR_ARCHITEW6432
}

$arch = switch ($processArch) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    'x86'   { '386' }
    default { Die "unsupported architecture: $processArch" }
}
$goos = 'windows'

# --- locations ---------------------------------------------------------------

if (-not $Prefix -or $Prefix -eq '') {
    $Prefix = Join-Path $env:LOCALAPPDATA 'Programs\mobiledeck'
}
# Resolve against the PowerShell location, not the process directory, so a
# relative -Prefix lands where the caller typed it.
$Prefix = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Prefix)
$binary = Join-Path $Prefix 'mobiledeck.exe'

# A checkout is detected from this script's own location. When the script is
# piped into a shell there is no location, and release mode is used.
$repoRoot = $null
if ($PSScriptRoot) {
    $candidate = Join-Path $PSScriptRoot '..'
    if (Test-Path (Join-Path $candidate 'host\go.mod')) {
        $repoRoot = (Resolve-Path $candidate).Path
    }
}

if ($Source) {
    if (-not (Test-Path (Join-Path $Source 'host\go.mod'))) {
        Die "-Source $Source does not look like the repository (no host\go.mod)"
    }
    $Source = (Resolve-Path $Source).Path
}

$mode = $null
if ($FromSource) { $mode = 'source' }
if ($FromRelease) { $mode = 'release' }
if ($Source -and -not $mode) { $mode = 'source' }
if (-not $mode) {
    if ($repoRoot) { $mode = 'source' } else { $mode = 'release' }
}

if ($WithGui -and $mode -eq 'release') {
    Die '-WithGui needs a source build: the published archive is built with CGO disabled and contains no window. Use -FromSource.'
}

if ($mode -eq 'source' -and -not $Source) {
    $Source = $repoRoot
}

# --- version label -----------------------------------------------------------

$buildVersion = if ($Version) { $Version } else { 'dev' }
if ($mode -eq 'source' -and -not $Version -and $Source) {
    $git = Get-Command git -ErrorAction SilentlyContinue
    if ($git) {
        $described = & git -C $Source describe --tags --always --dirty 2>$null
        if ($LASTEXITCODE -eq 0 -and $described) {
            $buildVersion = "$described".Trim()
        }
    }
}

# --- helpers -----------------------------------------------------------------

$work = $null
function Get-WorkDir {
    if (-not $script:work) {
        $script:work = Join-Path ([System.IO.Path]::GetTempPath()) ("mobiledeck-install-" + [System.Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Force -Path $script:work | Out-Null
    }
    return $script:work
}

function Get-ReleaseBase {
    if ($Version) { return "$RELEASE_BASE_URL/download/$Version" }
    return "$RELEASE_BASE_URL/latest/download"
}

function Save-Url([string]$Url, [string]$Destination) {
    if (-not $script:tlsSet) {
        # Windows PowerShell 5.1 defaults to TLS 1.0, which GitHub refuses.
        try {
            [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
        } catch {
            # PowerShell 7 does not need this; ignore.
        }
        $script:tlsSet = $true
    }
    Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing
}

function Add-UserPath([string]$Directory) {
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    $entries = $userPath -split ';' | Where-Object { $_ -ne '' }
    $already = $entries | Where-Object { $_.TrimEnd('\') -ieq $Directory.TrimEnd('\') }
    if ($already) {
        Note "install: $Directory is already on your user PATH"
    } else {
        $newPath = (@($entries) + $Directory) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Note "install: added $Directory to your user PATH"
    }
    # Make it usable in this session too.
    $env:Path = "$env:Path;$Directory"
}

function Show-NextSteps {
    Write-Host ''
    Write-Host 'next:'
    Write-Host "  `"$binary`" run --bind 0.0.0.0"
    Write-Host "  `"$binary`" pair"
    Write-Host "  `"$binary`" doctor"
    Write-Host ''
}

# --- dry run -----------------------------------------------------------------

Note "install: $mode mode, target $goos/$arch, binary $binary"
if (Test-Path $CONFIG_DIR) {
    Note "install: configuration directory $CONFIG_DIR exists; it will not be touched"
}

if ($DryRun) {
    Note 'install: dry run, nothing will be changed'
    if ($mode -eq 'release') {
        $asset = "mobiledeck_windows_$arch.zip"
        $base = Get-ReleaseBase
        Note "would download $base/$asset"
        Note "would download $base/checksums.txt and verify the SHA-256 of $asset"
        Note "would expand $asset and install mobiledeck.exe to $binary"
    } else {
        if ($Source) {
            Note "would build $Source\host with CGO_ENABLED=$([int][bool]$WithGui) (version $buildVersion)"
        } else {
            Note "would clone $CLONE_URL and build it with CGO_ENABLED=$([int][bool]$WithGui) (version $buildVersion)"
        }
        Note "would install mobiledeck.exe to $binary"
    }
    Note "would add $Prefix to your user PATH (a new terminal is needed)"
    Show-NextSteps
    exit 0
}

# --- obtain the binary -------------------------------------------------------

$built = $null

if ($mode -eq 'release') {
    $asset = "mobiledeck_windows_$arch.zip"
    $base = Get-ReleaseBase
    $assetUrl = "$base/$asset"
    $checksumsUrl = "$base/checksums.txt"

    Note "install: downloading $assetUrl"
    $dir = Get-WorkDir
    $archive = Join-Path $dir $asset
    try {
        Save-Url $assetUrl $archive
    } catch {
        Die "could not download $assetUrl. Release downloads require the repository to be published; it is not yet. Use -FromSource to build from a checkout."
    }
    try {
        Save-Url $checksumsUrl (Join-Path $dir 'checksums.txt')
    } catch {
        Die "could not download $checksumsUrl, so the download cannot be verified. Refusing to install an unverified binary."
    }

    $expected = $null
    foreach ($line in Get-Content (Join-Path $dir 'checksums.txt')) {
        $parts = $line -split '\s+', 2
        if ($parts.Count -lt 2) { continue }
        $name = $parts[1].Trim().TrimStart('*')
        if ($name -eq $asset) { $expected = $parts[0].Trim().ToLowerInvariant(); break }
    }
    if (-not $expected) {
        Die "$asset is not listed in checksums.txt; refusing to install an unverified binary"
    }

    $actual = (Get-FileHash -Algorithm SHA256 -Path $archive).Hash.ToLowerInvariant()
    if ($actual -ne $expected) {
        Die "checksum mismatch for $asset`: expected $expected, got $actual"
    }
    Note "install: checksum ok ($actual)"

    # Expand-Archive in Windows PowerShell 5.1 only accepts .zip, so the download
    # is unpacked under that name regardless of what the release calls it.
    $unpacked = Join-Path $dir 'unpack'
    $zip = Join-Path $dir 'mobiledeck.zip'
    Copy-Item -Path $archive -Destination $zip -Force
    Expand-Archive -Path $zip -DestinationPath $unpacked -Force
    $found = Get-ChildItem -Path $unpacked -Recurse -File -Filter 'mobiledeck.exe' | Select-Object -First 1
    if (-not $found) {
        Die 'the archive did not contain mobiledeck.exe'
    }
    $built = $found.FullName
} else {
    if (-not $Source) {
        $git = Get-Command git -ErrorAction SilentlyContinue
        if (-not $git) {
            Die 'no checkout found and git is not on PATH. Run this script from inside the repository, pass -Source DIR, or publish the repository and use -FromRelease.'
        }
        $dir = Get-WorkDir
        $clone = Join-Path $dir 'src'
        if ($Version) {
            Note "install: cloning $CLONE_URL at $Version"
            & git clone --depth 1 --branch $Version $CLONE_URL $clone
        } else {
            Note "install: cloning $CLONE_URL"
            & git clone --depth 1 $CLONE_URL $clone
        }
        if ($LASTEXITCODE -ne 0) {
            Die "could not clone $CLONE_URL. A from-source install needs a checkout: run this script from inside the repository, or pass -Source DIR."
        }
        $Source = $clone
    }

    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Die 'from-source install needs Go 1.26 or newer on PATH'
    }

    $cgo = '0'
    if ($WithGui) {
        $cgo = '1'
        Note 'install: -WithGui needs a C toolchain (MinGW-w64 gcc) on PATH'
    }

    $dir = Get-WorkDir
    $out = Join-Path $dir 'mobiledeck.exe'
    Note "install: building $Source\host (CGO_ENABLED=$cgo, version $buildVersion)"
    Push-Location (Join-Path $Source 'host')
    try {
        $env:CGO_ENABLED = $cgo
        & go build -trimpath -ldflags "-X main.version=$buildVersion" -o $out ./cmd/mobiledeck
        if ($LASTEXITCODE -ne 0) {
            Die "go build failed ($LASTEXITCODE)"
        }
    } finally {
        Pop-Location
    }
    $built = $out
}

# --- install -----------------------------------------------------------------

New-Item -ItemType Directory -Force -Path $Prefix | Out-Null
Copy-Item -Path $built -Destination $binary -Force
Note "install: installed $binary"

Add-UserPath $Prefix
Note 'install: open a new terminal for the PATH change to take effect'

if (-not $NoService) {
    Note 'install: no logon entry was created. Windows has no user unit, and a Windows Service cannot inject input (docs/DEPLOYMENT.md section 3.2). To start at logon, see that section.'
}

Show-NextSteps
