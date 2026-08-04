[CmdletBinding()]
param(
    [string]$Version,
    [string]$OutputDirectory,
    [string]$CertificateThumbprint,
    [string]$SignToolPath,
    [string]$TimestampUrl = "http://timestamp.acs.microsoft.com"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

if ([string]::IsNullOrWhiteSpace($Version)) {
    $Version = (Get-Content -Raw (Join-Path $repositoryRoot "VERSION")).Trim()
}
$Version = $Version.TrimStart("v")

if ($Version -notmatch '^\d+\.\d+\.\d+$') {
    throw "Version must contain exactly three numeric components, for example 0.3.0. Got: $Version"
}

$versionParts = $Version.Split('.') | ForEach-Object { [int]$_ }
if ($versionParts[0] -gt 255 -or $versionParts[1] -gt 255 -or $versionParts[2] -gt 65535) {
    throw "Version $Version is outside Windows Installer's supported numeric range."
}

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $OutputDirectory = Join-Path $repositoryRoot "dist"
} elseif (-not [System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repositoryRoot $OutputDirectory
}

$buildDirectory = Join-Path $repositoryRoot "build\windows-msi"
$applicationPath = Join-Path $buildDirectory "relay-app.exe"
$installerSource = Join-Path $repositoryRoot "installer\windows\Relay.wxs"
$versionedMsi = Join-Path $OutputDirectory "Relay-$Version-windows-x64.msi"
$stableMsi = Join-Path $OutputDirectory "Relay-windows-x64.msi"
$checksumsPath = Join-Path $OutputDirectory "SHA256SUMS-windows.txt"

New-Item -ItemType Directory -Force -Path $buildDirectory | Out-Null
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

function Invoke-ExternalCommand {
    param(
        [Parameter(Mandatory)]
        [string]$FilePath,
        [Parameter(Mandatory)]
        [string[]]$ArgumentList
    )

    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "$FilePath failed with exit code $LASTEXITCODE."
    }
}

function Resolve-SignTool {
    if (-not [string]::IsNullOrWhiteSpace($SignToolPath)) {
        if (-not (Test-Path -LiteralPath $SignToolPath -PathType Leaf)) {
            throw "signtool.exe was not found at $SignToolPath."
        }
        return (Resolve-Path $SignToolPath).Path
    }

    $command = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "signtool.exe is required when -CertificateThumbprint is supplied. Install the Windows SDK or pass -SignToolPath."
    }
    return $command.Source
}

function Invoke-CodeSign {
    param(
        [Parameter(Mandatory)]
        [string]$TargetPath
    )

    $signTool = Resolve-SignTool
    Invoke-ExternalCommand -FilePath $signTool -ArgumentList @(
        "sign",
        "/sha1", $CertificateThumbprint,
        "/fd", "SHA256",
        "/tr", $TimestampUrl,
        "/td", "SHA256",
        $TargetPath
    )
    Invoke-ExternalCommand -FilePath $signTool -ArgumentList @("verify", "/pa", "/v", $TargetPath)
}

Push-Location $repositoryRoot
try {
    Write-Host "Restoring the pinned WiX Toolset..."
    Invoke-ExternalCommand -FilePath "dotnet" -ArgumentList @("tool", "restore")

    Write-Host "Building relay-app.exe $Version..."
    $linkerFlags = "-s -w -H windowsgui"
    Invoke-ExternalCommand -FilePath "go" -ArgumentList @(
        "build",
        "-trimpath",
        "-tags", "desktop,production",
        "-ldflags", $linkerFlags,
        "-o", $applicationPath,
        "./cmd/relay-app"
    )

    if (-not [string]::IsNullOrWhiteSpace($CertificateThumbprint)) {
        Write-Host "Signing relay-app.exe..."
        Invoke-CodeSign -TargetPath $applicationPath
    }

    Write-Host "Building Relay MSI..."
    Invoke-ExternalCommand -FilePath "dotnet" -ArgumentList @(
        "tool", "run", "wix", "--",
        "build", $installerSource,
        "-arch", "x64",
        "-d", "ProductVersion=$Version",
        "-d", "RelayAppExe=$applicationPath",
        "-pdbtype", "none",
        "-out", $versionedMsi
    )

    if (-not [string]::IsNullOrWhiteSpace($CertificateThumbprint)) {
        Write-Host "Signing Relay MSI..."
        Invoke-CodeSign -TargetPath $versionedMsi
    }

    Copy-Item -LiteralPath $versionedMsi -Destination $stableMsi -Force

    Write-Host "Validating MSI payload with an administrative extraction..."
    $verificationDirectory = Join-Path $buildDirectory ("verify-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $verificationDirectory | Out-Null
    try {
        $process = Start-Process -FilePath "msiexec.exe" -ArgumentList @(
            "/a", ('"' + $versionedMsi + '"'),
            "/qn",
            ('TARGETDIR="' + $verificationDirectory + '"')
        ) -Wait -PassThru
        if ($process.ExitCode -ne 0) {
            throw "Windows Installer administrative extraction failed with exit code $($process.ExitCode)."
        }

        $extractedApplication = Get-ChildItem -LiteralPath $verificationDirectory -Recurse -Filter "relay-app.exe" | Select-Object -First 1
        if ($null -eq $extractedApplication) {
            throw "The MSI validation extraction did not contain relay-app.exe."
        }
    } finally {
        $resolvedBuildDirectory = [System.IO.Path]::GetFullPath($buildDirectory).TrimEnd('\') + '\'
        $resolvedVerificationDirectory = [System.IO.Path]::GetFullPath($verificationDirectory).TrimEnd('\') + '\'
        if ($resolvedVerificationDirectory.StartsWith($resolvedBuildDirectory, [System.StringComparison]::OrdinalIgnoreCase)) {
            Remove-Item -LiteralPath $verificationDirectory -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    $hashes = @(
        Get-FileHash -LiteralPath $versionedMsi -Algorithm SHA256
        Get-FileHash -LiteralPath $stableMsi -Algorithm SHA256
    )
    $checksumLines = $hashes | ForEach-Object {
        "$($_.Hash.ToLowerInvariant())  $([System.IO.Path]::GetFileName($_.Path))"
    }
    Set-Content -LiteralPath $checksumsPath -Value $checksumLines -Encoding utf8

    if ([string]::IsNullOrWhiteSpace($CertificateThumbprint)) {
        Write-Warning "The executable and MSI are unsigned. Configure Authenticode signing before presenting this as a trusted public download."
    }

    Write-Host "Created:"
    Write-Host "  $versionedMsi"
    Write-Host "  $stableMsi"
    Write-Host "  $checksumsPath"
} finally {
    Pop-Location
}
