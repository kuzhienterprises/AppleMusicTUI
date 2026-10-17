<#
.SYNOPSIS
    Installs am, Apple Music in your terminal, for the current Windows user.

.DESCRIPTION    Downloads the release built for this machine from GitHub, verifies it against
    the checksums.txt published with that release, installs it under
    %LOCALAPPDATA%\Programs\am, adds that directory to your user PATH, and then
    runs am doctor so you can see what the app still needs.

    Set AM_INSTALL_BASE_URL to download from a mirror instead of GitHub; with it
    set, pass -Version explicitly, for example -Version v0.1.1.

    No administrator rights are needed, and nothing outside your own profile is
    touched.

.PARAMETER Version
    Release tag to install, for example v0.1.1. Defaults to the latest release.

.PARAMETER InstallDir
    Install directory. Defaults to %LOCALAPPDATA%\Programs\am.

.PARAMETER NoPath
    Leave the user PATH alone.

.PARAMETER WithMpv
    Also install mpv with winget. mpv is only needed for 30 second previews.

.PARAMETER Uninstall
    Remove am and its PATH entry. Your settings and sign-in are left alone.

.PARAMETER DryRun
    Print what would be done and change nothing.

.EXAMPLE
    irm https://raw.githubusercontent.com/kuzhienterprises/AppleMusicTUI/main/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Version v0.1.1 -WithMpv
