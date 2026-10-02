param(
    [Parameter(Mandatory = $true)]
    [string]$PayloadDirectory,

    [Parameter(Mandatory = $true)]
    [string]$InstallerPath,

    [Parameter(Mandatory = $true)]
    [string]$PortableArchivePath,

    [string]$ExpectedThumbprint,

    [switch]$RequireTrusted,

    [ValidateSet("canonical", "legacy-dual")]
    [string]$PortableLayout = "canonical"
)

$ErrorActionPreference = "Stop"

$expectedPayload = @(
    "reasonix-desktop.exe",
    "reasonix-guard.exe",
    "reasonix-launcher.exe",
    "reasonix-update-helper.exe",
    "reasonix-cli.exe",
    "reasonix-uninstall.exe"
)

function Assert-AuthenticodeSignature {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Path
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Signed Windows artifact is missing: $Path"
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($null -eq $signature.SignerCertificate -or $signature.SignatureType -eq "None") {
        throw "Authenticode signature is missing: $Path"
    }
    if ($RequireTrusted -and $signature.Status -ne "Valid") {
        throw "Authenticode signature is not trusted for $Path`: $($signature.Status) $($signature.StatusMessage)"
    }
    if ($ExpectedThumbprint -and ($signature.SignerCertificate.Thumbprint -ne $ExpectedThumbprint -or -not $signature.TimeStamperCertificate)) {
        throw "Unexpected signer or missing timestamp: $Path"
    }
    Write-Host "Authenticode $($signature.Status): $Path"
}

# signing-files.txt (desktop/packaging/signing-files.mjs) enumerates every PE
# file in the payload: the flat Go executables plus the Electron app/ tree.
# The SignPath artifact configuration signs exactly this set, so verify the
# same list instead of a hand-maintained copy.
$signingListPath = Join-Path $PayloadDirectory "signing-files.txt"
if (-not (Test-Path -LiteralPath $signingListPath -PathType Leaf)) {
    throw "Payload signing list is missing: $signingListPath"
}
$signingFiles = @(
    Get-Content -LiteralPath $signingListPath |
        ForEach-Object { $_.Trim() } |
        Where-Object { $_ -ne "" -and -not $_.StartsWith("#") }
)
if ($signingFiles.Count -eq 0) {
    throw "Payload signing list is empty: $signingListPath"
}
foreach ($name in $expectedPayload) {
    if ($signingFiles -notcontains $name) {
        throw "Payload signing list does not cover $name"
    }
}
if ($signingFiles -notcontains "app/Reasonix.exe") {
    throw "Payload signing list does not cover the Electron shell app/Reasonix.exe"
}
if ($signingFiles -notcontains "app/resources/bin/reasonix-cli-launcher.exe") {
    throw "Payload signing list does not cover the CLI entry app/resources/bin/reasonix-cli-launcher.exe"
}

$payloadFiles = @(Get-ChildItem -LiteralPath $PayloadDirectory -File -Filter "*.exe")
if ($payloadFiles.Count -ne $expectedPayload.Count) {
    throw "Payload must contain exactly $($expectedPayload.Count) flat executables, found $($payloadFiles.Count)"
}
foreach ($entry in $signingFiles) {
    Assert-AuthenticodeSignature -Path (Join-Path $PayloadDirectory ($entry -replace '/', [System.IO.Path]::DirectorySeparatorChar))
}
Assert-AuthenticodeSignature -Path $InstallerPath

$extractRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("reasonix-authenticode-" + [guid]::NewGuid().ToString("N"))
try {
    Expand-Archive -LiteralPath $PortableArchivePath -DestinationPath $extractRoot

    # Legacy portable releases kept all six executables at InstallRoot. The
    # versioned-v1 layout deliberately keeps only the launcher aliases and CLI
    # at the root, while the active Desktop, update helper, CLI and the Electron
    # app/ tree live under versions/vX.Y.Z/. Verify the exact layout selected by
    # current.json instead of treating the versioned executables as missing.
    $currentPath = Join-Path $extractRoot "current.json"
    if (-not (Test-Path -LiteralPath $currentPath -PathType Leaf)) {
        throw "Portable archive must use the versioned layout (current.json is missing)"
    }
    $current = Get-Content -LiteralPath $currentPath -Raw | ConvertFrom-Json
    if ($current.schemaVersion -ne 1) {
        throw "Portable current.json schemaVersion must be 1"
    }
    $activeVersion = [string]$current.activeVersion
    $activeDir = [string]$current.activeDir
    if ($activeVersion -notmatch '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$' -or
        [string]::IsNullOrWhiteSpace($activeDir) -or
        $activeDir.Replace("\", "/") -ne "versions/$activeVersion") {
        throw "Portable current.json must bind activeVersion to versions/<activeVersion>"
    }

    $activePath = [System.IO.Path]::GetFullPath((Join-Path $extractRoot $activeDir))
    $extractPrefix = [System.IO.Path]::GetFullPath($extractRoot).TrimEnd([char[]]@('\', '/')) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $activePath.StartsWith($extractPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not (Test-Path -LiteralPath $activePath -PathType Container)) {
        throw "Portable current.json activeDir escapes or is missing: $activeDir"
    }

    # Root/versioned executables mapped back to their payload source; every PE
    # file under the versioned app/ tree is verified from signing-files.txt.
    $portableSources = @(
        [pscustomobject]@{ Portable = "Reasonix.exe"; Payload = "reasonix-launcher.exe" },
        [pscustomobject]@{ Portable = "reasonix-cli.exe"; Payload = "app/resources/bin/reasonix-cli-launcher.exe" },
        [pscustomobject]@{ Portable = (Join-Path $activeDir "reasonix-desktop.exe"); Payload = "reasonix-desktop.exe" },
        [pscustomobject]@{ Portable = (Join-Path $activeDir "reasonix-update-helper.exe"); Payload = "reasonix-update-helper.exe" },
        [pscustomobject]@{ Portable = (Join-Path $activeDir "reasonix-cli.exe"); Payload = "reasonix-cli.exe" }
    )
    if ($PortableLayout -eq "legacy-dual") {
        $portableSources += [pscustomobject]@{ Portable = "reasonix-launcher.exe"; Payload = "reasonix-launcher.exe" }
    }
    foreach ($entry in ($signingFiles | Where-Object { $_ -like "app/*" })) {
        $portableSources += [pscustomobject]@{
            Portable = (Join-Path $activeDir ($entry -replace '/', [System.IO.Path]::DirectorySeparatorChar))
            Payload  = ($entry -replace '/', [System.IO.Path]::DirectorySeparatorChar)
        }
    }

    $expectedPE = @($portableSources | ForEach-Object { $_.Portable.Replace('\', '/').ToLowerInvariant() } | Sort-Object)
    $actualPE = @(Get-ChildItem -LiteralPath $extractRoot -Recurse -File |
        Where-Object { $_.Extension -in @('.exe', '.dll') } |
        ForEach-Object { $_.FullName.Substring($extractPrefix.Length).Replace('\', '/').ToLowerInvariant() } | Sort-Object)
    if (@(Compare-Object $expectedPE $actualPE).Count -ne 0) {
        throw "Portable PE inventory does not match the exact $PortableLayout signed payload mapping"
    }

    foreach ($entry in $portableSources) {
        $portablePath = Join-Path $extractRoot $entry.Portable
        Assert-AuthenticodeSignature -Path $portablePath
        $portableHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $portablePath).Hash
        $payloadHash = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $PayloadDirectory $entry.Payload)).Hash
        if ($portableHash -ne $payloadHash) {
            throw "Portable $($entry.Portable) does not match signed payload $($entry.Payload)"
        }
    }
}
finally {
    if (Test-Path -LiteralPath $extractRoot) {
        Remove-Item -LiteralPath $extractRoot -Recurse -Force
    }
}

Write-Host "Windows Authenticode release contract verified."
