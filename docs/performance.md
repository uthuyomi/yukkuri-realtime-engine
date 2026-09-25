# Performance and measurement semantics

English | [日本語](performance.ja.md)

## Definitions first

Current Browser Voice uses `history.mjs`. It subtracts server RFC3339 timestamps at nanosecond precision before converting to milliseconds; display rounding does not modify stored values. Server event emission time is not an instrumented provider execution boundary.

| UI metric | Current measurement / limitation |
| --- | --- |
| EOT → STT final | `input_audio.turn` state=complete emission → final transcript emission; EOT means server commit notification, not acoustic speech end |
| STT final → LLM first delta | Final transcript emission → first response.text.delta emission; promoted buffered output can differ from actual speculative inference timing |
| LLM first delta → TTS start | Unavailable; no public TTS-start timestamp |
| TTS start → first audio ready | Unavailable; no public TTS-start timestamp |
| Server TTFA | EOT notification → first response.audio.chunk.started notification, emitted after synthesis; not the exact provider-internal first-ready instant |
| Client TTFA | Browser receipt of EOT notification → receipt of first PCM, using performance.now; not a cross-clock subtraction |
| Audible TTFA | Unavailable; PCM receipt/render progress alone does not measure physical audible onset |
| Interruption latency | Matching generation/interruption suspected → confirmed server notifications, not microphone-to-speaker-stop time |
| total | EOT → generation.done/cancel/error notification; text input uses generation.created. Generation end is not playback drain. |

Missing/unmatched/negative-order intervals stay unavailable (`—`), never zero estimates. Generation/session IDs and conversation metadata join turns; speculative notifications do not create ordinary turns. Best-effort metadata loss can leave a turn uncorrelated or pending.

The page retains history in memory only. Server TTFA aggregates include completed measured turns; interrupted/cancelled/failed turns are excluded. Audio `generation.done` alone does not enter the completed aggregate: conversation completion is required. p50/p95 use nearest-rank, with min/max and sample count. A text-only turn has no audio TTFA.

## Initial user-reported real-microphone observation

Hardware: NVIDIA GeForce GTX 1660 6GB. STT: whisper.cpp, CUDA, small, persistent runtime. Four completed turns; one interrupted turn excluded. Reported Server TTFA: **p50 2.11 s, p95 2.40 s, min 2.09 s, max 2.40 s**.

| Turn | Server TTFA | STT | LLM |
| --- | --- | --- | --- |
| 1 | 2.11 s | 1.58 s | 273 ms |
| 2 | 2.40 s | 1.50 s | 479 ms |
| 3 | 2.12 s | 1.39 s | 326 ms |
| 5 | 2.09 s | 1.27 s | 795 ms |

These numbers were supplied by the project owner during release preparation. Raw event traces, exact unrounded values, complete provider/network settings and the original aggregation implementation were not supplied. In particular, nearest-rank applied to the displayed rounded TTFA rows yields p50 2.11 s and p95 2.40 s; an interpolated median would differ. We preserve the reported values rather than manufacture raw samples or TTS timings. The 2.09 s row's rounded components slightly exceed its total; rounding/boundary differences cannot be resolved without raw traces.

This is an extremely small real-microphone demo sample, **not a controlled benchmark**, a latency guarantee or evidence of comparative performance. Step 10 did not independently reproduce it. Network and external LLM/provider behavior affect results. The originally reported Server TTFA label must not be relabeled audible latency; the current browser's notification-based definition above is explicit.

## Reproduction and earlier records

Use [quickstart](quickstart.md), record Engine/SDK commit, CPU/GPU/driver, whisper revision/model hash/device, audio duration, endpoint settings, speculation state, LLM model/provider/network and raw correlated events. Separate warm/cold STT, generation completion and playback completion. Report enough completed samples and exclusions; do not publish transcripts/recordings without permission.

`go run ./cmd/stt-bench -device cuda -mode persistent -model small -durations 2,5,10,30 -repeat 2` measures provider-side fixtures, not end-to-end microphone latency. The earlier [CPU records](stt-performance.md) use silence fixtures and different hardware. Do not combine those statistics with this microphone observation or infer a CPU/CUDA speedup from them.
