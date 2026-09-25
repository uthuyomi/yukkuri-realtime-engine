# Changelog

This file describes verified capabilities, not fabricated prior release history. Public protocol v1 is separate from the engine/package version.

## [0.1.0] - 2026-09-25

### Added

- Self-hosted Windows voice runtime with HTTP health, capabilities and standalone speech, plus realtime and transcription WebSockets.
- Provider boundaries for TTS, STT, streaming LLM, endpoint detection and backchannel classification.
- Persistent whisper.cpp final-only transcription, CPU/CUDA selection, bounded shared admission and worker lifecycle recovery.
- Smart Turn sidecar, continuous PCM/VAD input, dynamic endpointing, generation cancellation, tentative interruptions, false-interruption/backchannel recovery.
- Bounded speculative STT/LLM generation with formal promotion barriers.
- Multi-turn, playback-aware conversation state; semantic speech chunks; source-frame timelines, credit-v1 and bounded browser playback.
- TypeScript SDK/browser helpers, Python async SDK and CLI, local package smoke tests.
- Japanese Browser Voice conversation/turn latency history and completed-turn aggregates, without persistent storage or estimated audible latency.
- English/Japanese release documentation, contributor/security guidance, CI configuration, release-source audit and repeated-session cleanup regression test.

### Licensing

- Added the standard [MIT License](LICENSE) for original project code and project-authored documentation unless otherwise noted, with Copyright (c) 2026 uthuyomi as explicitly designated by the owner. Third-party components retain their own terms, including proprietary AQUEST assets obtained separately. Local AQUEST history cleanup completed in Step 10-C; Step 10-D replaced advertised GitHub main. No backend/cache deletion is claimed; release/publication remains deferred.

### Corrected during release preparation

- Documented AquesTalk speed as a ratio (`1.0`), matching implementation, instead of invalid `100` examples.
- Removed raw .env parser errors from startup logs because they can quote credential-bearing input.
- Prevented a queued STT request from reloading/invoking a worker during runtime shutdown before its asynchronous cancellation callback runs.
- Removed AqKanji2Koe vendor assets from the Git index while preserving local files; strengthened source packaging checks. Step 10-C subsequently removed the 17 vendor paths from local reachable history; Step 10-D replaced advertised remote main and verified a fresh clone.

### Known limitations

Early release; Windows-only engine entry point; no authentication, durable session or reconnect restoration; final-only STT; heuristic backchannel; unavailable audible/TTS-start metrics; synchronous native TTS and the documented Windows vet warning. See the README and release-quality report. No package/release publication is performed by this preparation.
