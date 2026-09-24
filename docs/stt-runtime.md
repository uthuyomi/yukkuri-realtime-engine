# STT Runtime

The Engine defaults to **persistent whisper.cpp, final-only, `STT_DEVICE=auto`, small, Japanese**. SDK/CLI device selection is deliberately absent. `Provider.Transcribe(ctx, Request)` remains compatible; no partial transcript is manufactured.

## Architecture and choice

```text
Engine startup -> device initialization -> model load -> one real probe inference
                                                   -> shared runtime ready
WS transcription / realtime / speculation -> shared bounded admission
                                         -> serialized upstream /inference
                                         -> authoritative final (unless cancelled)
Engine shutdown -> cancel sessions/work -> kill + wait worker -> release job handle
```

The integration uses upstream **whisper-server**, with no custom inference engine or worker wire protocol. Investigated/pinned revision: `d09f61a708f3487afa956ff578e60eae5e7a233c`. Its server loads one model before `/health`, serializes inference with a mutex, and accepts multipart WAV at `/inference`. The provider connects to a child on loopback, on a selected ephemeral port with a random private request prefix. It never calls `/load`, enables ffmpeg conversion, or serves application files. The prefix is an isolation measure, not an authentication system; this local worker is not a public endpoint.

| Approach | Decision |
| --- | --- |
| Native whisper C API | Model reuse and abort hooks are possible, but requires maintained C ABI/CGO bindings and couples native failures to the Engine. Current Go Windows build uses CGO=0. Deferred. |
| Upstream persistent HTTP server | Selected: mature inference, CPU/CUDA binaries and DLL distribution, model reuse, process isolation; same provider boundary. |
| New custom worker protocol | Unnecessary given upstream server. Not implemented. |
| SDL whisper-stream executable | Investigated; overlapping-window full inference and SDL capture are not a host-PCM incremental decoding boundary. See streaming below. |

