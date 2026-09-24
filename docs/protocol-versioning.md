# Protocol versioning

`/v1` and `session.created.data.protocol_version: "1"` identify public protocol major version 1. It is a string. This is the first formal wire contract; prior behavior is retained where safe and specifically listed as compatibility behavior. Provider versions, engine builds and capability versions are separate concepts.

Within v1, additive optional fields, new ignorable server events and new optional capabilities are permitted. Existing field meanings, direction, source-frame units and binary association do not change silently. An incompatible change requires a new major protocol/path. Never interpret server timestamps as playback clocks.

Clients may send `session.configure {protocol_version:"1", audio_flow_control:"credit-v1"}`. Unsupported versions/modes receive unsupported_capability and do not terminate the socket. No configuration message is required for legacy bounded mode. Recommended clients inspect discovery/session capabilities and explicitly select credit-v1 before their first generation. Legacy `playback.configure {flow_control:"credit-v1"}` remains valid.

Unknown server events/fields must be ignored by clients. Unknown client event names receive structured errors. Unknown data fields are generally ignored for additive compatibility; `input_text.commit` deliberately rejects unknown fields to preserve its user-only trust boundary. Missing/`null` data remains accepted for events whose data fields are all optional. The server always emits a data object, even when empty.

Client event IDs are optional ASCII tokens (`A-Z a-z 0-9 _ . : -`, at most 128 bytes). Omitted/empty IDs are accepted. Server event IDs are `evt_` plus 128 random bits encoded as hex: designed to be globally unique probabilistically, not sequential, not timestamps and not replay cursors. Correlation does not imply duplicate suppression. Sending duplicate create/commit requests can create new work; SDK retry policies must account for this.

No reconnect restoration, durable sessions, resume tokens or exactly-once delivery are provided. A new socket receives a new session and (realtime) a new conversation. On connection failure, stale generation/audio belongs to the old socket and must be discarded.

## Machine-readable contract

- [client-events.schema.json](protocol/client-events.schema.json): event names, fields, types, required data and the input_text trust boundary, generated from the validator's field table.
- [server-event.schema.json](protocol/server-event.schema.json): common server envelope, not every runtime data object.

These schemas deliberately describe a structural subset. Runtime state, generation ownership, byte rather than character budgets, sample format enums, credit arithmetic, provider readiness and optional connection-specific restrictions remain in validation/tests and the event catalog. Passing structural JSON Schema does not imply an operation is allowed in the current state. This avoids maintaining a misleading hand-written schema for every internal state transition.

The normal protocol test fails on fixture drift. To intentionally update after a contract change:

```powershell
go test ./internal/protocol -args -update
```

This is a developer test fixture update, not a public CLI or SDK generator. No OpenAPI generator/toolchain was introduced. SDK work should import/use these artifacts together with the event catalog and conformance tests.
