# STT performance and reproducible measurements

> Historical STT implementation/CPU measurement record. Statements about unavailable CUDA hardware describe that earlier run, not current project capability. For the later user-reported GTX 1660 real-microphone observation, see [performance](performance.md). Step 10 does not independently reproduce that observation.

## What was actually measured

Windows x64, Intel Core i7-1260P (12 cores / 16 logical processors), Intel Iris Xe, model `ggml-small.bin` (487,601,967 bytes), Japanese (`ja`), 4 inference threads, best-of 5 / beam 5. No NVIDIA GPU, `nvidia-smi`, `nvcc` or CUDA Toolkit was available. Target **GTX 1660 measurements are 未実施**. This is not an RTX3060 run.

Fixture: generated zero PCM16 mono 16kHz, 2/5/10/30 seconds, two repetitions per length. **Latency fixture only, not Japanese speech or accuracy evaluation.** Silence can trigger special Whisper behavior; durations do not imply linear compute, and these figures do not predict real Japanese utterance latency. Machine activity/frequency/cache conditions were not controlled or randomized; especially the baseline had background development/checkout activity. Do not interpret median ratios as a controlled speedup guarantee. No beam/model/language reduction was used to improve the result.

The exact pre-change provider flags were measured **before replacing Engine integration**:

```text
whisper-cli.exe -m <existing-small> -f <generated-wav> -l ja -nt -np
```

The old provider code remains as a comparison path. Installed binary reports `1.9.4-dev`; its exact build commit is unknown. Separate pinned builds use `d09f61a708f3487afa956ff578e60eae5e7a233c`, built locally with VS2022 x64 Release. Different executable provenance is explicitly distinguished below.

## Results (wall seconds / RTF, median of two)

| Input duration | A: old installed CPU process | B: installed CPU persistent | B: pinned CPU persistent | Pinned CPU process (diagnostic flags) |
| --- | --- | --- | --- | --- |
| 2s | 13.001 / 6.500 | 10.569 / 5.285 | 10.597 / 5.299 | 15.879 / 7.939 |
| 5s | 18.351 / 3.670 | 10.826 / 2.165 | 10.075 / 2.015 | 15.163 / 3.033 |
| 10s | 18.123 / 1.812 | 13.287 / 1.329 | 9.716 / 0.972 | 17.227 / 1.723 |
| 30s | 12.628 / 0.421 | 12.133 / 0.404 | 10.205 / 0.340 | 13.481 / 0.449 |

All measured samples, without transcripts/paths:

- [A pre-change CPU records](benchmarks/stt-before-cpu.json)
- [B installed CPU server records](benchmarks/stt-persistent-installed-cpu.json)
- [B pinned CPU server records](benchmarks/stt-persistent-pinned-cpu.json)
- [Pinned CPU process records](benchmarks/stt-process-pinned-cpu.json)

Old baseline total ranged **11.999–24.097 seconds**; pinned persistent total **9.700–10.741 seconds** (rounded). Persistent initialization was **10,218.401ms**, separately recorded: server start/model-ready **792.549ms** plus real availability probe **9,421.238ms** and integration overhead. Each subsequent row is warm-runtime request latency; it does not hide startup in a zero-duration initialization claim. Installed persistent initialization was 11,058.344ms.

The initial exact baseline uses `-np`, which suppresses internal timings. Thus process initialization, model load, inference and finalization are **not separately measurable from that run** and are null. A separate pre-change installed-CLI diagnostic run (2s silence, removed only `-np`) reported:

| Upstream diagnostic | Time |
| --- | --- |
| Model load | 658.86ms |
| Mel | 4.74ms |
| Encoder | 10,253.96ms |
| Decoder | 45.75ms |
| Batch decoder | 861.43ms |
| Sampling | 141.58ms |
| Upstream total | 12,108.21ms |

These are upstream instrumented phases, **not** an exhaustive mutually exclusive wall-time decomposition. Process launch API duration is not process-ready duration. The encoder was a major cost in this diagnostic; model reload alone does not explain the 10–11s complaint. Persistent inference removes repeated model loading and provides a lifecycle boundary, but CPU decoding is still too slow to claim low-latency Japanese conversation is solved.

Peak process working sets: baseline **768.961MiB**, pinned persistent **777.613MiB**, pinned process **769.891MiB** (maximum recorded per series). Persistent peak is the worker's lifetime high-water mark, not a per-request allocation. CPU time is recorded in seconds; process baseline CPU percentage uses one-core=100% (can exceed 100% with multiple threads). It is not total-machine utilization. GPU memory and whole-Engine/session memory were **未実施**. Installed-server early records have null memory/CPU because instrumentation was not yet enabled; no estimates were backfilled.

## Matrix status

| Case | Implementation/tool | Execution status |
| --- | --- | --- |
| A current CPU process-per-utterance | `-mode legacy -device cpu`, exact old flags | Measured before implementation; raw baseline above |
| B CPU persistent | `-mode persistent -device cpu` | Installed and pinned build measured |
| C CUDA process-per-utterance | `-mode process -device cuda` | **未実施**: NVIDIA GPU/Toolkit unavailable |
| D CUDA persistent | `-mode persistent -device cuda` | **未実施**: NVIDIA GPU/Toolkit unavailable |
| E CUDA streaming | No safe incremental implementation enabled | **未実施**: streaming deferred; request for streaming mode fails rather than faking partials |