#>
[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [string]$InstallDir = (Join-Path $env:LOCALAPPDATA 'Programs\am'),
    [switch]$NoPath,
    [switch]$WithMpv,
    [switch]$Uninstall,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'

$repo      = 'kuzhienterprises/AppleMusicTUI'
$runAsFile = [bool]($PSCommandPath -and ([IO.Path]::GetFileName($PSCommandPath) -eq 'install.ps1'))
$pathChanged = $false

function Say  { param([string]$m) Write-Host "==> $m" -ForegroundColor Cyan }
function Ok   { param([string]$m) Write-Host "  ok  $m" -ForegroundColor Green }
function Warn { param([string]$m) Write-Host "  !!  $m" -ForegroundColor Yellow }
function Fail { param([string]$m) Write-Host "  xx  $m" -ForegroundColor Red }
function Done { param([int]$code) if ($runAsFile) { exit $code } }

function Get-UserPathState {
    $key = Get-Item 'HKCU:\Environment'
    $names = $key.GetValueNames()
    $raw = ''
    $kind = 'ExpandString'
    if ($names -contains 'Path') {
        $raw = [string]$key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
        $kind = $key.GetValueKind('Path').ToString()
    }
    return [pscustomobject]@{ Raw = $raw; Kind = $kind }
}

function Save-UserPathState {
    param([string]$Raw, [string]$Kind)
    Set-ItemProperty -Path 'HKCU:\Environment' -Name 'Path' -Value $Raw -Type $Kind
}

function Split-PathList {
    param([string]$Raw)
    return @($Raw -split ';' | Where-Object { $_ -ne '' })
}

function Add-ToUserPath {
    param([string]$Dir)
    $state = Get-UserPathState
    $wanted = $Dir.TrimEnd('\')
    $present = @($state.Raw -split ';' | Where-Object { $_ -ne '' -and $_.TrimEnd('\') -eq $wanted })
    if ($present.Count -gt 0) {
        Ok "already on your PATH: $Dir"
        return
    }
    # Prepend, and leave the rest of the value exactly as it was.
    if ($state.Raw -eq '') {
        Save-UserPathState $Dir $state.Kind
    } else {
        Save-UserPathState ($Dir + ';' + $state.Raw) $state.Kind
    }
    $script:pathChanged = $true
    Ok "added to the front of your user PATH: $Dir"
}

function Remove-FromUserPath {
    param([string]$Dir)
    $state = Get-UserPathState
    $wanted = $Dir.TrimEnd('\')
    # -split keeps empty segments, so joining the survivors restores the value
    # byte for byte apart from the entry being removed.
    $segments = @($state.Raw -split ';')
    $kept = @($segments | Where-Object { $_.TrimEnd('\') -ne $wanted })
    if ($kept.Count -eq $segments.Count) {
        Warn "$Dir was not on your PATH"
        return
    }
    Save-UserPathState ($kept -join ';') $state.Kind
    $script:pathChanged = $true
    Ok "removed from your user PATH: $Dir"
}

function Test-BrowserPresent {
    $probe = @(
        'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe',
        'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe',
        'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe',
        'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe'
    )
    foreach ($p in $probe) { if (Test-Path $p) { return $true } }
    return $false
}

function Get-ReleaseTag {
    param([string]$Wanted)
    if ($env:AM_INSTALL_BASE_URL -and $Wanted -eq 'latest') {
        throw 'set -Version when AM_INSTALL_BASE_URL is used, for example -Version v0.1.1'
    }
    if ($Wanted -ne 'latest') {
        if ($Wanted -notlike 'v*') { return "v$Wanted" }
        return $Wanted
    }
    Say 'Finding the latest release'
    try {
        $headers = @{ 'User-Agent' = 'am-installer' }
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers $headers
        if ($release.tag_name) { return $release.tag_name }
    } catch {
        # The API is rate limited per IP address, so a 403 here is normal on a
        # shared connection. The releases feed is not limited at all.
    }
    try {
        $feed = @{ Uri = "https://github.com/$repo/releases.atom" }
        if ($PSVersionTable.PSVersion.Major -le 5) { $feed.UseBasicParsing = $true }
        $content = (Invoke-WebRequest @feed).Content
        $match = [regex]::Match($content, 'releases/tag/([^"]+)')
        if ($match.Success) { return $match.Groups[1].Value }
    } catch { }
    throw 'could not find the latest release; pass -Version v0.1.1, or set AM_INSTALL_BASE_URL to a mirror'
}

function Invoke-Download {
    param([string]$Uri, [string]$OutFile)
    $params = @{ Uri = $Uri; OutFile = $OutFile }
    if ($PSVersionTable.PSVersion.Major -le 5) { $params.UseBasicParsing = $true }
    Invoke-WebRequest @params
}

try {
    # Windows PowerShell 5.1 still negotiates TLS 1.0 by default.
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch { }

    Write-Host ''
    Write-Host 'am - Apple Music in your terminal' -ForegroundColor White
    Write-Host ''

    $windows = $true
    if ($PSVersionTable.PSEdition -eq 'Core') { $windows = $IsWindows }
    if (-not $windows) {
        throw 'this installer is for Windows; on macOS or Linux run install.sh from the same repository'
    }

    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    switch ($arch) {
        'AMD64' { $goarch = 'amd64' }
        'ARM64' { $goarch = 'arm64' }
        default {
            throw "there is no prebuilt binary for $arch. Build from source instead:`n  go install github.com/$repo/cmd/am@latest"
        }
    }
    Ok "Windows $goarch detected"

    $target = Join-Path $InstallDir 'am.exe'

    if ($Uninstall) {
        Say 'Removing am'
        if (Test-Path $target) {
            Remove-Item $target -Force
            Ok "removed $target"
        } else {
            Warn "there is no am at $target"
        }
        foreach ($doc in 'LICENSE', 'README.md') {
            $f = Join-Path $InstallDir $doc
            if (Test-Path $f) { Remove-Item $f -Force }
        }
        if (Test-Path $InstallDir) {
            if (@(Get-ChildItem $InstallDir -Force).Count -eq 0) { Remove-Item $InstallDir -Force }
        }
        if (-not $NoPath) { Remove-FromUserPath $InstallDir }
        Write-Host ''
        Ok 'your settings and sign-in were left alone'
        Write-Host "  $env:APPDATA\applemusic-cli" -ForegroundColor DarkGray
        Write-Host ''
        Done 0
        return
    }

    $tag = Get-ReleaseTag $Version
    $ver = $tag.TrimStart('v')
    $asset = "am_${ver}_windows_${goarch}.zip"
    if ($env:AM_INSTALL_BASE_URL) {
        $base = $env:AM_INSTALL_BASE_URL.TrimEnd('/')
    } else {
        $base = "https://github.com/$repo/releases/download/$tag"
    }

    if ($DryRun) {
        Say 'Dry run, nothing will be changed'
        Write-Host "  release    $tag"
        Write-Host "  asset      $asset"
        Write-Host "  from       $base"
        Write-Host "  install to $target"
        Write-Host ("  PATH       " + $(if ($NoPath) { 'left alone' } else { 'add ' + $InstallDir }))
        Write-Host ''
        Done 0
        return
    }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("am-install-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null

    try {
        Say "Downloading $asset ($tag)"
        Invoke-Download "$base/$asset" (Join-Path $tmp $asset)
        Invoke-Download "$base/checksums.txt" (Join-Path $tmp 'checksums.txt')

        Say 'Verifying the download'
        $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { ($_ -split '\s+')[1] -eq $asset } | Select-Object -First 1
        if (-not $line) { throw "checksums.txt has no entry for $asset" }
        $want = ($line -split '\s+')[0].ToUpper()
        $got = (Get-FileHash -Path (Join-Path $tmp $asset) -Algorithm SHA256).Hash.ToUpper()
        if ($got -ne $want) {
            throw "checksum mismatch for $asset`n  expected $want`n  got      $got"
        }
        Ok "sha256 $($got.Substring(0, 16).ToLower())..."

        Say 'Installing'
        Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force
        $source = Join-Path $tmp 'am.exe'
        if (-not (Test-Path $source)) {
            $found = Get-ChildItem $tmp -Recurse -Filter 'am.exe' | Select-Object -First 1
            if (-not $found) { throw 'the archive did not contain am.exe' }
            $source = $found.FullName
        }
        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        if (Test-Path $target) {
            try {
                $old = (& $target --version 2>$null | Select-Object -First 1)
                if ($old) { Say "Replacing $old" }
            } catch { }
        }
        Copy-Item $source $target -Force
        foreach ($doc in 'LICENSE', 'README.md') {
            $f = Join-Path $tmp $doc
            if (Test-Path $f) { Copy-Item $f (Join-Path $InstallDir $doc) -Force }
        }
        Ok "installed $target"
    } finally {
        Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    if (-not $NoPath) { Add-ToUserPath $InstallDir }

    Say 'Checking this machine'
    $installed = (& $target --version 2>&1 | Select-Object -First 1)
    Ok "$installed"

    $earlier = Get-Command am -ErrorAction SilentlyContinue
    if ($earlier -and $earlier.Source -ne $target -and $earlier.Source -notlike "$InstallDir*") {
        Warn "another am is on your PATH at $($earlier.Source)"
    }

    if (Test-BrowserPresent) {
        Ok 'a Chromium browser is available'
    } else {
        Warn 'no Chrome or Edge found: one is needed for sign-in and full-length playback'
        Warn '  https://www.google.com/chrome/'
    }

    $mpv = Get-Command mpv -ErrorAction SilentlyContinue
    if ($mpv) {
        Ok "mpv is available at $($mpv.Source)"
    } else {
        if ($WithMpv -and (Get-Command winget -ErrorAction SilentlyContinue)) {
            Say 'Installing mpv with winget'
            winget install --id mpv-player.mpv-CI.MSVC -e --accept-package-agreements --accept-source-agreements
        } else {
            Warn 'mpv is not installed: it is only needed for 30 second previews'
            Warn '  winget install --id mpv-player.mpv-CI.MSVC -e'
        }
    }

    Write-Host ''
    Say 'Running am doctor'
    & $target doctor
    $doctorCode = $LASTEXITCODE

    Write-Host ''
    Write-Host 'am is installed.' -ForegroundColor Green
    Write-Host "  binary  $target"
    if ($doctorCode -ne 0) {
        Write-Host '  am doctor reported something above that still needs attention' -ForegroundColor Yellow
    }
    Write-Host ''
    Write-Host 'Next:'
    Write-Host '  am login                      sign in with your browser, once'
    Write-Host '  am playlists                  your library'
    Write-Host '  am search "instant crush"     the catalogue'
    if ($pathChanged) {
        Write-Host ''
        Write-Host 'Open a new terminal for the PATH change to apply everywhere.' -ForegroundColor Yellow
    }
    Write-Host ''
    # The install succeeded; am doctor reports on the machine, not on the install.
    Done 0
} catch {
    Write-Host ''
    Fail $_.Exception.Message
    Write-Host ''
    Write-Host 'Nothing was changed.' -ForegroundColor DarkGray
    Write-Host ''
    Done 1
}
