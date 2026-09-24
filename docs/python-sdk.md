# Python SDK

`yukkuri-realtime` 0.1.0 installs the import package `yukkuri_realtime` and the `yukkuri` console command. Python 3.11+ is required. Async is canonical; no nested-event-loop sync facade is introduced. A CLI wraps the async API for shell users.

## Local installation

```powershell
python -m pip install -e ./sdk/python
python -m unittest discover -s sdk/python/tests -v
python -m pip wheel --no-deps ./sdk/python --wheel-dir sdk/python/dist
```

No PyPI publication. Use a virtualenv for application dependencies. Package data includes `py.typed`. Runtime dependencies are HTTPX (`>=0.28,<1`) for bounded async HTTP and websockets (`>=15,<18`) for async WebSocket transport, close/cancellation and bounded receive queues. There are no ML/audio-device dependencies. References: [HTTPX async API](https://www.python-httpx.org/api/), [timeouts](https://www.python-httpx.org/advanced/timeouts/), [websockets client](https://websockets.readthedocs.io/en/stable/reference/asyncio/client.html). WebSocket proxy auto-discovery is disabled for predictable local Engine connections; pass the reachable Engine/TLS gateway URL explicitly.

```python
import asyncio
from pathlib import Path
from yukkuri_realtime import YukkuriClient

async def main():
    async with YukkuriClient("http://127.0.0.1:8765") as client:
        await client.health()
        audio = await client.speak("ゆっくりしていってね")
        Path("hello.wav").write_bytes(audio.audio)
asyncio.run(main())
```

`speak(text, voice=None, speed=None, provider=None, timeout=None)` returns SpeechAudio with bytes, AudioFormat, content_type and request_id. WAV RIFF length is validated; transport and truncated response errors are not treated as normal completion. Raw PCM has no v1 end checksum/duration, so provider-side silent truncation of that format cannot always be inferred.

## Public API

| API | Meaning |
| --- | --- |
| `health()` | HTTP process health |
| `capabilities(refresh=False)` | Versioned discovery, 30s TTL cache by default |
| `speak(text, ...)` | Standalone HTTP TTS |
| `transcribe(pcm, timeout=None)` | One-shot raw PCM16 mono16k → final Transcript |
| `transcription.connect()` | Independent TranscriptionSession |
| `realtime.connect()` | RealtimeSession, no LLM invocation until input |
| `close()` / async context manager | Close owned sessions and HTTP client |

Both session kinds support `send_audio`, `cancel_input`, `stop_audio_input`, `close`, `on`, advanced `send_event`, and async context management. TranscriptionSession adds `start`, `commit`, `cancel`. RealtimeSession adds `send_text`, `start_audio_input(mode='realtime'|'manual')`, `commit_input`, `cancel_generation`, `ack_played`, `playback_snapshot`.

```python
from yukkuri_realtime import wav_to_pcm
pcm = wav_to_pcm(Path("input.wav").read_bytes())
transcript = await client.transcribe(pcm)
print(transcript.text)

async with await client.transcription.connect() as session:
    await session.start()
    await session.send_audio(pcm)
    transcript = await session.commit()
```

WAV parsing validates the RIFF extent, fmt/data chunks, PCM encoding, channels, sample rate, byte rate, alignment and bit depth. Unsupported WAV is rejected; the SDK doesn't resample it or send headers as PCM. Current server STT is final-only. An empty recognized string is distinct from an invalid empty audio commit. Cancelled STT may take a short time to release its server admission slot.

```python
async with await client.realtime.connect() as session:
    session.on("text_delta", lambda e: print(e["delta"], end="", flush=True))
    session.on("error", lambda e: print(e.code, e.message))
    generation = await session.send_text("札幌について教えて", output="text")
    await generation.wait_done(timeout=130)
```

`send_text` commits a user turn, cannot supply system/developer roles, and resolves after the server assigns a generation ID. `generation.cancel()` scopes cancellation. `wait_done()` returns `done` or `cancelled`, or raises an error; send completion is not speaker completion. SDK emits no reconnect or restore requests.

## Events and playback

Event names use snake_case: connected, transcript, text_delta, text_done, audio, generation_started, generation_done, interruption, backchannel, error, closed, event. `on(name, synchronous_callback)` returns unsubscribe. Parsing/callback invocation preserves wire order; don't block the event loop in callbacks. Application callback exceptions do not corrupt the transport. Schedule application async work explicitly and observe those tasks. `session.events()` is a bounded raw-event async iterator, useful for diagnostics; subscribe before work. Its default 256-event capacity raises resource_limit for a slow consumer rather than growing forever.

Unknown server events are delivered through event and ignored by higher-level handling. Known envelope/binary corruption is protocol_error. response.audio.delta is paired internally with the next binary, verifying bytes, PCM format, source offset, packet sequence, and generation. A missing binary has a timeout and is also detected on disconnect.

Audio callbacks receive AudioPacket. The SDK does not own speakers. On receipt it advances received_source_frames, **never played_source_frames**. A two-second credit-v1 window bounds in-flight output. External playback must report its cumulative native source position:

```python
session.on("audio", external_player.enqueue)
# From the actual renderer's progress notification, marshal into this asyncio loop:
await session.ack_played(cumulative_rendered_source_frames, generation_id)
```

Do not ACK in enqueue or after saving a file. Source frames differ from output device frames when resampling. Generation ID isolation prevents old players ACKing new audio. Without actual playback progress the server blocks at the window and eventually times out; this preserves playback-aware history.

## Errors, cancellation and lifecycle

YukkuriError retains code/message/recoverable and available request_id/event_id/related_event_id/generation_id. HTTP and WS structured errors share this model. Transport exceptions are converted to safe connection_error/connection_closed, timeouts to timeout, malformed protocol to protocol_error. `asyncio.CancelledError` deliberately remains the language-native cancellation signal.

Timeout constructor options are **seconds**: http_timeout=130, connect_timeout=10, close_timeout=5, operation_timeout=130, capabilities_ttl=30. HTTP and operation methods allow per-call timeout. HTTP timeout covers the full buffered request/response. `commit` cancellation/timeout sends input cancel; cancelling send_text before generation acceptance closes the session because no safe scoped ID is yet known. After acceptance call generation.cancel explicitly. Cancelling or timing out wait_done stops waiting only: it does not assert that output was cancelled.

States: connecting → active → closing → closed. Close sends session.close, waits for final metadata within its budget, then closes/cancels local transport resources. A quiet session has no SDK idle timeout. Disconnect is terminal: closed/error and pending-request failure, never automatic reconnect. A new connect creates a fresh server session and conversation. Client async-context exit closes all owned sessions.

Examples: `examples/python/simple_tts.py`, `transcription.py`, `realtime_text.py`. They accept `YUKKURI_ENGINE_URL` explicitly in example code; the SDK itself is configured through the constructor and does not silently read environment files.
