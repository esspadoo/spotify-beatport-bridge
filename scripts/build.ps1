[CmdletBinding()]
param(
    [switch]$Arm64,
    [string]$Version = "dev"
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $PSScriptRoot
$distPath = Join-Path $projectRoot "dist"
$originalGOOS = $env:GOOS
$originalGOARCH = $env:GOARCH
$originalCGO = $env:CGO_ENABLED

function New-BPBridgePackage {
    param(
        [Parameter(Mandatory = $true)][string]$Binary,
        [Parameter(Mandatory = $true)][string]$ZipName
    )

    $stagePath = Join-Path $distPath (".package-" + [Guid]::NewGuid().ToString("N"))
    $distFull = [IO.Path]::GetFullPath($distPath).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    $stageFull = [IO.Path]::GetFullPath($stagePath)
    if (-not $stageFull.StartsWith($distFull, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to use package staging path outside dist: $stageFull"
    }

    New-Item -ItemType Directory -Path $stageFull | Out-Null
    try {
        Copy-Item -LiteralPath $Binary -Destination (Join-Path $stageFull "bpbridge.exe")
        Copy-Item -LiteralPath (Join-Path $projectRoot "README.md") -Destination $stageFull
        Copy-Item -LiteralPath (Join-Path $projectRoot "LICENSE") -Destination $stageFull
        Copy-Item -LiteralPath (Join-Path $projectRoot ".env.example") -Destination $stageFull
        Compress-Archive -Path (Join-Path $stageFull "*") -DestinationPath (Join-Path $distPath $ZipName) -Force
    }
    finally {
        Remove-Item -LiteralPath $stageFull -Recurse -Force
    }
}

Push-Location $projectRoot
try {
    $commit = "unknown"
    if ((Test-Path -LiteralPath (Join-Path $projectRoot ".git")) -and (Get-Command git -ErrorAction SilentlyContinue)) {
        $candidate = (& git rev-parse --short HEAD 2>$null)
        if ($LASTEXITCODE -eq 0 -and $candidate) { $commit = $candidate.Trim() }
    }
    $builtAt = [DateTime]::UtcNow.ToString("yyyy-MM-ddTHH:mm:ssZ")
    $ldflags = "-s -w -X github.com/bpbridge/bpbridge/internal/cli.Version=$Version -X github.com/bpbridge/bpbridge/internal/cli.Commit=$commit -X github.com/bpbridge/bpbridge/internal/cli.BuiltAt=$builtAt"

    & go test ./...
    if ($LASTEXITCODE -ne 0) { throw "go test failed" }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "go vet failed" }

    New-Item -ItemType Directory -Force -Path $distPath | Out-Null
    $env:CGO_ENABLED = "0"
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    & go build -trimpath -ldflags $ldflags -o (Join-Path $distPath "bpbridge.exe") ./cmd/bpbridge
    if ($LASTEXITCODE -ne 0) { throw "Windows amd64 build failed" }
    New-BPBridgePackage -Binary (Join-Path $distPath "bpbridge.exe") -ZipName "bpbridge-windows-amd64.zip"

    if ($Arm64) {
        $env:GOARCH = "arm64"
        & go build -trimpath -ldflags $ldflags -o (Join-Path $distPath "bpbridge-windows-arm64.exe") ./cmd/bpbridge
        if ($LASTEXITCODE -ne 0) { throw "Windows arm64 build failed" }
        New-BPBridgePackage -Binary (Join-Path $distPath "bpbridge-windows-arm64.exe") -ZipName "bpbridge-windows-arm64.zip"
    }

    Write-Host "Artifacts written to $distPath"
}
finally {
    $env:GOOS = $originalGOOS
    $env:GOARCH = $originalGOARCH
    $env:CGO_ENABLED = $originalCGO
    Pop-Location
}