Source: [pinned server](https://github.com/ggml-org/whisper.cpp/blob/d09f61a708f3487afa956ff578e60eae5e7a233c/examples/server/server.cpp), [native API](https://github.com/ggml-org/whisper.cpp/blob/d09f61a708f3487afa956ff578e60eae5e7a233c/include/whisper.h).

## CPU setup (Windows PowerShell)

Prerequisites: Git, CMake, Visual Studio 2022 Build Tools with Desktop development with C++, Windows SDK; x64 build. The script does not install drivers, toolkits or models.

```powershell
./scripts/setup-whisper.ps1 -Backend cpu
# Keep the existing runtime/whisper/models/ggml-small.bin.
$env:STT_DEVICE = 'cpu'
$env:STT_MODEL = 'small'
go run ./cmd/engine
```

`GGML_CUDA=OFF`, `GGML_NATIVE=OFF`, `BUILD_SHARED_LIBS=ON`, Release; builds only whisper-server/whisper-cli targets and dependencies. `runtime/whisper/cpu/` holds exe + ggml/whisper DLLs and build-info.json. Default executable resolution prefers that directory and falls back to the existing `runtime/whisper/whisper-server.exe` (or CLI in process mode). Models retain their old directory. A missing server is an unavailable STT service, not an implicit switch to per-utterance CLI.

CPU runtime was built and exercised on Windows x64 in this stage. Other CPU architectures require their own upstream build/validation.

## CUDA setup and GTX 1660

GTX 1660 is the target, not RTX 3060. It is a Turing compute-capability 7.5 device; the setup script's **distribution build target** defaults to `75`. This is not a device-name allowlist. Other GPUs can use an appropriate configurable architecture list, for example `-CudaArchitectures '75;86'`, supported by the installed Toolkit. Selection never tests the GPU model string. [NVIDIA confirmation of GTX 1660 capability](https://forums.developer.nvidia.com/t/backwards-compatibility-with-new-cards-and-older-cuda-versions/112247).

Install a compatible NVIDIA driver, CUDA Toolkit (12.8 is the documented toolchain example), and the MSVC toolset supported by that Toolkit. NVIDIA's CUDA 12.8 Windows guide lists MSVC 193x / VS2022; a newer default VS compiler may require installing/selecting a supported older toolset. Do not bypass checks with `allow-unsupported-compiler`. Use `-VCToolset` if needed. `nvcc` must be on PATH and `CUDA_PATH` must identify that installation. See [NVIDIA Windows installation guide](https://docs.nvidia.com/cuda/archive/12.8.0/cuda-installation-guide-microsoft-windows/index.html).

```powershell
nvcc --version
./scripts/setup-whisper.ps1 -Backend cuda -CudaArchitectures '75' -VCToolset '14.39'
$env:STT_DEVICE = 'cuda'
$env:STT_MODEL = 'small'
go run ./cmd/stt-bench -device cuda -mode persistent -durations 2,5,10,30 -repeat 2
go run ./cmd/engine
```

The script uses `GGML_CUDA=ON`, `CMAKE_CUDA_ARCHITECTURES=75`, Windows x64 Release shared libraries. It places whisper/ggml DLLs and `cudart64_*.dll`, `cublas64_*.dll`, `cublasLt64_*.dll` from that Toolkit in `runtime/whisper/cuda/`. Driver DLLs come from the NVIDIA driver; don't copy them from arbitrary sources. The Microsoft VC++ runtime must also be installed. CPU and CUDA DLL directories must remain separate; do not replace the CPU fallback with a CUDA-linked binary. DLL dependencies/version compatibility must be validated on the target machine. Copying local dependencies is not a license grant to redistribute NVIDIA files; release packaging must review the relevant redistribution terms. [Upstream CUDA build/link configuration](https://github.com/ggml-org/whisper.cpp/blob/d09f61a708f3487afa956ff578e60eae5e7a233c/ggml/src/ggml-cuda/CMakeLists.txt).

**GTX 1660/CUDA build and real inference remain unverified here:** the execution machine exposes Intel Iris Xe, with neither NVIDIA device nor Toolkit. Unit tests with fake CUDA evidence are not hardware validation. No CUDA performance claim is made.

All binaries, sources under runtime/, build outputs and models are ignored by Git. Setup never downloads/replaces the model, edits AquesTalk files, publishes a package or creates a release.

## Configuration

| Variable | Default / meaning |
| --- | --- |
| `STT_DEVICE` | `auto`, `cpu`, `cuda` only |
| `STT_RUNTIME` | `persistent`; `process` for controlled per-utterance comparison/debugging |
| `STT_MODEL` | `small`; tiny/base/small/medium/large-v1/v2/v3/large-v3-turbo identifiers, `.en` where model exists |
| `STT_MODEL_PATH` | Explicit file override; public/log model label becomes `custom` |
| `STT_LANGUAGE` | `ja` (existing behavior); `auto` or upstream supported language code, independent of device |
| `STT_THREADS` | 4, range 1–128 |
| `STT_BEST_OF` / `STT_BEAM_SIZE` | 5 / 5, range 1–16; explicitly preserve old CLI defaults because upstream server defaults differ |
| `STT_QUEUE_CAPACITY` | 8 admitted ordinary requests including the active one, 1–64 |
| `STT_CPU_EXECUTABLE` / `STT_CUDA_EXECUTABLE` | Server or CLI path overrides for the selected runtime mode; no public exposure |

Device selection never changes model, language, search parameters or context. Supported identifiers are filename selection, not model download/availability promises. Invalid config disables STT safely; health/TTS/text-only services can still operate.

## Device semantics and failure handling

- **cpu**: invokes `-ng`, including the initialization probe; never attempts a CUDA executable.
- **cuda**: requires the configured CUDA executable/dependencies, successful startup/model load, upstream evidence of CUDA backend initialization **and CUDA model allocation**, and a real inference probe. Silent upstream CPU fallback is rejected. Initialization or subsequent failure never changes this request to CPU.
- **auto**: currently evaluates the supported CUDA backend, then CPU. It is not Vulkan/DirectML/Intel GPU support. CUDA missing, init failure, model incompatibility, inference-probe failure or timeout cause cleanup followed by CPU initialization with its own budget. `nvidia-smi`, GPU name, or `use_gpu=1` alone is never sufficient.

Safe fallback metadata is `fallback_from=cuda`, `fallback_reason=cuda_initialization_failed` or `cuda_runtime_failed`. A CUDA worker failure during an actual request fails that request; auto changes to CPU on the **next** request, after reaping the failed worker. There is no hidden retry producing two finals. CPU worker failures reload CPU on the next request. No infinite retry loop. Startup failures stay unavailable until Engine restart; failure after initial readiness can be retried by a subsequent raw protocol request. SDK capability caches may need refreshing/reconnecting after availability changes.

Backend evidence parsing is tied to the researched upstream diagnostic vocabulary and fails closed if a future build changes it. Readiness tests actual model/device execution, not just file existence. Startup logs contain only backend, requested/selected device, safe model identifier, persistent mode and safe fallback category. Discovery intentionally omits GPU model/identity, paths, commands and raw errors.

## Lifecycle, concurrency and cancellation

One Engine owns one worker/model/context, shared across sessions. No session-local model. Persistent successful requests reuse it; process mode intentionally loads per utterance, plus one startup availability probe. Init allowance is two minutes **per attempted backend** (auto can therefore take two attempts); each transcription has a two-minute total timeout including runtime wait/reload. Public provider timeout remains two minutes including transport admission.

The shared admission wrapper advertises the actual inference concurrency: **1 for this runtime**, versus the old generic provider limit of 2. `limits.concurrent_stt` is authoritative. `limits.stt_admitted_requests` exposes the bounded admission capacity. Speculation uses nonblocking admission and skips when busy; it does not create another worker or queue behind committed work. Normal requests wait context-cancellably within the bounded queue. The runtime also has its own bounded queue for direct callers outside the transport wrapper.

The final result is discarded if its context is cancelled, even if the worker returns success. Existing input cancellation, turn/speculation revision invalidation, session.close and disconnect flow through the same context boundary. Partials cannot create conversation items or generations because none are generated. Existing final commit/barge-in/playback barriers remain unchanged.

On cancellation/error the provider kills and waits for the worker before releasing its inference slot. This intentionally works even with older distributed upstream servers that cannot reliably abort HTTP inference on disconnect. The next request reloads/probes one worker; cancellation therefore loses warm-model reuse and can increase the next latency. No cancelled request is replayed. Pinned upstream has a connection-closed abort callback, but this integration uses conservative process cleanup rather than depending on that alone.

Engine shutdown cancels sessions and pending STT; runtime Close cancels direct callers, waits for the current worker to be reaped and releases it. On Windows, hidden workers are assigned to a kill-on-job-close Job Object with a one-process limit; assignment failure is an availability failure. Process creation and assignment have a brief OS launch interval before job ownership is established. Normal cancellation/crash/shutdown paths are exercised with real child processes. A worker crash fails requests safely, not the Engine. There is no recursive restart supervisor or reconnect restoration.

## Bounds and privacy

| Resource | Bound |
| --- | --- |
| Model/context/child process | 1 per runtime; failed CUDA is closed before CPU starts |
| Active inference | 1 |
| Runtime/admission capacity | default 8, configurable 1–64; includes active ordinary requests |
| Provider PCM | mono PCM s16le 16kHz, nonempty/even; max 19,200,000 bytes (600s continuous ceiling) |
| Public manual PCM | Existing 120s bound; existing WS binary/JSON limits unchanged |
| Multipart allocation | One bounded request copy; no persistent-mode temporary WAV |
| Provider JSON/text response | 64KiB before decoding; existing public final text limit 16KiB applies afterwards |
| stderr fragment buffer | 4096 bytes; only numeric timings/backend evidence retained |
| Partial queue | None; streaming unavailable |
| Timeouts | 120s init per backend, 120s inference including runtime admission; transport/provider and session cleanup limits remain documented in API |

Worker stdout is discarded in persistent mode. Raw stderr/provider error/command/path is never forwarded to regular logs or public errors. CLI mode's bounded stdout is used only as the transcript. Benchmark output contains timings, safe metadata and measurements, not transcript/audio. HTTP failure maps to `provider_unavailable`, `resource_limit`, `timeout`, or `transcription_failed`, preserving event/turn/request correlation in the existing transport.

Observability: admission queue wait, runtime slot wait, request-to-final time, cancellation flag, device selection/fallback and lifecycle state. `Measurement()` provides the last serialized runtime measurement. Windows peak working set is process-lifetime peak (sampled every 20ms); CPU time for server inference is the process CPU delta during that request. Neither is whole-system/GPU memory. Unavailable metrics are null. See [performance and benchmark](stt-performance.md).

## Public API / SDK compatibility

Protocol remains **v1**. No event names, final transcript payload, commit promises, or input PCM framing change. Both `/v1/transcription` and realtime use the same runtime. TTS, LLM, conversation/history, SmartTurn/EOT, interruption/recovery, speculation commit barrier, credit-v1 and source-frame accounting stay independent.

`features.transcription` retains version/available/modes and gains optional `runtime` metadata:

```json
{"version":"1","available":true,"modes":["commit","final-only"],"runtime":{"backend":"whisper.cpp","requested_device":"auto","selected_device":"cpu","model":"small","persistent":true,"state":"ready","fallback_from":"cuda","fallback_reason":"cuda_initialization_failed"}}
```

States include initializing, ready, reload_required, unavailable and closed. `available` signals initial usable service / retry eligibility, not a promise that a worker cannot fail immediately afterwards; reload_required can remain available for lazy recovery. TS/Python SDKs add optional typed discovery metadata only. `yukkuri transcribe` is unchanged. No partial event is added, and final-only clients need no opt-in or change.

## Streaming decision and next implementation

No streaming capability or partial event is advertised. [Pinned whisper-stream](https://github.com/ggml-org/whisper.cpp/blob/d09f61a708f3487afa956ff578e60eae5e7a233c/examples/stream/stream.cpp) captures via SDL and repeatedly runs full inference on overlapping windows. The selected upstream HTTP server accepts complete audio and produces a completed result; it does not provide an incremental host-PCM decode stream with stable revision/cancellation semantics. Repeatedly calling final Transcribe and calling the results partial would violate this stage's requirement. The measured CPU encoder cost also greatly exceeds the sample's subsecond update cadence.

A follow-up can evaluate a native/isolated upstream-supported streaming bridge with PCM ingestion, bounded revision events and abort callbacks, or an evaluated Japanese streaming model. Before enabling it: measure Japanese accuracy and first-partial/final latency on GTX1660 and CPU; define authoritative-final and revision boundaries; cap partial queues and speculative attempts/cooldown; reuse host PCM buffers; add optional StreamingProvider, typed v1 partial event, and tests proving no partial commits a user item/generation/audio. SmartTurn must remain EOT only. Existing speculative snapshot-STT optimization remains, with its original attempts/lifetime/buffer limits and shared single-inference admission; no new partial-driven LLM work is introduced.

Whisper was not replaced. SenseVoice/sherpa-onnx, Moonshine Japanese or Qwen ASR have not been benchmarked here, and no unsupported accuracy/license comparison is claimed.
