#Requires -Version 5.1
<#
.SYNOPSIS
    Installs the Ferro Labs AI Gateway (ferrogw) for the current user.

.DESCRIPTION
    irm https://get.ferrolabs.ai/install.ps1 | iex

    Downloads the release archive for this machine's architecture, verifies it
    against the published SHA-256 checksums, and places the binary under
    %LOCALAPPDATA%\Programs\ferrogw. Nothing else is written: no config, no
    master key, no change to the current directory.

.NOTES
    Targets Windows PowerShell 5.1, which is in-box on Windows 10/11. Requiring
    pwsh 7 would mean an install before the install, so nothing here may use
    PowerShell 7 syntax (ternary ?:, ??, ?., -Parallel) or 7-only cmdlets.

    The file is deliberately pure ASCII. PowerShell 5.1 reads a BOM-less .ps1 as
    the ANSI code page, and `irm` decodes a charset-less text/plain response as
    ISO-8859-1 -- so any non-ASCII byte here would arrive mangled by one path or
    the other.
#>

# The env fallbacks are spelled literally rather than derived from $Product
# below, because param() defaults are bound before the script body runs.
# They exist because `irm | iex` cannot pass arguments -- for the piped form,
# which is the documented one, environment variables are the only channel.
param(
    [string] $Version    = $env:FERROGW_VERSION,
    [string] $InstallDir = $env:FERROGW_INSTALL_DIR,
    # Presence enables the switch, but the falsey spellings are honoured: a user
    # who writes FERROGW_NO_MODIFY_PATH=false means off, and [bool]'false' is $true.
    [switch] $NoModifyPath = ($env:FERROGW_NO_MODIFY_PATH -and $env:FERROGW_NO_MODIFY_PATH -notmatch '^(0|false|no)$'),
    [switch] $Help         = ($env:FERROGW_HELP -and $env:FERROGW_HELP -notmatch '^(0|false|no)$')
)

# --- Product ----------------------------------------------------------------
# The same seam install.sh carries: a sibling `ferro` (CLI) installer is meant to
# drop in by editing this table, not by rewriting the script.

$Product = @{
    Repo        = 'ferro-labs/ai-gateway' # GitHub owner/name; also roots the release URLs
    Binary      = 'ferrogw'               # binary inside the archive, and the installed name
    AssetPrefix = 'ferrogw'               # asset prefix: <prefix>_<version>_windows_<arch>.zip
    DisplayName = 'Ferro Labs AI Gateway' # product name, for human-facing output only
    EnvPrefix   = 'FERROGW'               # env namespace: <prefix>_VERSION, _INSTALL_DIR, ...
}

# --- Body -------------------------------------------------------------------
# Everything lives in this function and it is invoked on the very last line.
# A `irm | iex` whose transfer drops mid-file either fails to parse (unbalanced
# braces) or defines a function and calls nothing -- in both cases no byte is
# written to disk. Do not add statements with side effects after this point.
#
# The function deliberately takes no parameters and the call passes no
# arguments, reading the param() variables from the enclosing scope instead.
# An argument list would reopen the hole this structure exists to close: a
# transfer that drops partway through `Install-FerroGateway -Version $Version
# -InstallDir $InstallDir` leaves a command that still parses and still
# installs. A truncated bare identifier is an unknown command and runs nothing.

