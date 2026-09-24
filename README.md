# Yukkuri Realtime Engine

Self-hosted voice engine with AquesTalk output, whisper.cpp transcription and realtime conversation. Public protocol version: **1**.

## Quick start: Engine → SDK / CLI

Start the configured Engine in one terminal:

```powershell
go run ./cmd/engine
```

Install the local Python SDK and CLI (Python 3.11+):

```powershell
python -m pip install -e ./sdk/python
python -m yukkuri_realtime health
python -m yukkuri_realtime speak "こんにちは" --output hello.wav
python -m yukkuri_realtime transcribe input.wav
python -m yukkuri_realtime realtime
```

Or build the TypeScript SDK (Node 22+, no runtime dependencies):

```powershell
cd sdk/typescript
npm ci
npm run build
cd ../..
node examples/typescript/simple-tts/main.mjs
```

In a local app, install the built `sdk/typescript` directory with `npm install /path/to/repo/sdk/typescript`:

```ts
import {YukkuriClient} from '@yukkuri-realtime/client';
const client = new YukkuriClient({baseUrl: 'http://127.0.0.1:8765'});
const audio = await client.speak('ゆっくりしていってね');
const session = await client.realtime.connect();
session.on('textDelta', e => console.log(e.delta));
await (await session.sendText('札幌について教えて')).done;
await session.close();
```

- [TypeScript SDK / browser voice / source-frame ACK](docs/typescript-sdk.md)
- [Python async SDK](docs/python-sdk.md)
- [CLI commands, URL configuration and local installation](docs/cli.md)
- [SDK / CLI implementation and verification report](docs/sdk-cli-finishing.md)

Browser voice: after building the SDK, serve the **repository root** with `python -m http.server 8080 --bind 127.0.0.1`, then open `http://127.0.0.1:8080/examples/typescript/browser-voice/`. No npm/PyPI publication is needed.

- [Public API / HTTP / capabilities / limits](docs/api.md)
- [Realtime WebSocket protocol and complete event catalog](docs/realtime-protocol.md)
- [Transcription-only WebSocket API](docs/transcription-api.md)
- [Errors and correlation](docs/errors.md)
- [Protocol versioning and compatibility](docs/protocol-versioning.md)
- [Client event JSON Schema](docs/protocol/client-events.schema.json)
- [Server envelope JSON Schema](docs/protocol/server-event.schema.json)
- [Public API Finishing implementation report](docs/public-api-finishing.md)

`go run ./cmd/engine` starts the Windows engine on `127.0.0.1:8765`. Configured providers are exposed through `/v1/capabilities`; missing TTS, STT or LLM disables the corresponding service rather than terminating the process. Proprietary AquesTalk files and whisper.cpp/model files must be installed separately for those services. `OPENAI_API_KEY` is optional when conversation is not needed. No keys or paths are exposed by discovery.

The SDK reference browser example owns playback and microphone cleanup. The old raw protocol diagnostic is retained as `examples/browser/legacy-realtime-test.html`; both share the SDK's canonical Worklet. Continuous endpointing requires the configured SmartTurn sidecar. `file://` pages have a null Origin and are rejected by default. For additional browser origins, set `API_ALLOWED_ORIGINS` to a comma-separated list of exact origins. This is a local self-hosted service without authentication; Origin checks are not an authentication mechanism.

Development checks:

```powershell
go test ./...
go vet ./internal/protocol ./internal/audio ./internal/realtime ./internal/conversation ./internal/speech ./internal/transport/http
node --test examples/browser/*.test.cjs
python -m unittest discover -s sdk/python/tests -v
# In sdk/typescript: npm test
```

Architecture: [input](docs/realtime-input.md), [interruption](docs/interruption-recovery.md), [speculation](docs/speculative-generation.md), [conversation](docs/conversation-runtime.md), [audio runtime](docs/audio-runtime.md).
