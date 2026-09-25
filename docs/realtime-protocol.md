# Realtime WebSocket protocol v1

English | [日本語](realtime-protocol.ja.md)

Connect to `/v1/realtime`. The first server event is `session.created`. Internal Turn, Interruption, Speculation, Conversation and Audio Runtime state machines remain independent behind this protocol.

## Envelope, correlation and binary framing

```json
{"type":"generation.created","event_id":"evt_...","session_id":"sess_...","timestamp":"2026-09-24T12:00:00Z","related_event_id":"client_1","generation_id":"gen_...","data":{"voice":"f1","speed":1.0}}
```

Server-required fields: `type` string, `event_id` unique opaque string, `session_id` string, `timestamp` RFC3339 UTC string, `data` object. Optional envelope fields: `generation_id`, `related_event_id`. Turn/conversation/speculation/interruption IDs stay in event data for compatibility. Timestamps are for observability/correlation, never sample-accurate playback.

Client-required envelope field: `type`. `data` is required when the event has required data fields; empty/omitted/null is accepted otherwise. Optional `event_id` enables correlation and is not a retry/idempotency key. Optional `session_id`, if supplied, must match this socket. `generation_id` selects a generation for scoped operations. Clients do not choose server IDs, timestamps or privilege roles.

Direction determines binary meaning:

| Connection | Client binary | Server binary |
| --- | --- | --- |
| /v1/realtime | Raw input PCM under the most recent accepted input_audio.start config | Raw output PCM immediately following matching response.audio.delta metadata |
| /v1/transcription | Raw input PCM between start and commit | Never emitted |

Input binary is not attached to preceding arbitrary JSON and has no per-packet metadata header. Output binary is **one message per immediately preceding response.audio.delta**, whose bytes, format, generation, source offset and sequence describe it. Metadata/binary are indivisible at the writer: another generation/control event never appears between the pair. After reading metadata, consume the corresponding binary even if its generation is stale, then discard both. Do not mistake input PCM or WAV for output PCM.

