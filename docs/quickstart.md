# Quickstart (Windows x64)

English | [日本語](quickstart.ja.md) · [README](../README.md)

Commands below use **PowerShell**, from your cloned repository root unless stated otherwise. Do not use cmd.exe `set` syntax in PowerShell. Existing local SDK/model files should be retained. Skip copying `.env.example` if `.env` already exists; edit the existing file without sharing its contents.

## Prerequisites

- Git and Go 1.27.1 (`go.mod`); check `go version`.
- Python 3.13 for the pinned Smart Turn dependency set. The separate Python SDK requires 3.11+.
- Node.js 22+ and npm for the browser SDK.
- CMake, Visual Studio 2022 Build Tools with Desktop development with C++, Windows SDK, x64 target for whisper.cpp.
- Optional NVIDIA driver/CUDA Toolkit and compatible MSVC toolset for CUDA. CPU is supported without these.
- AquesTalk1 Windows x64 and AqKanji2Koe Windows x64 assets obtained separately from AQUEST under applicable terms. External LLM credentials are needed for conversation.

## Local TTS assets

The current entry point loads all nine voice DLLs; a missing configured DLL disables TTS. Place the vendor's files at these paths relative to the repository root:

```text
internal/providers/tts/aquestalk/aqtk1_win/lib64/<voice>/AquesTalk.dll
  <voice>: f1, f2, f3, m1, m2, r1, dvd, imd1, jgr
internal/providers/tts/aquestalk/aqk2k_win/lib64/AqKanji2Koe.dll
internal/providers/tts/aquestalk/aqk2k_win/aq_dic/  (vendor dictionary files)
```

These directories are ignored and excluded from source packaging. Do not commit DLLs, dictionaries, SDK headers/libraries or keys. The entry point has no public TTS path/key environment variables; Go `aquestalk.Config` contains key fields but the entry point does not populate them. Consult [providers](providers.md) for the actual configuration boundary; do not invent an `AQUEST_*` variable.

## Build STT, acquire the model, build the client

The CPU script pins whisper.cpp to `d09f61a708f3487afa956ff578e60eae5e7a233c`, builds the server/CLI and copies runtime DLLs locally. It does not download the model. The next command invokes that checkout's [model downloader](https://github.com/ggml-org/whisper.cpp/blob/d09f61a708f3487afa956ff578e60eae5e7a233c/models/download-ggml-model.cmd); it uses the upstream model distribution and needs network access. Keep an existing verified small model instead of downloading it again.

```powershell
./scripts/setup-whisper.ps1 -Backend cpu
New-Item -ItemType Directory -Force runtime/whisper/models | Out-Null
& ./runtime/whisper/upstream/models/download-ggml-model.cmd small (Resolve-Path runtime/whisper/models).Path
Test-Path runtime/whisper/models/ggml-small.bin
./tools/turn-detector/setup.ps1
npm --prefix sdk/typescript ci
npm --prefix sdk/typescript run build
Copy-Item .env.example .env
```

Smart Turn setup creates its own virtualenv and verifies the pinned model's SHA-256. Edit `.env`: set `OPENAI_API_KEY` privately and `OPENAI_MODEL` to a Responses-compatible model available to your account. The source default is `gpt-5.6-luna`; this is not a guarantee of account/model availability. Empty key disables conversation. Keep `STT_DEVICE=cpu` for a CPU-only start.

## Run three terminals

```powershell
# Terminal 1 — repository root
./runtime/turn-detection/venv/Scripts/python.exe tools/turn-detector/server.py --model runtime/turn-detection/smart-turn-v3.2-cpu.onnx
```

```powershell
# Terminal 2 — repository root
$env:STT_DEVICE = 'cpu'
$env:STT_MODEL = 'small'
go run ./cmd/engine
```

```powershell
# Terminal 3 — repository root
python -m http.server 8080 --bind 127.0.0.1
```

Wait for `Smart Turn ready` and `HTTP server listening on 127.0.0.1:8765`. STT startup includes model loading and a real inference probe, potentially up to two minutes per attempted backend. Do not treat slow startup as a WebSocket error.

```powershell
Invoke-RestMethod http://127.0.0.1:8765/health
Invoke-RestMethod http://127.0.0.1:8765/v1/capabilities | ConvertTo-Json -Depth 8
Invoke-RestMethod http://127.0.0.1:8766/health
```

Open <http://127.0.0.1:8080/examples/typescript/browser-voice/>. Click 接続 (resumes audio), then マイク開始, grant microphone access, and speak. Stop microphone before switching input mode (the text form does this). Use 切断 to release microphone/player/socket. Stop terminal processes with Ctrl+C. `file://` is not supported by default Origin policy.

The browser fetches vad-web 0.0.31 and onnxruntime-web 1.22.0 assets from CDN. Core SDK has no ML runtime dependency; applications can self-host assets. Do not expose the repository HTTP server: it can serve `.env` and local assets to clients that can reach it.

## Optional CUDA

```powershell
nvcc --version
./scripts/setup-whisper.ps1 -Backend cuda -CudaArchitectures '75'
$env:STT_DEVICE = 'cuda'
$env:STT_MODEL = 'small'
go run ./cmd/engine
```

Use a CUDA Toolkit/MSVC pair that supports your GPU; `75` is the script's GTX 1660 target, not an allowlist. `-VCToolset` can select an installed compatible toolset. CPU/CUDA dependencies must remain in separate directories. Explicit `cuda` fails closed, never silently falls back; `auto` may fall back to CPU. See [STT runtime](stt-runtime.md). Do not infer readiness from GPU detection alone.

## SDK/CLI and provider-free verification

```powershell
python -m venv .venv
./.venv/Scripts/python.exe -m pip install -e ./sdk/python
./.venv/Scripts/python.exe -m yukkuri_realtime health
./.venv/Scripts/python.exe -m yukkuri_realtime capabilities
./.venv/Scripts/python.exe -m yukkuri_realtime realtime
```

The last command needs LLM; `speak` needs TTS; `transcribe` needs STT. Health needs no provider. API integration tests use fake providers and need no paid key/proprietary files/models: [contributing](../CONTRIBUTING.md). These instructions were checked against source and provider-free build/package tests; external downloads, CUDA and real-microphone setup were not repeated in Step 10.
