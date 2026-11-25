# Install the latest agrouter release on Windows, without requiring Go or administrator rights.
# The script block keeps its preferences out of the caller's session when run through `irm ... | iex`.
& {
    $ErrorActionPreference = "Stop"
    Set-StrictMode -Version Latest
    $ProgressPreference = "SilentlyContinue"
    # Windows PowerShell 5.1 can default to TLS 1.0, which GitHub refuses
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $repository = "svtlv/agrouter"
    $executable = "agrouter.exe"
    $installDirectory = if ($env:AGROUTER_INSTALL_DIR) { $env:AGROUTER_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\agrouter" }
    $architecture = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
        "X64" { "amd64" }
        "Arm64" { "arm64" }
        default { throw "agrouter installer: unsupported Windows architecture: $_" }
    }

    $headers = @{ "User-Agent" = "agrouter-installer" }
    $tag = $env:AGROUTER_VERSION
    if (-not $tag) {
        $tag = (Invoke-RestMethod -UseBasicParsing -Headers $headers -Uri "https://api.github.com/repos/$repository/releases/latest").tag_name
    }
    # the same tag format the release workflow accepts
    if ($tag -cnotmatch '^v[01]\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9a-z-]+(\.[0-9a-z-]+)*)?$') {
        throw "agrouter installer: invalid release tag: $tag"
    }
    $version = $tag.Substring(1)
    $archive = "agrouter_${version}_windows_${architecture}.zip"
    $download = "https://github.com/$repository/releases/download/$tag"

    $temporaryDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("agrouter-" + [guid]::NewGuid())
    $archivePath = Join-Path $temporaryDirectory $archive
    $checksumsPath = Join-Path $temporaryDirectory "checksums.txt"
    $extractPath = Join-Path $temporaryDirectory "extract"

    try {
        New-Item -ItemType Directory -Path $extractPath -Force | Out-Null
        Write-Host "Downloading agrouter $version for windows/$architecture..."
        Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri "$download/$archive" -OutFile $archivePath
        Invoke-WebRequest -UseBasicParsing -Headers $headers -Uri "$download/checksums.txt" -OutFile $checksumsPath

        $expected = @(Get-Content -LiteralPath $checksumsPath | ForEach-Object {
                $fields = -split $_
                if ($fields.Count -eq 2 -and $fields[1] -ceq $archive) { $fields[0] }
            })
        if ($expected.Count -ne 1 -or $expected[0] -notmatch '^[a-fA-F0-9]{64}$') {
            throw "agrouter installer: missing or invalid checksum for $archive"
        }
        $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash
        if ($actual -ne $expected[0]) {
            throw "agrouter installer: checksum mismatch for $archive"
        }

        Expand-Archive -LiteralPath $archivePath -DestinationPath $extractPath -Force
        $sourceExecutable = Join-Path $extractPath $executable
        if (-not (Test-Path -LiteralPath $sourceExecutable -PathType Leaf)) {
            throw "agrouter installer: the release archive does not contain $executable"
        }

        New-Item -ItemType Directory -Path $installDirectory -Force | Out-Null
        Copy-Item -LiteralPath $sourceExecutable -Destination (Join-Path $installDirectory $executable) -Force

        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $pathEntries = @($userPath -split ";" | Where-Object { $_ })
        if ($pathEntries -notcontains $installDirectory) {
            [Environment]::SetEnvironmentVariable("Path", (@($pathEntries) + $installDirectory) -join ";", "User")
            $env:Path = "$env:Path;$installDirectory"
        }

        Write-Host "Installed $tag to $installDirectory\$executable"
        Write-Host "Open a new terminal, then run: agrouter --version"
    }
    finally {
        if (Test-Path -LiteralPath $temporaryDirectory) {
            Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
        }
    }
}