The limits apply to whole WebSocket messages, including fragmented messages, not only individual network frames. [Limits, liveness, CORS](api.md#limits-and-timeouts).

## Session lifecycle

Public lifecycle: connecting → created → active → closing → closed. This is not a new shared internal runtime enum.

`session.close` stops intake, cancels active generation/transcription/credit waiting/speculation, and gives runtime and transport workers a shared 2-second cleanup budget. It then emits `session.closed {cleanup_complete: true|false}` using a separately bounded write and starts normal WS close (1000). Closing does not drain unfinished speech: unsaid PCM is discarded. The client must clear its old generation. Per-generation done/cancel events are not guaranteed during whole-session shutdown.

Disconnect cancels the same work but cannot guarantee any final event. Server shutdown cancels tracked WebSocket contexts too; HTTP Shutdown alone would not close hijacked sockets. A provider must honor context cancellation. A noncompliant provider can outlive the bounded wait; cleanup_complete=false reports that fact and the connection still closes. No new work is accepted during closing.

## Client → server catalog

All entries below use the common envelope. `?` denotes optional data; `gen` denotes envelope generation_id. Events with no data requirements accept `{}`, null or omitted data. Unknown names receive invalid_request; unrelated future server events must be ignored by clients.

| Category / event | Required fields | Optional fields | State / behavior |
| --- | --- | --- | --- |
| Session: `session.configure` | data.protocol_version string | audio_flow_control=`credit-v1` | Version 1 only; flow selection before any active generation; replies session.configured |
| Session: `session.close` | none | event_id | Any open session; no subsequent work |
| Session: `ping` | none | event_id | Replies pong; distinct from WS protocol ping frames |
| Generation: `generation.create` | none | output=`text`/`audio`, voice string, speed nonnegative number | Default audio. Replaces/cancels current generation. Client supplies response text; does not call LLM |
| Generation: `generation.cancel` | none for legacy | gen | Matching generation cancelled; stale gen ignored; omitted gen cancels current |
| Response Text: `response.text.delta` | data.text string | gen | Feed current client-supplied response generation; stale scoped gen ignored; max decoded text and bounded speech pipeline |
| Response Text: `response.text.done` | none | gen | Ends supplied response; text completion or flushes speech chunks |
| Input Text: `input_text.commit` | data.text non-whitespace string | output=`text`/`audio` | Default text. Commits user turn and invokes LLM. Reject while audio input is active; no role/system/developer fields |
| Input Audio: `input_audio.start` | sample_rate=16000, channels=1, encoding=`pcm_s16le` | mode=`realtime` or empty | Empty mode: legacy manual buffer. Realtime mode requires configured endpoint runtime. Stop realtime input before switching mode |
| Input Audio: `input_audio.commit` | none | event_id | Manual mode only, nonempty PCM. Starts final STT then conversation response. Realtime mode is server committed |
| Input Audio: `input_audio.cancel` | none | event_id | Discard input/cancel obsolete transcription. Continuous mode returns to listening; manual collection ends |
| Input Audio: `input_audio.stop` | none | event_id | Stop input, discard buffer, cancel pending input work |
| Turn: `input_audio.speech_start` | none | gen, interruption_id | Realtime input active; VAD speech start, may suspect interruption of gen |
| Turn: `input_audio.speech_end` | none | event_id | Realtime input active; VAD end, does not itself commit PCM |
| Turn: `input_audio.vad_misfire` | none | event_id | Realtime input active; false-start/recovery metadata |
| Playback: `playback.configure` | flow_control=`credit-v1` | event_id | Legacy spelling of pre-generation credit negotiation; no separate ACK |
| Playback: `playback.credit` | gen; capacity_source_frames, received_source_frames, played_source_frames, buffered_source_frames integers ≥0 | event_id | Generation-specific cumulative snapshot; arithmetic checked; stale generation ignored |
| Playback: `playback.progress` | played_source_frames integer ≥0 OR played_seconds number ≥0 | gen, both positions | Native source frames take precedence; legacy seconds interpreted on source rate. Gen should always be supplied |
| Playback: `playback.paused` | gen, interruption_id; one played position | both played positions | Matching active pause token only; confirms read position, not buffer discard |
| Playback: `playback.overflow` | none | gen, interruption_id | Terminal client audio failure; current generation cancelled, stale gen ignored |
| Interruption: `interruption.suspected` | gen, interruption_id | event_id | Suspect interruption; pause immediately in client, wait for decision |
| Interruption: `interruption.failed` | gen, interruption_id | event_id | Client decision watchdog failed; authoritative cancellation path |

`/v1/transcription` supports only the common session events and manual input start/commit/cancel/stop; see its [separate restrictions](transcription-api.md).

## Server → client catalog

Every row includes the required server envelope. Fields listed here are inside data unless stated otherwise. Additional optional metadata can be added within v1.

| Category / event | Required data | Optional data / envelope | Restrictions / meaning |
| --- | --- | --- | --- |
| Session: `session.created` | protocol_version, capabilities, request_id | realtime: transport, audio_flow_control, legacy_audio_window_ms, conversation_id, interruption_timeout_ms; transcription: connection_type | First event, one per connection |
| Session: `session.configured` | protocol_version | audio_flow_control; related_event_id | Successful explicit configuration |
| Session: `session.closed` | cleanup_complete boolean | related_event_id | Last graceful-shutdown metadata; no replay contract |
| Session: `pong` | empty object | related_event_id | JSON ping response |
| Conversation: `conversation.item.updated` | conversation_id, item_id, role, status, content_bytes, sent_text_bytes, played_chunks, generated_frames, sent_frames, played_frames | turn_id, generation_id; envelope gen | Best-effort metadata only. No transcript, generated text or system prompt in this event |
| Input Audio: `input_audio.started` | sample_rate, channels, encoding | mode, related_event_id | Transcription endpoint accepted config |
| Input Audio: `input_audio.committed` | turn_id, bytes | related_event_id | Transcription endpoint accepted nonempty buffer for STT |
| Input Audio: `input_audio.cancelled` | turn_id (possibly empty) | related_event_id | Transcription cancel/stop acknowledged |
| Turn: `input_audio.turn` | state | turn_id, reason, error | Realtime turn state; error text sanitized, no PCM in JSON |
| Transcription: `input_audio.transcript.final` | text, language, turn_id | related_event_id where a manual commit caused work | Authoritative final utterance. No current partial event |
| Generation: `generation.created` | envelope gen | client supplied: voice, speed; conversation: source, output; promoted: source, speculation_id; related_event_id | Only formal/committed output; server owns gen ID |
| Generation: `generation.done` | object | audio: source_frames, sample_rate; envelope gen | TTS/send pipeline finished, not speaker playback complete. Text-only object is empty |
| Generation: `generation.cancelled` | object | reason; envelope gen | Unplayed output must be discarded; old generation event must not clear new output |
| Response Text: `response.text.delta` | text | envelope gen, related_event_id | LLM output delta; audit/generated text is not necessarily played history |
| Response Text: `response.text.done` | empty object | envelope gen | LLM stream ended; TTS/audio may remain |
| Response Audio: `response.audio.chunk.started` | sequence, text, codec, sample_rate, channels, bits_per_sample | envelope gen | One semantic speech chunk, format known before credit wait; text is normalized TTS text |
| Response Audio: `response.audio.delta` | speech_sequence, audio_sequence, bytes, source_frames, source_start_frame, sample_rate, channels, bits_per_sample | envelope gen | Immediately followed by exactly one binary PCM message |
| Response Audio: `response.audio.chunk.done` | sequence, bytes | envelope gen | Chunk PCM sent; may still be buffered/paused at client |
| Interruption: `interruption.suspected` | interruption_id, state, reason, playback | turn_id, decision, error; envelope gen | Server has entered tentative pause |
| Interruption: `interruption.recovered` | interruption_id, state, reason, playback | turn_id, decision, error; envelope gen | Matching client resumes preserved read position |
| Interruption: `interruption.confirmed` | interruption_id, state, reason, playback | turn_id, decision, error; envelope gen | Discard unplayed PCM; normally followed by generation.cancelled |
| Backchannel: `input_audio.backchannel` | interruption_id, state, reason, playback | turn_id, decision; envelope gen | Recovery classified as backchannel; no new user/assistant history turn |
| Speculation: `speculation.started` | speculation_id, turn_id, revision, state, stage, duration_ms | reason and timing fields | Background candidate STT; not output permission |
| Speculation: `speculation.ready` | same common fields | stt_duration_ms, llm_first_delta_ms | STT or buffered LLM ready, still not committed speech |
| Speculation: `speculation.promoted` | same common fields | generation_id, saved_ms; envelope gen | Official generation adopts valid candidate |
| Speculation: `speculation.invalidated` | same common fields | reason, wasted_ms | Candidate invalidated, cannot publish stale text/PCM |
| Speculation: `speculation.cancelled` | same common fields | reason, wasted_ms | Candidate cancelled |
| Speculation: `speculation.fallback` | same common fields | reason, generation_id, wasted_ms | Optimization failed; ordinary committed path may continue |
| Error: `error` | code, message, recoverable | legacy_code; related_event_id, gen | [Stable errors](errors.md); not raw Go/provider error |

Speculation timing fields, saved_ms/wasted_ms and stage/reason are observability, not commitments or reliable replay events. `input_audio.turn.state` values: idle, listening, speaking, possible_end, waiting, complete. Conversation status: pending, committed, completed, interrupted, cancelled. The interruption `playback` object preserves its historical field names (`GenerationID`, `SampleRate`, `Channels`, `GeneratedFrames`, `SentFrames`, `PlayedFrames`, `Paused`, `PausePlayedFrames`, `StartedAt`, `UpdatedAt`) for compatibility. All its frame counts and SampleRate are source PCM, not AudioContext frames.

Worklet-local `playback.underrun`, `playback.completed`, `format`, `audio`, `pause`, `resume`, `clear`, `done` are **not WebSocket client requests**. The sample forwards native progress/credit and exposes local metrics. SDKs must not invent wire events from internal MessagePort names.

## Generation and conversation ownership

There is at most one active output generation per realtime session. Every create gets a fresh server-generated ID. A duplicate create is another replacement, even if client event_id repeats. Replacement cancels the old context, unblocks credit wait, freezes playback-aware history and resets generation-scoped audio state. A stale scoped cancel is ignored. Omitted cancel ID is a legacy convenience; SDKs should always supply it.

`generation.create` plus client `response.text.delta/done` is client-supplied response/TTS generation. It never invokes LLM and does not commit a user conversation turn. Its orphan assistant does not become a fictitious user exchange in subsequent LLM context.

`input_text.commit` commits user text exactly once for that accepted turn and invokes the configured LLM with server-owned history/system instructions. Default output=text, unlike generation.create's default audio. It does not permit client system/developer roles, messages arrays or custom system prompts. Active microphone input must be stopped/cancelled before committing text.

For voice input, manual commit or SmartTurn endpoint → final STT → committed user turn → formal response generation. Speculative work cannot publish text/audio before that commit. False interruption resumes the same generation/item; true interruption includes only fully confirmed played semantic chunks in the next context.

## Credit-v1 formal contract

1. Negotiate `session.configure {protocol_version:"1",audio_flow_control:"credit-v1"}` (ACK session.configured), or legacy playback.configure.
2. Server creates formal generation. Initial source credit is zero in negotiated mode.
3. `response.audio.chunk.started` provides source format before PCM. Client computes capacity and sends an initial playback.credit with received=played=buffered=0.
4. Server reserves source frames within the window, then sends response.audio.delta + binary atomically.
5. Client renders/resamples, periodically reports cumulative received and played source positions. Pause retains data/read position; receipt while paused does not replenish playback credit.
6. After all chunks are sent, generation.done carries total source frames. Flush resampler tail, drain preserved PCM, then report exact final played_source_frames.

For each generation, capacity C, received R, played P, buffered B are absolute source-frame snapshot values. Enforce `B = R - P`, `0 ≤ P ≤ R ≤ server_reserved`. C is between 100ms and 30 seconds at the source rate. Server send allowance is `max(0, P + C - server_reserved)`; reserved includes network and MessagePort data in flight. **This is not an additive grant.** Duplicate snapshots cannot create more credit. Older received/played positions are ignored, including for history advancement.

Credit waits hold neither Session nor writer mutex, and cancel/replace/disconnect/30s timeout release them. Control events do not consume audio credit. Source frame rates and output hardware rates must not be mixed: use the generation resampler's native source accounting and reach exactly generation.done.source_frames after drain.

Without negotiation, legacy bounded mode supplies an initial two-second window and replenishes only confirmed playback. A client that never ACKs cannot receive unlimited audio. Recommended SDK mode is credit-v1; do not implement unbounded fallback.

## Example flows

### Text → text

```text
C input_text.commit {text:"こんにちは", output:"text"}
S generation.created {source:"text",output:"text"}, gen=G
S response.text.delta {text:"…"}, gen=G (zero or more)
S response.text.done, gen=G
S generation.done, gen=G
S conversation.item.updated ... completed
```

Conversation metadata may interleave and is not a required ordering barrier.

### Text → audio / simple supplied response

```text
C session.configure {protocol_version:"1",audio_flow_control:"credit-v1"}
S session.configured
C input_text.commit {text:"天気を教えて",output:"audio"}
# Or generation.create {output:"audio"}, followed by supplied response.text.delta/done; no LLM then.
S generation.created, gen=G
S response.audio.chunk.started (after LLM text chunking, if applicable)
C playback.credit ... gen=G
S response.audio.delta; binary PCM (repeat)
S generation.done {source_frames:N,...}
C playback.credit {played_source_frames:N,...} after actual drain
```

### Mic → AI → audio

```text
C input_audio.start {mode:"realtime",sample_rate:16000,channels:1,encoding:"pcm_s16le"}
C binary frames continuously (including speech-end silence)
C input_audio.speech_start
C input_audio.speech_end
S input_audio.turn possible_end / waiting / complete, governed by SmartTurn + endpoint delays
S input_audio.transcript.final
S generation.created ... then text/audio response flow above
```

### Interruption and backchannel recovery

```text
client pauses Worklet immediately, retaining G's buffer/read index
C input_audio.speech_start {interruption_id:"pause_1"}, gen=G
C playback.paused {interruption_id:"pause_1",played_source_frames:P}, gen=G
C continuous PCM + input_audio.speech_end
S interruption.recovered {interruption_id:"pause_1"}, gen=G
client resumes only matching G/pause_1; optional input_audio.backchannel follows
# True interruption instead:
S interruption.confirmed, gen=G
S generation.cancelled, gen=G
client discards G; next committed user turn can produce a new generation
```

Independent `interruption.suspected` is available when a new generation arrives during speech. Mismatched/stale decisions never resume or clear a new generation. A client decision watchdog sends interruption.failed; it must not stay paused forever.

For mic → transcript without conversation, use [transcription-only flow](transcription-api.md). For curl TTS, see [HTTP API](api.md).
