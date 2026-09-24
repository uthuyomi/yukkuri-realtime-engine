param(
    [ValidateSet('cpu', 'cuda')][string]$Backend = 'cpu',
    [ValidatePattern('^[0-9]+(;[0-9]+)*$')][string]$CudaArchitectures = '75',
    [string]$VCToolset = '',
    [ValidateRange(1, 32)][int]$Jobs = 2
)
$ErrorActionPreference = 'Stop'
$revision = 'd09f61a708f3487afa956ff578e60eae5e7a233c'
$repoRoot = Split-Path -Parent $PSScriptRoot
$runtimeRoot = Join-Path $repoRoot 'runtime/whisper'
$sourceDir = Join-Path $runtimeRoot 'upstream'
$buildDir = Join-Path $runtimeRoot "build-$Backend"
$outputDir = Join-Path $runtimeRoot $Backend

function Invoke-Checked([string]$Command, [string[]]$Arguments) {
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed (exit $LASTEXITCODE)" }
}
Get-Command git, cmake -ErrorAction Stop | Out-Null
if ($Backend -eq 'cuda') {
    Get-Command nvcc -ErrorAction Stop | Out-Null
    if (-not $env:CUDA_PATH) { throw 'CUDA_PATH must identify the installed CUDA Toolkit.' }
}
New-Item -ItemType Directory -Force -Path $runtimeRoot, $outputDir | Out-Null
if (-not (Test-Path -LiteralPath (Join-Path $sourceDir '.git'))) {
    Invoke-Checked 'git' @('clone', '--depth', '1', 'https://github.com/ggml-org/whisper.cpp.git', $sourceDir)
}
$dirty = & git -C $sourceDir status --porcelain
if ($LASTEXITCODE -ne 0 -or $dirty) { throw 'Upstream checkout is not clean; preserve changes and use a clean checkout.' }
$currentRevision = & git -C $sourceDir rev-parse HEAD
if ($currentRevision -ne $revision) {
    Invoke-Checked 'git' @('-C', $sourceDir, 'fetch', '--depth', '1', 'origin', $revision)
    Invoke-Checked 'git' @('-C', $sourceDir, 'checkout', '--detach', $revision)
}
$cudaFlag = if ($Backend -eq 'cuda') { 'ON' } else { 'OFF' }
$configure = @('-S', $sourceDir, '-B', $buildDir, '-G', 'Visual Studio 17 2022', '-A', 'x64',
    "-DGGML_CUDA=$cudaFlag", '-DGGML_NATIVE=OFF', '-DWHISPER_BUILD_TESTS=OFF',
    '-DWHISPER_BUILD_EXAMPLES=ON', '-DWHISPER_CURL=OFF', '-DBUILD_SHARED_LIBS=ON')
if ($VCToolset) { $configure += @('-T', "version=$VCToolset") }
if ($Backend -eq 'cuda') { $configure += "-DCMAKE_CUDA_ARCHITECTURES=$CudaArchitectures" }
Invoke-Checked 'cmake' $configure
Invoke-Checked 'cmake' @('--build', $buildDir, '--config', 'Release', '--target', 'whisper-server', 'whisper-cli', '--parallel', "$Jobs")
$binDir = Join-Path $buildDir 'bin/Release'
foreach ($name in @('whisper-server.exe', 'whisper-cli.exe')) {
    Copy-Item -LiteralPath (Join-Path $binDir $name) -Destination $outputDir -Force
}
Get-ChildItem -LiteralPath $binDir -Filter '*.dll' -File | ForEach-Object {
    Copy-Item -LiteralPath $_.FullName -Destination $outputDir -Force
}
if ($Backend -eq 'cuda') {
    # Upstream's Windows shared CUDA backend links CUDA runtime/cuBLAS DLLs.
    # These are local setup copies, not a license grant for redistributing them.
    foreach ($pattern in @('cudart64_*.dll', 'cublas64_*.dll', 'cublasLt64_*.dll')) {
        $dependencies = @(Get-ChildItem -LiteralPath (Join-Path $env:CUDA_PATH 'bin') -Filter $pattern -File)
        if ($dependencies.Count -eq 0) { throw "Missing CUDA dependency: $pattern" }
        foreach ($dependency in $dependencies) { Copy-Item -LiteralPath $dependency.FullName -Destination $outputDir -Force }
    }
}
@{ upstream_revision = $revision; backend = $Backend; cuda_architectures = $(if ($Backend -eq 'cuda') { $CudaArchitectures } else { $null }); configuration = 'Release' } |
    ConvertTo-Json | Set-Content -LiteralPath (Join-Path $outputDir 'build-info.json') -Encoding UTF8
Write-Host "Installed $Backend whisper-server/whisper-cli. Models are unchanged. Run stt-bench to validate the backend."
