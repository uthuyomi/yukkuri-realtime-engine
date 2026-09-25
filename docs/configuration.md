# Configuration reference

English | [日本語](configuration.ja.md)

Source of truth: `cmd/engine/main.go`, `internal/providers/stt/whispercpp/config.go`, and the CLI parser. Engine loads `.env` in the working directory without replacing existing process variables. Empty values select defaults. CLI/SDK do not load `.env`. Every variable below is syntactically optional; conversation requires a key and voice services require their actual executable/model assets. Never paste secrets into logs/issues.

| Name | Purpose / 用途 | Default / 既定値 | Valid values / 条件 | Example / 例 | Sensitivity / 機密性 |
| --- | --- | --- | --- | --- | --- |
| `OPENAI_API_KEY` | External LLM credential / 外部LLM資格情報 | empty / 空 | Nonempty for conversation / 会話に必要 | `(set privately / 非公開で設定)` | secret / 秘密 |
| `OPENAI_MODEL` | LLM model identifier / モデル識別子 | gpt-5.6-luna | Provider account must support it / アカウントで利用可能なもの | `gpt-5.6-luna` | no / なし |
| `STT_DEVICE` | Requested backend / 要求backend | auto | auto, cpu, cuda | `cpu` | no / なし |
| `STT_RUNTIME` | Worker mode / worker方式 | persistent | persistent, process | `persistent` | no / なし |
| `STT_MODEL` | Model filename identifier / モデル名 | small | tiny, base, small, medium, large-v1/v2/v3, large-v3-turbo; optional .en | `small` | no / なし |
| `STT_MODEL_PATH` | Override model path / モデルパス上書き | runtime/whisper/models/ggml-small.bin | Nonempty local path; marks model custom / ローカルパス、表示名custom | `runtime/whisper/models/ggml-small.bin` | local path / ローカルパス |
| `STT_CPU_EXECUTABLE` | CPU worker path / CPU実行ファイル | computed; see below / 下記参照 | Local executable for selected mode / 方式に対応する実行ファイル | `runtime/whisper/cpu/whisper-server.exe` | local path / ローカルパス |
| `STT_CUDA_EXECUTABLE` | CUDA worker path / CUDA実行ファイル | computed; see below / 下記参照 | Local executable for selected mode / 方式に対応する実行ファイル | `runtime/whisper/cuda/whisper-server.exe` | local path / ローカルパス |
| `STT_LANGUAGE` | Recognition language / 認識言語 | ja | auto or 2–3 lowercase letters / 小文字2〜3字 | `ja` | no / なし |
| `STT_THREADS` | Inference threads / 推論スレッド | 4 | 1–128 | `4` | no / なし |
| `STT_BEST_OF` | Search candidates / 探索候補 | 5 | 1–16 | `5` | no / なし |
| `STT_BEAM_SIZE` | Beam search width / beam幅 | 5 | 1–16 | `5` | no / なし |
| `STT_QUEUE_CAPACITY` | Admitted requests including active / 実行中込みの受入数 | 8 | 1–64 | `8` | no / なし |
| `TURN_DETECTOR_URL` | Smart Turn endpoint / 判定先 | http://127.0.0.1:8766/predict | Loopback HTTP only; no credentials/query/fragment / loopback HTTPのみ | `http://127.0.0.1:8766/predict` | local endpoint / ローカル接続先 |
| `TURN_MIN_DELAY` | Earliest endpoint check / 最小待機 | 300ms | Go duration > 0; <= TURN_MAX_DELAY | `300ms` | no / なし |
| `TURN_MAX_DELAY` | Endpoint upper delay / 最大待機 | 2500ms | Go duration >= TURN_MIN_DELAY | `2500ms` | no / なし |
| `TURN_MAX_DURATION` | Continuous input turn bound / 連続入力の上限 | 120s | Go duration 1s–10m | `120s` | no / なし |
| `INTERRUPTION_DECISION_WINDOW` | Interruption decision budget / 割り込み判定猶予 | 1500ms | Go duration 300ms–5s | `1500ms` | no / なし |
| `BACKCHANNEL_ACOUSTIC_RECOVERY` | Allow acoustic recovery heuristic / 音響特徴での復帰を許可 | true | Go ParseBool: true/false, 1/0, t/f, T/F, TRUE/FALSE, True/False | `false` | no / なし |
| `SPECULATION_ENABLED` | Enable bounded preemptive work / 先行生成 | true | Go ParseBool (same as above / 同上) | `true` | no / なし |
| `SPECULATION_TIMEOUT` | Candidate lifetime / 候補の寿命 | 45s | Go duration > 0 and <= 2m | `45s` | no / なし |
| `SPECULATION_COOLDOWN` | Attempt spacing / 試行間隔 | 2s | Go duration >= 0 | `2s` | no / なし |
| `API_ALLOWED_ORIGINS` | Extra exact browser origins / 追加の完全一致Origin | empty / 空 | Comma-separated; no wildcard expansion / カンマ区切り | `http://localhost:8080` | trust boundary / 信頼境界 |
| `YUKKURI_ENGINE_URL` | CLI/example base URL / CLI・exampleの接続先 | http://127.0.0.1:8765 | HTTP(S), no credentials/query/fragment / 資格情報など不可 | `http://127.0.0.1:8765` | endpoint / 接続先 |

