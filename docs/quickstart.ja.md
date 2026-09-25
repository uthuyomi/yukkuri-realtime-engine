# クイックスタート（Windows x64）

[English](quickstart.md) | 日本語 · [README](../README.ja.md)

以下は特記のない限りcloneしたリポジトリルートから実行する**PowerShell**のコマンドです。cmd.exeの`set`とは構文が異なります。既存のSDK・モデルは保持してください。`.env`がある場合はコピーを省略し、内容を共有せず既存ファイルを編集してください。

## 前提

- Git、`go.mod`で指定するGo 1.27.1。`go version`で確認します。
- 固定依存のSmart TurnにはPython 3.13。別パッケージのPython SDKは3.11以上です。
- ブラウザSDKにはNode.js 22以上とnpm。
- whisper.cppのビルドにはCMake、Visual Studio 2022 Build ToolsのC++デスクトップ開発、Windows SDK、x64ターゲット。
- CUDAを使う場合のみNVIDIAドライバー、CUDA Toolkit、対応するMSVC。CPUでは不要です。
- AQUESTから条件に従って別途取得したAquesTalk1 Windows x64とAqKanji2Koe Windows x64。会話には外部LLMの資格情報も必要です。

## ローカルTTS資産

標準起動プログラムは9声種すべてをロードします。設定されたDLLが1つでもないとTTSを無効化します。配布元のファイルをルートからの相対パスで配置します。

```text
internal/providers/tts/aquestalk/aqtk1_win/lib64/<voice>/AquesTalk.dll
  <voice>: f1, f2, f3, m1, m2, r1, dvd, imd1, jgr
internal/providers/tts/aquestalk/aqk2k_win/lib64/AqKanji2Koe.dll
internal/providers/tts/aquestalk/aqk2k_win/aq_dic/  (vendor dictionary files)
```

これらはGitとソース配布から除外します。DLL、辞書、SDKヘッダー／ライブラリ、キーをcommitしないでください。標準起動プログラムにTTSパス／キー用の公開環境変数はありません。Goの`aquestalk.Config`にはキーのフィールドがありますが、標準起動プログラムからは設定していません。[providers](providers.ja.md)を参照し、存在しない`AQUEST_*`変数を追加しないでください。

## STTのビルド、モデル取得、クライアントのビルド

CPUセットアップはwhisper.cppを`d09f61a708f3487afa956ff578e60eae5e7a233c`に固定し、server/CLIをビルドして依存DLLをローカルに配置します。モデルは取得しません。続くコマンドはそのcheckoutの[モデル取得スクリプト](https://github.com/ggml-org/whisper.cpp/blob/d09f61a708f3487afa956ff578e60eae5e7a233c/models/download-ggml-model.cmd)を呼びます。上流の配布元へのネットワーク接続が必要です。検証済みsmallモデルがある場合は再取得せず利用してください。

```powershell
./scripts/setup-whisper.ps1 -Backend cpu
New-Item -ItemType Directory -Force runtime/whisper/models | Out-Null
& ./runtime/whisper/upstream/models/download-ggml-model.cmd small (Resolve-Path runtime/whisper/models).Path
Test-Path runtime/whisper/models/ggml-small.bin
./tools/turn-detector/setup.ps1
npm --prefix sdk/typescript ci
npm --prefix sdk/typescript run build
Copy-Item .env.example .env
```

Smart Turnセットアップは専用virtualenvを作成し、固定モデルのSHA-256を確認します。`.env`の`OPENAI_API_KEY`を非公開で設定し、`OPENAI_MODEL`にはアカウントで利用できるResponses対応モデルを設定します。ソースの既定値は`gpt-5.6-luna`ですが、利用可能性を保証しません。キーが空なら会話は無効です。CPUのみの起動では`STT_DEVICE=cpu`にします。

## 3つのターミナルで起動

```powershell
# Terminal 1 — repository root
./runtime/turn-detection/venv/Scripts/python.exe tools/turn-detector/server.py --model runtime/turn-detection/smart-turn-v3.2-cpu.onnx
```

```powershell
# Terminal 2 — repository root
$env:STT_DEVICE = 'cpu'
$env:STT_MODEL = 'small'
go run ./cmd/engine
```

```powershell
# Terminal 3 — repository root
python -m http.server 8080 --bind 127.0.0.1
```

`Smart Turn ready`と`HTTP server listening on 127.0.0.1:8765`を待ちます。STT起動にはモデルロードと実推論の確認があり、試すbackendごとに最大2分かかる場合があります。起動待ちをWebSocketの故障と混同しないでください。

```powershell
Invoke-RestMethod http://127.0.0.1:8765/health
Invoke-RestMethod http://127.0.0.1:8765/v1/capabilities | ConvertTo-Json -Depth 8
Invoke-RestMethod http://127.0.0.1:8766/health
```

<http://127.0.0.1:8080/examples/typescript/browser-voice/>を開き、「接続」で音声を有効にして「マイク開始」を押し、許可して発話します。入力方式を切り替える前にマイクを停止します（テキストフォームは自動で停止）。「切断」でマイク・再生・socketを解放します。ターミナルはCtrl+Cで終了します。既定のOrigin方針では`file://`を許可しません。

ブラウザはvad-web 0.0.31とonnxruntime-web 1.22.0の資産をCDNから取得します。core SDK自体にML依存はなく、アプリ側で資産を配信する構成も可能です。開発用HTTPサーバーは`.env`やローカル資産も配信できるので、ネットワークへ公開しないでください。

## CUDAを使う場合

```powershell
nvcc --version
./scripts/setup-whisper.ps1 -Backend cuda -CudaArchitectures '75'
$env:STT_DEVICE = 'cuda'
$env:STT_MODEL = 'small'
go run ./cmd/engine
```

GPUに対応するCUDA Toolkit/MSVCの組を使います。`75`はGTX 1660向けの既定ビルド対象で、GPU名の許可リストではありません。`-VCToolset`でインストール済みの対応toolsetを指定できます。CPU/CUDAの依存DLLは別ディレクトリに保ちます。明示的な`cuda`は失敗時にCPUへ切り替えません。`auto`ではCPUへfallbackする場合があります。[STT runtime](stt-runtime.md)も参照してください。GPU検出だけで準備完了と判断しません。

## SDK/CLIとproviderなしの検証

```powershell
python -m venv .venv
./.venv/Scripts/python.exe -m pip install -e ./sdk/python
./.venv/Scripts/python.exe -m yukkuri_realtime health
./.venv/Scripts/python.exe -m yukkuri_realtime capabilities
./.venv/Scripts/python.exe -m yukkuri_realtime realtime
```

最後のコマンドはLLM、`speak`はTTS、`transcribe`はSTTが必要です。healthにはproviderは不要です。API結合テストはfake providerを使うため、有料キー・専有資産・モデルは不要です。[開発手順](../CONTRIBUTING.md)を参照してください。起動手順はソースとproviderなしのビルド／パッケージテストで照合しました。Step 10では外部ダウンロード・CUDA・実マイクのセットアップは再実行していません。
