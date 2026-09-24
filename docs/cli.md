# CLI

The `yukkuri` CLI is shipped with the Python SDK. It reuses the SDK's HTTP, WebSocket, error, timeout and WAV handling instead of maintaining another protocol implementation. This avoids new Go client transport code; distribution requires Python 3.11+, not a standalone binary. Speaker/microphone system dependencies are not included.

```powershell
python -m pip install -e ./sdk/python
yukkuri --help
yukkuri health
yukkuri capabilities
yukkuri speak "こんにちは" --output hello.wav
yukkuri transcribe input.wav
yukkuri realtime
```

If Python's Scripts directory isn't on PATH, use `python -m yukkuri_realtime` in place of `yukkuri`. The installed console entry point and module invoke the same function.

URL precedence: explicit `--url` → `YUKKURI_ENGINE_URL` → `http://127.0.0.1:8765`. `--url` is accepted before or after the subcommand. SDK constructors use their explicit/default URL; CLI is the environment-aware layer. No .env loading or credentials are added.

```powershell
yukkuri --url http://127.0.0.1:8765 health
yukkuri speak "こんにちは" --voice f1 --speed 100 --output hello.wav
yukkuri --timeout 150 transcribe input.wav
```

Global `--timeout` (seconds, default 130) goes before the subcommand and controls HTTP/operation waits. Connect and close have the SDK's separate 10s/5s budgets. Ctrl+C exits 130 and async cleanup cancels work; normal success exits 0; file/format/network/API failures exit 1; argparse usage errors exit 2. Errors go to stderr with stable code, safe message and available correlation IDs. No raw provider body or traceback is dumped.

`health`/`capabilities` print JSON. `speak` buffers complete audio and writes a WAV only after validation; PCM16 output can be wrapped with its declared format. Existing output files are replaced only after a successful API response. There is no implicit speaker playback. `transcribe` accepts only complete PCM16 mono 16 kHz WAV, validates/dechunks it, sends PCM to /v1/transcription and prints the final text. Unsupported encoding/rate/channels are rejected, not silently converted. Input is bounded by the server's current 120-second manual-turn contract.

`realtime` accepts lines from stdin, streams text deltas, waits for that generation to finish, then accepts the next turn. EOF closes the session; there is no TUI or microphone/speaker support. A server error exits with its typed error. No reconnect/restoration is attempted.

Tests launch a real Go Public API server with fake STT/TTS/LLM providers; no proprietary files, Whisper model or paid API is needed:

```powershell
python -m unittest discover -s sdk/python/tests -v
```