## Resolution and startup behavior

`STT_MODEL` selects a filename, not a download. A regex-accepted `.en` identifier does not guarantee such a model exists. Do not choose an English-only model for Japanese. `STT_MODEL_PATH` wins and reports `custom`, never its path, through capabilities.

The default executable is `whisper-server.exe` in persistent mode and `whisper-cli.exe` in process mode. CPU prefers `runtime/whisper/cpu/`, falling back to `runtime/whisper/` if that file does not exist. CUDA uses `runtime/whisper/cuda/`. Overrides must match the chosen mode.

`cpu` never probes GPU; `cuda` never falls back to CPU. `auto` validates CUDA loading/inference and may initialize CPU instead. A CUDA failure during inference fails that request; auto switches on the next request. Device selection never changes the model. Invalid STT configuration disables STT. Invalid turn/interruption/speculation configuration terminates startup.

`YUKKURI_ENGINE_URL` is for CLI/examples, overridden by CLI `--url`; SDKs use their constructor. It is commented in `.env.example` and is not the Engine bind address.

## Configuration that is not an environment variable

The entry point binds `127.0.0.1:8765`. TTS paths, nine voices and default f1 are configured in code. Provider Go config has license-key fields, but no public key environment variables. LLM BaseURL exists only in Go provider config.

Smart Turn CLI requires `--model`; defaults are `--port 8766`, `--threshold 0.5` (strictly between 0 and 1), loopback bind. Whisper setup uses local Toolkit `CUDA_PATH`, not an Engine setting.

Fixed bounds: WS JSON/input binary 65536 bytes each; output binary 16384 bytes; HTTP body 1 MiB; text 32768 bytes; final transcript 16384 bytes; manual input 120 seconds; 64 sockets; 16 workers/socket. STT/TTS provider deadline 2 minutes, write 10 seconds, cleanup 2 seconds, credit wait 30 seconds. Ordinary LLM streaming has no fixed server-wide response deadline.

Conversation defaults: 50 items / 256 KiB, context 20 items / 48 KiB. Speculation: one worker/session, three attempts/turn, 16 KiB transcript, 64 KiB / 1024 deltas. These are Go config or constants, not invented environment variables. See capabilities and [API limits](api.md#limits-and-timeouts) for effective values.

No-Origin native clients, same-origin and HTTP(S) loopback origins are allowed by default. Additional origins are exact matches, without wildcard expansion or credential CORS. Origin is not authentication.

Opt-in test variables: `TURN_DETECTOR_TEST_URL`, `WHISPER_CANCEL_TEST_EXE` / `WHISPER_CANCEL_TEST_MODEL`, `WHISPER_RUNTIME_TEST_EXE` / `WHISPER_RUNTIME_TEST_MODEL`. `YUKKURI_TEST_*` controls internal fake workers, not public configuration.
