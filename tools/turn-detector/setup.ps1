$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath (Join-Path $PSScriptRoot '../..')
python -m venv runtime/turn-detection/venv
if ($LASTEXITCODE -ne 0) { throw 'venv creation failed' }
& runtime/turn-detection/venv/Scripts/python.exe -m pip install -r tools/turn-detector/requirements.txt
if ($LASTEXITCODE -ne 0) { throw 'dependency installation failed' }
$modelPath = Join-Path (Get-Location) 'runtime/turn-detection/smart-turn-v3.2-cpu.onnx'
$expectedHash = '2BB026316B14A660486A75B1733CD3FBAB8C2FD0314DC9AF7BE49F8CCA967E4F'
if (!(Test-Path -LiteralPath $modelPath) -or (Get-FileHash -LiteralPath $modelPath -Algorithm SHA256).Hash -ne $expectedHash) {
    Invoke-WebRequest -UseBasicParsing -Uri 'https://huggingface.co/pipecat-ai/smart-turn-v3/resolve/f766f81d3cfdf7737ac64aad813d91bbfd56bf93/smart-turn-v3.2-cpu.onnx' -OutFile $modelPath
}
if ((Get-FileHash -LiteralPath $modelPath -Algorithm SHA256).Hash -ne $expectedHash) { throw 'Model SHA256 mismatch' }
Write-Host 'Ready. Run: runtime/turn-detection/venv/Scripts/python.exe tools/turn-detector/server.py --model runtime/turn-detection/smart-turn-v3.2-cpu.onnx'
