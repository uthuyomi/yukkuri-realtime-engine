# Troubleshooting

English | [日本語](troubleshooting.ja.md)

Start with health and capabilities separately. A successful health check does not guarantee provider inference.

```powershell
Invoke-RestMethod http://127.0.0.1:8765/health
Invoke-RestMethod http://127.0.0.1:8765/v1/capabilities | ConvertTo-Json -Depth 8
```

## TTS unavailable / AqKanji2Koe unavailable

Check all nine x64 voice DLLs, converter DLL and aq_dic against the quickstart paths. Run from the repository root. A startup TTS error may contain a local path; redact it before sharing. Do not download replacement DLLs from arbitrary mirrors.

## STT unavailable

Check STT_MODEL_PATH / STT_MODEL, the selected mode executable and its adjacent runtime DLLs. Invalid runtime configuration disables STT. A failed initialization needs Engine restart. Capabilities runtime shows safe state/fallback fields, not paths.

## CUDA requested but unavailable

Confirm the CUDA build, driver/Toolkit dependencies and compatible model. cuda deliberately does not fall back; auto may select CPU. Check requested_device, selected_device and fallback_reason. nvidia-smi alone is not an inference readiness test.

## Smart Turn unavailable / detector busy

Check port 8766 /health, model argument and the sidecar process. One native inference at a time; 503 busy is bounded rejection. The configured Engine capability does not probe live sidecar health.

## LLM unavailable / generation_failed

Set OPENAI_API_KEY privately in the Engine environment/.env; restart. Check OPENAI_MODEL account availability. The source default is not an account entitlement. Provider errors are sanitized; never paste credentials into an issue.

## Microphone permission / no input

Use localhost HTTP or a secure browser context, grant permission and select a functioning device. Click 接続 then マイク開始. Check CDN requests for Silero/ONNX; browser/device AEC is outside server control.

## No playback / audio stalls

Connect from a user click to resume AudioContext. Keep the tab/player alive and check device volume. Only actual rendered source frames replenish credit: never fake ACKs to bypass a stall. generation.done is not speaker completion.

## WebSocket / origin_rejected

Use HTTP-served page, not file://. Compare exact Origin with API_ALLOWED_ORIGINS; restart after editing .env. Use the Engine port 8765, not the static server port 8080. Reconnect creates a new session with no history restoration.

## Port already in use

Run Get-NetTCPConnection -LocalPort 8765,8766,8080 -ErrorAction SilentlyContinue. Identify the owning process before stopping it. Sidecar --port requires a matching TURN_DETECTOR_URL; Engine port is fixed in the supplied entry point.

## SDK dist missing / blank browser behavior

Run npm --prefix sdk/typescript ci and npm --prefix sdk/typescript run build. Serve the repository root, not just the example directory; verify dist/index.js and dist/browser.js return 200 in the browser network panel.

## resource_limit / audio_flow_error

Inspect discovered limits, turn length, credit arithmetic and active sessions. Pause preserves buffered PCM, so credit can intentionally stop. Cancel obsolete work/close unused sessions; do not silently retry committed user turns.

## go vet / race environment

The AquesTalk native return-pointer conversion produces possible misuse of unsafe.Pointer on Windows. See CONTRIBUTING for the narrow documented check split. Local CGO_ENABLED=0 without a C compiler cannot run -race; CI has a Linux portable-core race job.

When reporting, include OS, Go/Node/Python versions, safe capability state, error code, request/generation IDs and reproduction steps. Exclude keys, .env, recordings, conversations and proprietary assets. See [SECURITY](../SECURITY.md), [configuration](configuration.md), [quickstart](quickstart.md).
