# Providers

English | [日本語](providers.ja.md)

Providers live under `internal/providers/`. STT implements `Transcribe`, LLM `Generate`/stream `Recv`, TTS `Synthesize`, turn detection `Detect`, backchannel `Classify`. Engine registers TTS; transport setters connect the other boundaries. This is a Go composition boundary, not a public dynamic-plugin loader. Availability comes from `/v1/capabilities`; configured does not guarantee that a remote API or sidecar will succeed.

## AquesTalk / AqKanji2Koe

The Windows adapter loads AquesTalk1 DLLs dynamically, converts Japanese text through AqKanji2Koe and returns mono 8 kHz WAV. Default voice f1; configured voices f1/f2/f3/m1/m2/r1/dvd/imd1/jgr. The entry point requires all configured DLLs and the dictionary. See [asset paths](quickstart.md#local-tts-assets).

AquesTalk and AqKanji2Koe are third-party proprietary software. This repository must not redistribute their DLLs, dictionaries, SDK libraries/headers, keys or other restricted SDK assets. Obtain them yourself and comply with AQUEST's terms. The project's [MIT License](../LICENSE) for original code and project-authored documentation grants no rights to those assets and does not change AQUEST licensing. Reviewed source candidates and rewritten local reachable history exclude them. Step 10-D also verified these assets are unreachable from advertised GitHub main after replacement; this does not certify backend/cache erasure; see the [release report](release-quality.md). Consult the vendor's [AquesTalk product page](https://www.a-quest.com/products/aquestalk.html) and [AqKanji2Koe product page](https://www.a-quest.com/products/aqkanji2koe.html), including their licensing links; this document does not determine your legal entitlements.

`speed` is a ratio: omitted/0 → 100%; positive values are multiplied by 100 and converted to integer percent, accepted at 50–300%. Use `1.0`, not `100`, for normal speed. Unknown voice, failed conversion or failed native synthesis becomes a sanitized generation error. The converter output buffer is 8192 bytes; long/complex input may fail conversion. Native calls are synchronous and context cancellation cannot interrupt an in-progress DLL call. DLL behavior/concurrency must be validated under the user's licensed build.

The Go config has `DevKey`, `UsrKey`, `Kanji2KoeDevKey`; the supplied executable does not populate these or expose environment variables for them. No secrets belong in source or artifacts. Missing/invalid assets disable TTS without disabling health or text conversation.

## whisper.cpp STT

The default runtime is persistent, final-only, small model, Japanese. It owns one upstream whisper-server process and serialized inference context, shared across sessions. Process mode uses whisper-cli per request and is a diagnostic/compatibility option, not the default. Pinned build revision and setup are in [STT runtime](stt-runtime.md).

| STT_DEVICE | Behavior |
| --- | --- |
| auto | Probe CUDA loading and real inference; if initialization fails, clean up and try CPU. After an in-request CUDA failure, fail that request and switch on the next one. |
| cpu | Use CPU executable and `-ng`; never try CUDA. |
| cuda | Require CUDA startup, model allocation evidence and inference success. No CPU fallback. |

Device selection never changes the model. `STT_MODEL` selects a known filename pattern, while `STT_MODEL_PATH` supports a supplied custom model. No model is bundled or auto-downloaded at Engine startup. A missing executable/model makes STT unavailable. Cancellation/error kills and waits for the child; the next eligible request reloads it. Initialization failures require Engine restart. See [configuration](configuration.md) for queue/resource bounds.

## Smart Turn

Go `smartturn` calls a loopback-only HTTP sidecar at `http://127.0.0.1:8766/predict` by default, with a three-second HTTP timeout and no redirects. The CPU ONNX sidecar consumes PCM16 mono16k, at most the last eight seconds, and returns a completion probability. It is endpoint detection, not STT. Setup pins model revision/hash and dependencies; the supplied Smart Turn license notice remains in `tools/turn-detector/SMART-TURN-LICENSE`. Downloaded model/dependencies retain their own terms.

Configured capability does not probe the sidecar. If unavailable or busy, bounded endpoint failure/fallback behavior applies; inspect logs and sidecar health. VAD still belongs to the client.

## LLM and backchannel

The sole current LLM implementation is OpenAI Responses SSE (`/responses`), configured by `OPENAI_API_KEY` and `OPENAI_MODEL`. Default source model is `gpt-5.6-luna`; set an available model for your account. This external provider receives conversation content; API charges/network conditions are outside the engine. No key means conversation unavailable. `BaseURL` is a Go config field, not an environment variable. Ordinary LLM calls honor cancellation but have no fixed server total-response deadline.

`multisignal-ja-v1` backchannel is a heuristic using timing, audio evidence, endpoint probability and optional semantic evidence. Acoustic recovery defaults on and can be disabled. It is not a learned intent recognizer; a short correction can be ambiguous. Recovery resumes existing PCM/history, true interruption cancels it.

## Third-party licenses

whisper.cpp, Smart Turn, Silero VAD/vad-web, ONNX Runtime, coder/websocket, SDK/browser dependencies and downloaded models remain subject to their respective licenses and terms. The project's MIT scope does not relicense these components.
