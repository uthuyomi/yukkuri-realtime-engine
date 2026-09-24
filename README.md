# Yukkuri Realtime Engine

Self-hosted voice engine with AquesTalk output, whisper.cpp transcription and realtime conversation. Public protocol version: **1**.

- [Public API / HTTP / capabilities / limits](docs/api.md)
- [Realtime WebSocket protocol and complete event catalog](docs/realtime-protocol.md)
- [Transcription-only WebSocket API](docs/transcription-api.md)
- [Errors and correlation](docs/errors.md)
- [Protocol versioning and compatibility](docs/protocol-versioning.md)
- [Client event JSON Schema](docs/protocol/client-events.schema.json)
- [Server envelope JSON Schema](docs/protocol/server-event.schema.json)
- [Public API Finishing implementation report](docs/public-api-finishing.md)

`go run ./cmd/engine` starts the Windows engine on `127.0.0.1:8765`. Configured providers are exposed through `/v1/capabilities`; missing TTS, STT or LLM disables the corresponding service rather than terminating the process. Proprietary AquesTalk files and whisper.cpp/model files must be installed separately for those services. `OPENAI_API_KEY` is optional when conversation is not needed. No keys or paths are exposed by discovery.

Serve `examples/browser/` from a localhost HTTP server, open `realtime-test.html`, then select Start Audio / Start Microphone. Continuous endpointing requires the configured SmartTurn sidecar. `file://` pages have a null Origin and are rejected by default. For additional browser origins, set `API_ALLOWED_ORIGINS` to a comma-separated list of exact origins. This is a local self-hosted service without authentication; Origin checks are not an authentication mechanism.

Development checks:

```powershell
go test ./...
go vet ./internal/protocol ./internal/audio ./internal/realtime ./internal/conversation ./internal/speech ./internal/transport/http
node --test examples/browser/*.test.cjs
```

Architecture: [input](docs/realtime-input.md), [interruption](docs/interruption-recovery.md), [speculation](docs/speculative-generation.md), [conversation](docs/conversation-runtime.md), [audio runtime](docs/audio-runtime.md).
