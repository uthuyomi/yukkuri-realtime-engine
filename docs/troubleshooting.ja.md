# トラブルシューティング

[English](troubleshooting.md) | 日本語

まずhealthとcapabilitiesを分けて確認してください。health成功でもproviderの推論成功は保証しません。

```powershell
Invoke-RestMethod http://127.0.0.1:8765/health
Invoke-RestMethod http://127.0.0.1:8765/v1/capabilities | ConvertTo-Json -Depth 8
```

## TTS unavailable / AqKanji2Koe unavailable

起動手順の9声種x64 DLL、変換DLL、aq_dicの配置を確認し、ルートから実行します。起動時TTSエラーにはローカルパスが含まれる場合があるため共有前に伏せます。非公式ミラーのDLLで置き換えないでください。

## STT unavailable

STT_MODEL_PATH／STT_MODEL、方式に対応する実行ファイルと同じ場所の依存DLLを確認します。不正設定でSTTは無効化され、初期化失敗後はEngine再起動が必要です。capabilitiesのruntimeは安全な状態／fallbackだけを返し、パスは返しません。

## CUDA requested but unavailable

CUDA build、driver／Toolkit依存、対応モデルを確認します。cudaは意図的にfallbackせず、autoはCPUを選ぶ場合があります。requested_device、selected_device、fallback_reasonを確認します。nvidia-smiだけでは実推論の確認になりません。

## Smart Turn unavailable / detector busy

8766番の/health、model引数、sidecarプロセスを確認します。native推論は同時1件で、503 busyは上限による拒否です。Engineの設定済みcapabilityは稼働healthを保証しません。

## LLM unavailable / generation_failed

Engineの環境／.envにOPENAI_API_KEYを非公開で設定して再起動します。OPENAI_MODELの利用可能性を確認します。ソースの既定値は利用権の保証ではありません。provider errorは情報を制限しています。issueにキーを貼らないでください。

## Microphone permission / no input

localhost HTTPまたは安全なbrowser contextを使い、許可とdeviceを確認します。「接続」「マイク開始」の順です。Silero/ONNXのCDN要求も確認します。browser／deviceのAECはサーバーの制御外です。

## No playback / audio stalls

ユーザーclickから接続してAudioContextをresumeします。tab／playerと出力音量を確認します。実renderのsource framesだけでcreditを補充するため、停止回避用の偽ACKは禁止です。generation.doneはspeaker完了ではありません。

## WebSocket / origin_rejected

file://でなくHTTP配信を使います。OriginとAPI_ALLOWED_ORIGINSの完全一致を確認し、.env変更後は再起動します。接続先は静的配信8080でなくEngineの8765です。再接続は新sessionで、履歴を復元しません。

## Port already in use

Get-NetTCPConnection -LocalPort 8765,8766,8080 -ErrorAction SilentlyContinueで確認します。停止前に所有プロセスを特定します。sidecarの--port変更にはTURN_DETECTOR_URLの一致が必要です。標準Engineのportは固定です。

## SDK dist missing / blank browser behavior

npm --prefix sdk/typescript ciとnpm --prefix sdk/typescript run buildを実行します。exampleだけでなくルートを配信し、network panelでdist/index.jsとdist/browser.jsの200応答を確認します。

## resource_limit / audio_flow_error

取得した上限、発話長、credit計算、session数を確認します。pause中はPCM保持によりcreditが止まることがあります。不要処理をcancel／closeし、確定user turnを黙って再試行しないでください。

## go vet / race environment

WindowsのAquesTalk native戻り値pointer変換にpossible misuse of unsafe.Pointer警告があります。限定した検査の分割はCONTRIBUTINGを参照してください。ローカルCGO_ENABLED=0かつC compilerなしでは-race不可です。CIにLinux portable-core raceを用意しています。

報告にはOS、Go／Node／Pythonの版、safe capability状態、error code、request／generation ID、再現手順を添えます。キー、.env、録音、会話、専有ファイルを添付しないでください。[SECURITY](../SECURITY.md)、[設定](configuration.ja.md)、[起動手順](quickstart.ja.md)。
