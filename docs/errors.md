# Error protocol v1

Every server WebSocket error uses the normal envelope. `event_id` is a new server ID; `related_event_id` refers to the optional ID of the client request that caused it. Generation errors also carry `generation_id` when one exists.

```json
{
  "type":"error", "event_id":"evt_...", "session_id":"sess_...",
  "timestamp":"2026-09-24T12:00:00Z", "related_event_id":"client_42",
  "data":{"code":"invalid_state","message":"This operation is not valid in the current state.","recoverable":true}
}
```

`recoverable` describes whether a corrected/retried operation is generally possible. It does not restore a cancelled generation or guarantee a failed provider will recover. For message-size violations and abusive worker saturation, it is false and the socket closes. A generation failure normally cancels that generation while leaving the connection usable.

| Stable code | Meaning | Typical HTTP status |
| --- | --- | --- |
| `invalid_request` | Malformed JSON, unknown client event, bad field/type, empty commit | 400; unknown route 404 / method 405 |
| `invalid_state` | Operation not allowed in current session/input/generation state | 409 if used by HTTP |
| `unsupported_format` | Input must be mono PCM s16le / 16000Hz | 415 if used by HTTP |
| `unsupported_capability` | Unsupported protocol/feature/mode or event on the wrong endpoint | 400 |
| `payload_too_large` | JSON, binary, decoded text or input turn exceeds limit | 413 |
| `resource_limit` | Concurrent sessions/workers, history/text buffer or transcript bound | 429 |
| `provider_unavailable` | Required TTS/STT/LLM is not configured | 503 |
| `transcription_failed` | STT failed or returned invalid result | 502 |
| `generation_failed` | LLM/TTS/response generation failed | 502 |
| `audio_flow_error` | Invalid credit snapshot or audio flow state | 400 |
| `timeout` | Bounded operation deadline expired | 504 |
| `internal_error` | Other unexpected failure | 500 |
| `origin_rejected` | Origin is outside configured policy | 403 |

HTTP error bodies retain the old safe `error` string and add stable fields:

```json
{"error":"The requested service is not configured.","code":"provider_unavailable","message":"The requested service is not configured.","recoverable":true,"request_id":"req_..."}
```

The same `request_id` appears in `X-Request-ID`. IDs are generated independently of content, secrets and incoming request headers. Normal logs include request/session/event/generation identifiers and stage/count metadata; they do not unconditionally include transcripts, prompts, LLM text or PCM. No raw provider error, path, key, response body or stack trace becomes a public error message. Error messages are selected from the public code table; do not parse wording.

## Compatibility aliases

WebSocket errors from old handlers include `legacy_code` when mapped:

| Legacy codes | Stable code |
| --- | --- |
| invalid_event, invalid_generation, invalid_output, invalid_text_delta, invalid_text_input, unknown_event, empty_input_audio | invalid_request |
| invalid_input_audio, invalid_interruption, invalid_vad_event, input_conflict, input_audio_not_started, server_endpointing, generation_not_active | invalid_state |
| stt_not_configured, llm_not_configured, tts_not_configured | provider_unavailable |
| stt_failed | transcription_failed |
| llm_failed, llm_stream_failed, synthesis_failed | generation_failed |
| text_backpressure, conversation_rejected | resource_limit |

Explicit new validation may provide a more specific category, e.g. unsupported_format instead of the old invalid_input_audio. Clients should migrate to `code`; legacy_code is diagnostic compatibility metadata. Error-code normalization and stricter validation are intentional changes in this first formal v1 contract. Existing valid event flows and bounded audio fallback remain supported.

Async events not tied to one request (endpoint decisions, conversation metadata, speculation telemetry) use generation/turn/conversation/speculation IDs for correlation. Transcription-only commit results/errors retain the commit event ID; they are not attributed to whichever request happened to arrive most recently. Client event IDs are correlation tokens, **not idempotency keys**.