function Install-FerroGateway {
    # Preference variables are dynamically scoped, so these two apply to every
    # cmdlet called from here and are discarded when the function returns.
    # Stop matters most: without it a failed Copy-Item or Expand-Archive is a
    # non-terminating error and the script would report success.
    $ErrorActionPreference = 'Stop'
    $prevProgress = $ProgressPreference

    $binaryName = $Product.Binary + '.exe'

    if ($Help) {
        Write-Host @"
Install $($Product.DisplayName) ($($Product.Binary)) for the current user.

  irm https://get.ferrolabs.ai/install.ps1 | iex

Options (as parameters when the script is saved and run directly):
  -Version <tag>       Release to install, e.g. v1.4.2. Default: latest.
  -InstallDir <path>   Install location. Default: %LOCALAPPDATA%\Programs\$($Product.Binary)
  -NoModifyPath        Do not add the install directory to the user PATH.
  -Help                Show this message.

Environment equivalents, which are the only channel the piped form has:
  $($Product.EnvPrefix)_VERSION, $($Product.EnvPrefix)_INSTALL_DIR,
  $($Product.EnvPrefix)_NO_MODIFY_PATH, $($Product.EnvPrefix)_HELP
"@
        return
    }

    # --- Architecture -------------------------------------------------------
    # PROCESSOR_ARCHITEW6432 is set only under WOW64 and is the one that names
    # the real machine; PROCESSOR_ARCHITECTURE there describes the emulated
    # process (x86) and would send a 64-bit box to a 404.
    $rawArch = $env:PROCESSOR_ARCHITEW6432
    if (-not $rawArch) { $rawArch = $env:PROCESSOR_ARCHITECTURE }

    switch ($rawArch) {
        'AMD64' { $arch = 'amd64' }
        'ARM64' { $arch = 'arm64' }
        default {
            throw "Unsupported processor architecture '$rawArch'. $($Product.Binary) ships windows/amd64 and windows/arm64 only."
        }
    }

    # --- Transport ----------------------------------------------------------
    # PowerShell 5.1 on Windows Server 2016 and older Windows 10 builds defaults
    # to TLS 1.0, which GitHub refuses. -bor rather than assignment so a newer
    # runtime keeps whatever else it had enabled.
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    # --- Version ------------------------------------------------------------
    if (-not $Version) {
        $latestUrl = "https://github.com/$($Product.Repo)/releases/latest"
        Write-Host "Resolving the latest release..."

        # Deliberately not api.github.com: unauthenticated it is capped at 60
        # requests/hour per IP, which fails outright on shared CI egress. The
        # /releases/latest 302 has no such limit. HEAD because only the
        # redirect target is wanted, not the release page.
        # -UseBasicParsing keeps 5.1 off the Internet Explorer DOM engine,
        # which is absent on Server Core and unusable before IE's first run.
        $resp = Invoke-WebRequest -Uri $latestUrl -Method Head -UseBasicParsing -MaximumRedirection 5

        # 5.1 wraps System.Net.HttpWebResponse, which carries the post-redirect
        # URI as ResponseUri. PS 7 wraps HttpResponseMessage, which has no such
        # property and exposes it as RequestMessage.RequestUri instead.
        $finalUri = $resp.BaseResponse.ResponseUri
        if (-not $finalUri) { $finalUri = $resp.BaseResponse.RequestMessage.RequestUri }
        if (-not $finalUri) { throw "Could not read the redirect target of $latestUrl." }

        # Segments, not a split of the whole URI: it excludes any query string,
        # which would otherwise satisfy the guard below and 404 on download.
        $Version = $finalUri.Segments[-1]
    }

    $tag = $Version
    if ($tag -notmatch '^v') { $tag = "v$tag" }
    if ($tag -notmatch '^v\d+\.\d+\.\d+') {
        throw "Could not resolve a release version (got '$Version')."
    }
    # Asset names carry the bare version; only the tag keeps the `v`.
    $assetVersion = $tag.Substring(1)

    $assetName    = "$($Product.AssetPrefix)_${assetVersion}_windows_${arch}.zip"
    $downloadBase = "https://github.com/$($Product.Repo)/releases/download/$tag"

    if (-not $InstallDir) {
        if (-not $env:LOCALAPPDATA) {
            throw "LOCALAPPDATA is not set; pass -InstallDir or set $($Product.EnvPrefix)_INSTALL_DIR."
        }
        # User scope on purpose. Program Files needs elevation, and an installer
        # that triggers a UAC prompt is one a user cannot run from the shell
        # they already have open.
        $InstallDir = Join-Path $env:LOCALAPPDATA "Programs\$($Product.Binary)"
    }

    Write-Host "Installing $($Product.DisplayName) $tag (windows/$arch) to $InstallDir"

    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("$($Product.Binary)-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null

    try {
        # Invoke-WebRequest renders a progress bar per chunk on 5.1, which costs
        # roughly 10x the wall time on a release-sized archive.
        $ProgressPreference = 'SilentlyContinue'

        $zipPath       = Join-Path $tmpDir $assetName
        $checksumsPath = Join-Path $tmpDir 'checksums.txt'

        Invoke-WebRequest -Uri "$downloadBase/$assetName"  -OutFile $zipPath       -UseBasicParsing
        Invoke-WebRequest -Uri "$downloadBase/checksums.txt" -OutFile $checksumsPath -UseBasicParsing

        $ProgressPreference = $prevProgress

        # --- Verify ---------------------------------------------------------
        # Mandatory, with no flag to skip it. The release pipeline signs
        # checksums.txt; consulting it is the whole point of publishing it.
        $expected = $null
        foreach ($line in Get-Content -LiteralPath $checksumsPath) {
            $fields = $line -split '\s+', 2
            if ($fields.Count -eq 2 -and $fields[1].Trim() -eq $assetName) {
                $expected = $fields[0].Trim()
                break
            }
        }
        if (-not $expected) { throw "checksums.txt from $tag has no entry for $assetName." }

        $actual = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash
        # Get-FileHash returns uppercase and checksums.txt is lowercase, so both
        # sides are folded rather than relying on -ne's default case-insensitivity.
        if ($actual.ToLowerInvariant() -ne $expected.ToLowerInvariant()) {
            throw "Checksum mismatch for ${assetName}: expected $expected, got $actual. Aborting."
        }
        Write-Host "Checksum OK (SHA-256)"

        # The release signs checksums.txt with keyless cosign, which in turn
        # covers every archive. Like install.sh, this is a printed note rather
        # than a check: cosign is not a reasonable prerequisite for a one-liner.
        # Unlike install.sh there is no flag to promote it, because there is no
        # cosign on a stock Windows box for the flag to find.
        Write-Host "note: this release's checksums.txt is cosign-signed. Install cosign to verify the release itself:"
        Write-Host "  $downloadBase/checksums.txt.sig"

        # --- Install --------------------------------------------------------
        # Extracted to the temp directory, not to the install directory: the
        # archive also carries README/LICENSE/CHANGELOG/config examples/notices,
        # and this installer writes exactly one file.
        $extractDir = Join-Path $tmpDir 'extract'
        Expand-Archive -LiteralPath $zipPath -DestinationPath $extractDir -Force

        $extracted = Join-Path $extractDir $binaryName
        if (-not (Test-Path -LiteralPath $extracted)) {
            throw "$assetName did not contain $binaryName."
        }

        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        $target = Join-Path $InstallDir $binaryName
        Copy-Item -LiteralPath $extracted -Destination $target -Force
    }
    finally {
        $ProgressPreference = $prevProgress
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host "Installed $target"

    # --- PATH ---------------------------------------------------------------
    $pathAdded = $false
    if (-not $NoModifyPath) {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')

        $alreadyPresent = $false
        if ($userPath) {
            foreach ($entry in ($userPath -split ';')) {
                if ($entry -and $entry.Trim().TrimEnd('\') -ieq $InstallDir.TrimEnd('\')) {
                    $alreadyPresent = $true
                    break
                }
            }
        }

        if (-not $alreadyPresent) {
            # Known ceiling: this API writes the value back as REG_SZ, so a user
            # PATH that contained %VAR% references is stored expanded. Reaching
            # for Microsoft.Win32.Registry to preserve REG_EXPAND_SZ is the
            # upgrade path if that ever bites someone.
            if ($userPath) {
                $newPath = $userPath.TrimEnd(';') + ';' + $InstallDir
            } else {
                $newPath = $InstallDir
            }
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
            $pathAdded = $true
        }
    }

    # --- Next steps ---------------------------------------------------------
    Write-Host ""
    if ($pathAdded) {
        Write-Host "Added $InstallDir to your user PATH."
        Write-Host "Open a new terminal for it to take effect -- this one will not see it."
        Write-Host ""
    }

    # Single-quoted: $env:GATEWAY_CONFIG is the text the user types, not
    # something to expand here.
    Write-Host 'Next steps:'
    Write-Host '  ferrogw init                           # writes config.yaml, prints your master key'
    Write-Host '  $env:GATEWAY_CONFIG = "./config.yaml"  # the server ignores the file without this'
    Write-Host '  ferrogw serve'
    Write-Host ''
    Write-Host 'Docs: https://docs.ferrolabs.ai'
}

Install-FerroGateway
