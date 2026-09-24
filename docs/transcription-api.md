# WS /v1/transcription

Transcription-only connection: no Conversation Runtime, assistant items, LLM, TTS, playback or speculative generation is created. It shares the STT provider and two-process admission budget with realtime conversation. Current whisper.cpp mode is **final-only, process-per-utterance**; no partial transcript or streaming Whisper is implied.

## Wire flow

```text
connect ws://127.0.0.1:8765/v1/transcription
S → session.created {protocol_version:"1", connection_type:"transcription", capabilities, request_id}
C → input_audio.start {sample_rate:16000, channels:1, encoding:"pcm_s16le"}
S → input_audio.started {sample_rate:16000, channels:1, encoding:"pcm_s16le"}
C → binary PCM chunks, each ≤65536 bytes, even byte length
C → input_audio.commit (optional event_id)
S → input_audio.committed {turn_id, bytes}
S → input_audio.transcript.final {turn_id, text, language}
repeat from input_audio.start for another utterance
```

Example messages:

```json
{"type":"input_audio.start","event_id":"config_1","data":{"sample_rate":16000,"channels":1,"encoding":"pcm_s16le"}}
{"type":"input_audio.commit","event_id":"utterance_1"}
```

Only binary payloads between start and commit belong to the current input buffer. They are raw signed little-endian int16 samples, one channel, no WAV header, base64 or output metadata. Commit transfers that buffer to a single STT job. `input_audio.committed` means accepted for work, not recognized. Both acceptance and the final/error event correlate with `utterance_1` through `related_event_id`; `turn_id` is server-generated.

Optional `mode` is empty or `manual`. Automatic endpointing is not implemented on this endpoint. Send commit after client VAD/recording stops. For server SmartTurn/continuous endpointing use `/v1/realtime`. Unsupported mode/format gives structured error and leaves the connection usable.

## States and cancellation

- Created/idle: accept start; binary or commit is invalid_state.
- Collecting: accept binary, commit, cancel/stop. Duplicate start is invalid_state. Empty audio commit is invalid_request; the collection remains open for more PCM.
- Transcribing: one active job per connection. New start is resource_limit until the worker exits. No queued utterance list grows in memory.
- Final: release the active slot; start a fresh input turn. An empty recognized transcript is a valid final result for nonempty audio.
- `input_audio.cancel` or `input_audio.stop`: discard buffered audio and cancel active transcription, then emit `input_audio.cancelled` with optional active `turn_id`. No late final from the cancelled job is emitted after cancellation acknowledgement. A provider that has not yet exited may briefly keep the slot busy.
- `session.close`: cancel work, bounded cleanup, `session.closed {cleanup_complete}`, normal WebSocket close. Disconnect also cancels work, without promising a final event.

The 120-second PCM bound and 16KiB final transcript bound are separate. Binary message overflow closes the socket; whole-turn overflow discards input and requires a new start. Provider errors are sanitized. Missing STT is represented in capabilities and by provider_unavailable when work is requested; the server/socket can still be opened for discovery.

Supported common events: `ping`, `session.configure` with protocol_version=1, `session.close`. `generation.*`, `input_text.commit`, playback, interruption, realtime VAD events and credit-v1 selection are unsupported on this connection and return unsupported_capability. Unknown event names return invalid_request. The server sends no binary messages on this endpoint.

Future partial transcription would use an additional negotiated feature and separate event; current clients must not wait for partials. `input_audio.transcript.final` is authoritative for this utterance only; no transcript history or reconnect replay is stored.

See [event envelope](realtime-protocol.md), [errors](errors.md), [limits and Origin policy](api.md).