CPU vs CUDA performance comparison cannot be made. First-partial and streaming finalization latency remain null/unmeasured. Streaming decision: [technical assessment and next implementation conditions](stt-runtime.md#streaming-decision-and-next-implementation).

## Commands

From repository root (PowerShell); binaries/models remain under ignored runtime/. Setup: [CPU / CUDA](stt-runtime.md).

```powershell
# Historical baseline capture with exact flags, Windows memory/CPU sampling:
python ./scripts/stt-baseline.py

# Go tool: JSONL to stdout, content-free diagnostic logging to stderr.
go run ./cmd/stt-bench -mode legacy -device cpu -cpu-executable ./runtime/whisper/whisper-cli.exe -durations 2,5,10,30 -repeat 2
go run ./cmd/stt-bench -mode persistent -device cpu -durations 2,5,10,30 -repeat 2
go run ./cmd/stt-bench -mode process -device cpu -durations 2,5,10,30 -repeat 2
go run ./cmd/stt-bench -mode persistent -device auto -durations 2 -repeat 1

# Run these on the configured GTX1660 host; explicit CUDA never falls back:
go run ./cmd/stt-bench -mode process -device cuda -durations 2,5,10,30 -repeat 2
go run ./cmd/stt-bench -mode persistent -device cuda -durations 2,5,10,30 -repeat 2

# A consented Japanese recording, PCM16/mono/16kHz WAV:
go run ./cmd/stt-bench -mode persistent -device cuda -model small -wav ./runtime/whisper/bench/japanese.wav -repeat 5
```

`-model-path`, `-cpu-executable`, `-cuda-executable` provide explicit local overrides. Benchmark flags are independent of Engine environment; SDK/CLI clients do not select GPUs. `legacy` requires CPU plus a verified CPU-only binary; it deliberately does not add `-ng` to the old flag sequence. New process/CPU runtime always uses `-ng`; new process diagnostic mode enables upstream timings, so it is separately labeled. The default `-durations` generates in-memory silence; `scripts/stt-baseline.py` writes reproducible WAV files under ignored bench/. There is no licensed/user audio committed.

Each JSONL record includes runtime requested/selected device, model, mode, fixture label, duration, repetition, initialization wall time, request wall time, RTF and EOT-to-final. The EOT boundary here is **already-buffered audio submitted to the provider**; it excludes SmartTurn/VAD, microphone and WebSocket/network delay. Use the real Engine manual run for end-to-end EOT.

Measurement fields:

- `runtime_wait_ms`: serialized runtime admission wait; shared transport queue wait is logged separately.
- `runtime_ready_ms`: start through successful server health (includes model load), null for process mode.
- `probe_ms`: actual initialization inference; initialization_ms includes it. Probe is one second of silence, not an accuracy test.
- `model_load_ms`, `encode_ms`, `decode_ms`: upstream numeric timings only where available. Server does not emit those per request; null instead of deriving estimates. Decoder field is not the whole inference cost.
- `process_launch_api_ms`: elapsed Start/Popen API call only.
- `peak_working_set_bytes`, `process_cpu_seconds`: Windows worker instrumentation; null if OS query unavailable. Persistent CPU seconds are request interval delta, process mode CPU seconds cover that CLI child.
- `first_partial_ms`, `finalization_ms`: null; no streaming or isolated finalization measurement exists.

Pinned process artifact was captured before optional startup-phase fields were fixed to retain/null correctly; its unmeasured runtime_ready/probe zero placeholders have been normalized to null. Measured latency/load/encoder/CPU/memory values are unchanged. The current command retains startup probe timing and emits null for absent phases. Original JSONL stays under ignored runtime/whisper/bench/.

The Python baseline measures WAV-write + Popen + process completion polling (10ms interval); it approximates the old provider command lifecycle but is not a Go function microbenchmark. Baseline PCM allocation occurs while writing the fixture; Go benchmark PCM is prepared before timing. Record this harness distinction when comparing totals.

Opt-in installed CPU cancellation smoke (loads the real model, then cancels an inference; no transcript output):

```powershell
$env:WHISPER_RUNTIME_TEST_EXE = (Resolve-Path ./runtime/whisper/cpu/whisper-server.exe).Path
$env:WHISPER_RUNTIME_TEST_MODEL = (Resolve-Path ./runtime/whisper/models/ggml-small.bin).Path
go test ./internal/providers/stt/whispercpp -run TestInstalledPersistentCancellation -count=1 -v
Remove-Item Env:WHISPER_RUNTIME_TEST_EXE, Env:WHISPER_RUNTIME_TEST_MODEL
```

## Japanese manual benchmark / release acceptance

Not executed here. Use consented recordings of approximately 2/5/10/30 seconds: normal conversation, names/numbers, quiet speech, pauses/self-corrections, background noise, backchannels and interruption. Keep model/language/beam/threads equal across CPU/process, CPU/persistent, CUDA/process and CUDA/persistent. Warm up separately, repeat at least five times, alternate ordering, record cold startup, median/tail request latency, EOT-to-final, cancel/reload latency, RAM and GPU VRAM. Do not choose a smaller model silently.

Maintain manually checked Japanese references and score normalized character error rate (document normalization), omissions, punctuation and proper-name errors. The bench tool intentionally does not print recognition text or compute CER; retrieve transcripts through the existing local transcription CLI/API for consented manual scoring. Keep recordings/references/transcripts out of ordinary logs/Git. Persistent vs process transcription must be checked for quality equivalence; silence cannot establish that.

On GTX1660 also test: explicit CPU really stays CPU, CUDA DLL/driver/model failure, insufficient GPU memory, auto CPU fallback, explicit CUDA refusal, two simultaneous sessions, speculative invalidation, disconnect/session.close mid-inference, Engine shutdown, and worker exit with no orphan process. Confirm next-request reload latency after cancel. Preserve model/checksum, upstream revision/build-info, driver/Toolkit version and safe benchmark metadata locally. Release/Quality should not mark GPU performance or Japanese accuracy complete until these runs exist.
