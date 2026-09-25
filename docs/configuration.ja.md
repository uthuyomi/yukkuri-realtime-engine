# 設定リファレンス

[English](configuration.md) | 日本語

設定の根拠は`cmd/engine/main.go`、`internal/providers/stt/whispercpp/config.go`、CLIのparserです。Engineは作業ディレクトリの`.env`を読み、既存の環境変数を上書きしません。空文字列は既定値を選択します。CLIとSDKは`.env`を読みません。以下の変数はすべて構文上任意ですが、会話にはAPIキー、音声機能には該当する実行ファイル・モデルが必要です。秘密をログやissueへ貼らないでください。

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

## 解釈と起動時の動作

`STT_MODEL`はファイル名を選ぶだけでダウンロードしません。正規表現上許される`.en`付きの名前でも、実在するモデルを保証しません。日本語には英語専用モデルを選ばないでください。`STT_MODEL_PATH`は優先され、capabilitiesにはパスではなく`custom`と表示します。

persistentの既定実行名は`whisper-server.exe`、processは`whisper-cli.exe`です。CPUは`runtime/whisper/cpu/`を優先し、そのファイルがなければ`runtime/whisper/`を使います。CUDAは`runtime/whisper/cuda/`を使います。パス上書きで方式の不一致を自動修正しません。

`cpu`はGPUを試さず、`cuda`はCPUへfallbackしません。`auto`はCUDAのロード・実推論を検証後、必要ならCPUへfallbackします。実行中のCUDA失敗は現在の要求を失敗させ、次の要求でCPUへ切り替えます。モデルは自動変更しません。不正STT設定はSTTを無効化します。不正なturn/interruption/speculation設定は起動を停止します。

`YUKKURI_ENGINE_URL`はCLI／examples用です。CLIの`--url`が優先し、SDKはconstructor指定を使用します。`.env.example`内ではコメント例です。Engineの接続待受設定ではありません。

## 環境変数ではない設定

標準Engineの待受は`127.0.0.1:8765`固定です。TTSパス、9声種、既定f1は起動コードの設定です。Goのprovider設定にはライセンスキーの欄がありますが、公開環境変数はありません。LLM BaseURLもGoのprovider設定にのみ存在します。

Smart TurnはCLIで`--model`必須、`--port 8766`、`--threshold 0.5`（0より大きく1未満）を指定します。loopbackにのみbindします。whisperセットアップの`CUDA_PATH`はCUDA Toolkitのローカルパスで、Engine設定ではありません。

固定上限：WS JSON／入力binary各65536 bytes、出力binary 16384 bytes、HTTP body 1 MiB、入力text 32768 bytes、final transcript 16384 bytes、manual入力120秒、64 sockets、16 workers/socket。providerのSTT/TTSは2分、writeは10秒、cleanupは2秒、credit待機30秒です。LLMの通常ストリームにサーバー全体の固定応答deadlineはありません。

会話の既定は50 items／256 KiB、context 20 items／48 KiB。先行生成は1 worker/session、3 attempts/turn、16 KiB transcript、64 KiB／1024 deltasです。これらはGoの設定または固定値で、架空の環境変数は提供していません。実効値はcapabilitiesと[API上限](api.md#limits-and-timeouts)を参照してください。

Originなしのnative client、same-origin、HTTP(S) loopback Originは既定で許可します。追加許可は完全一致、wildcardなし、credential CORSなし。Originは認証ではありません。

テスト専用変数：`TURN_DETECTOR_TEST_URL`、`WHISPER_CANCEL_TEST_EXE`／`WHISPER_CANCEL_TEST_MODEL`、`WHISPER_RUNTIME_TEST_EXE`／`WHISPER_RUNTIME_TEST_MODEL`。`YUKKURI_TEST_*`はfake worker内部用で、公開設定ではありません。
