# SDK / CLI Finishing 実装報告

2026-09-24。Public API v1を利用するTypeScript/Python SDK、CLI、Browser voice helperを実装した。Engineの本番runtimeとwire protocolは変更していない。git commit / push、npm/PyPI publish、GitHub Releaseは行っていない。

## 1. 変更ファイル一覧

| ファイル | 変更 |
| --- | --- |
| `.gitignore` | SDK依存、build、wheel、npm pack生成物を除外 |
| `README.md` | Engine → SDK/CLI Quick Start、docs/example導線 |
| `examples/browser/audio-worklet.js` | SDK canonical Workletをimportする互換URLへ変更 |
| `examples/browser/audio-worklet.test.cjs` | canonical実装を検証する参照先へ変更 |
| `examples/browser/realtime-test.html` | SDK reference exampleと旧診断ページへの案内 |
| `examples/browser/realtime-test.test.cjs` | 旧診断ページ・canonical Workletの回帰試験へ参照変更 |

## 2. 新規ファイル一覧

TypeScript package:

- `sdk/typescript/package.json`, `package-lock.json`, `tsconfig.json`, `README.md`
- `sdk/typescript/src/index.ts`, `types.ts`, `client-events.ts`, `errors.ts`, `audio.ts`, `client.ts`, `session.ts`, `browser.ts`
- `sdk/typescript/src/audio-worklet.js`（旧exampleから移動、同じ実装）
- `sdk/typescript/scripts/assets.mjs`, `wire-types.mjs`, `package-smoke.mjs`
- `sdk/typescript/test/integration.test.mjs`, `protocol.test.mjs`, `browser.test.mjs`

Python package:

- `sdk/python/pyproject.toml`, `README.md`
- `sdk/python/src/yukkuri_realtime/__init__.py`, `__main__.py`, `py.typed`, `models.py`, `audio.py`, `client.py`, `session.py`, `cli.py`
- `sdk/python/tests/test_integration.py`, `test_protocol.py`, `package_smoke.py`

Examples / fixture / docs:

- `examples/browser/legacy-realtime-test.html`（既存raw protocolページを保存）
- `examples/typescript/simple-tts/main.mjs`
- `examples/typescript/transcription/main.mjs`
- `examples/typescript/realtime-text/main.mjs`
- `examples/typescript/browser-voice/index.html`, `main.js`
- `examples/python/simple_tts.py`, `transcription.py`, `realtime_text.py`
- `internal/sdktest/main.go`
- `docs/typescript-sdk.md`, `python-sdk.md`, `cli.md`, `sdk-cli-finishing.md`

## 3. TypeScript SDK architecture

`@yukkuri-realtime/client` 0.1.0。HTTP client、共有Session、RealtimeSession/TranscriptionSession、wire types、typed errors、WAV parserをcoreへ分離した。fetch/WebSocket/AbortControllerを使い、runtime依存は0。buildのみTypeScript 5.9.3をlockした。

ESM + declarationをdistへ出力し、exportsはroot、browser、Worklet assetに限定する。内部wire helperをpackage subpathとして公開しない。client event構造型は既存JSON Schemaから生成し、通常テストでdriftを検出する。server eventはknown unionとunknown envelopeを区別し、schemaが状態機械の完全仕様とは仮定しない。

## 4. TypeScript public API

YukkuriClientのhealth/capabilities/speak/transcribe/realtime.connect/transcription.connectを提供。RealtimeSessionはsendText/startAudioInput/sendAudio/commitInput/cancelInput/stopAudioInput/cancelGeneration/ackPlayed/close。TranscriptionSessionはstart/sendAudio/commit/cancel/close。

Generationはserver ID、done promise、cancelを持つ。通常利用でgeneration.create、metadata/binary pairing、credit arithmeticを要求しない。advancedなsendEvent/raw event/credit snapshotも利用可能。

## 5. Browser / Node差分

coreは共通で、Node 22+の標準fetch/WebSocketを利用する。browser専用AudioContext/getUserMediaはoptional `/browser` entryに限定した。Electron rendererはbrowser、mainはNodeとして使用できる。CJS専用bundleはなく、CJSからはdynamic importを使う。

Nodeはspeakerを所有しないため明示ACK。BrowserはWorklet render progressをACKする。Node用の別protocol/client実装は作っていない。

## 6. Browser Audio helper

BrowserAudioPlayerがcanonical Workletをロードし、generation開始、format、PCM、done、pause/recovery/cancelを橋渡しする。fixed ring、stateful resampler、source accounting、metadata boundsは既存実装のまま。旧exampleも同じSDK source Workletを参照するため、二重保守にならない。

受信PCMはlittle-endianとして明示decode。出力device framesではなくWorkletのnative source progressをACKする。generation.doneでtailをflushし、実drainで総source framesへ一致する。false interruptionはmatching tokenだけresume、confirmed/cancelはunplayed PCM破棄。watchdogを維持した。

## 7. Browser microphone helper

BrowserMicrophone.start/stop/closeとoptional sileroVoiceFactoryを提供。getUserMediaはechoCancellation/noiseSuppression/autoGainControlを指定し、独自AEC/NSは追加していない。

VAD factoryを注入することでcoreはONNX/model/CDNを知らない。exampleだけ既存と同じvad-web 0.0.31 / onnxruntime-web 1.22.0を明示CDNロードする。self-host asset URLも指定可能。Sileroのcontinuous 16kHz frameをPCM16へ変換し、silenceも送る。VAD metadataとの順序を同じqueueで保ち、256KiBでbackpressureを制限する。許可待ち中のstop、起動失敗、切断、overflowでtracksを解放する。

## 8. Python SDK architecture

`yukkuri-realtime` distribution / `yukkuri_realtime` import package。src layout、pyproject、py.typed、明示export。client/session/models/audio/cliに分けた。

HTTPはHTTPX 0.28系以降1未満、WSはwebsockets 15〜17系。保守されているasync libraryでtimeout/cancel/close/receive queueを扱えるため選択した。今回の実行環境はHTTPX 0.28.1 / websockets 16.0。ML/Whisper/ONNX/audio-device libraryはPython SDKへ入れていない。

## 9. Python public API

YukkuriClient.health/capabilities/speak/transcribe、realtime.connect/transcription.connect。Sessionはasync context manager、synchronous callback登録、bounded raw-event async iteratorを提供。RealtimeSessionはsend_text/start_audio_input/send_audio/commit_input/cancel_input/stop_audio_input/cancel_generation/ack_played/close。TranscriptionSessionはstart/send_audio/commit/cancel/close。

SpeechAudio/Transcript/AudioPacket/AudioFormatはdataclass、Capabilities/RealtimeEventは型定義。Generation.wait_done/cancelで世代ID管理を隠す。

## 10. async / sync方針

Pythonはasync canonical。同期APIからイベントループを再入させるwrapperは追加しない。CLIはasyncio.runでSDKを利用する。HTTP/task cancellationは自然なasyncio.CancelledErrorを維持する。callbackは短い同期処理とし、外部async playerはアプリ側でtask/loop連携を管理する。

## 11. CLI architecture

Python SDK上にargparse CLIを実装し、console_scriptsのyukkuriとpython -m yukkuri_realtimeを同じentryへ接続した。別Go clientや別wire parserを増やさない選択。Python環境が必要で、single binary配布ではない。

## 12. CLI commands

health、capabilities、speak TEXT --output FILE、transcribe FILE.wav、realtimeを実装。realtimeはstdin text → streaming textの複数turn。speakはWAV保存のみで、cross-platform speaker依存を加えない。

WAVのRIFF/chunks/PCM16/mono/16kHzを検証してdechunkし、非対応formatを変換したふりをしない。URLは--url → YUKKURI_ENGINE_URL → defaultの優先順。exitは成功0、API/file/network error1、usage2、Ctrl+C130。

## 13. Capabilities handling

HTTP discoveryを30秒TTLでcacheし、refreshを明示提供。socket初回session.createdでもversionとcapabilityを確認する。未設定/未対応versionのTTS/STT/LLM機能はローカルtyped errorで説明する。credit-v1未対応をtext-only接続へ強制しない。optional unknown capability/eventを無視可能にした。

## 14. Error model

両SDKにYukkuriErrorを用意。HTTP/WS共通code/message/recoverableと、取得可能なrequest/event/related/generation IDを保持する。非JSON HTTP body、ネットワーク例外、Go raw provider errorは直接throwしない。local protocol/connection/cancel/timeout/incomplete_audio等を区別した。

## 15. Event model

connected、transcript、textDelta/text_delta、textDone/text_done、audio、generationStarted/generation_started、generationDone/generation_done、interruption、backchannel、error、closed、eventを提供。全低level eventを無理にhigh-levelへ複製しない。

receive順にdispatchし、unknown future eventもraw listenerへ渡す。callbackの例外でparser/pairingを破壊しない。Python raw iteratorは256eventのbounded queueで、遅いconsumerへresource_limitを返す。

## 16. Binary pairing

response.audio.deltaを1件pendingにし、直後binaryのbyte数、PCM bit/channel、source offset、packet順序を検証する。別JSON割込み、unexpected binary、長さ不一致、binary欠落timeout、pending中disconnectをprotocol_errorにする。

stale generationのmetadata/binaryは両方消費して破棄する。新世代へPCMを注入しない。Browser WebSocketはarraybufferを使い、非同期Blob decodeによる順序逆転を避ける。

## 17. Credit-v1 handling

接続設定後、chunk.startedのsource rateから2秒windowを初期化する。receiptはRだけ進め、ACKでPを進める。B=R−PをSDKが計算しsnapshotで送る。加算grantはない。古いACK/世代は無視し、receivedを超えるACKは拒否する。

## 18. Playback ACK semantics

受信・保存・生成完了を再生完了にしない。Browserはrenderしたsource prefix、Node/PythonはackPlayed/ack_playedの明示boundary。外部resamplerのdevice frameをnative source frameへ変換する責務をdocsへ明記した。

ACKが来なければbounded windowで止まり、server timeoutへ至る。この挙動を「動くようにする」ため受信=再生へ変更しない。Playback-aware Historyとfalse-interruptionの意味を保持する。

## 19. Cancellation / timeouts

TSはAbortSignal、Pythonはtask cancellationを利用。HTTP、connect、close、operationの別timeoutを提供。TSはms、Pythonは秒。transcription commit中止はinput cancel。generation受付後はscoped cancelを送り、受付前でID不明の中止はsession closeでworkを止める。

生成完了待ちとspeaker drainは別。closeは再生をdrainせず破棄する。HTTP途中切断と不完全WAVをエラーにし、raw PCMのsilent provider truncationを完全検出できないv1上の限界も記載した。

## 20. Session lifecycle

connecting → active → closing → closed。接続はversion/configure ACK後に返る。closeは冪等でsession.close/closedとbounded transport closeを処理する。切断はpending workを失敗させ、Browser resourcesも解放する。自動reconnect、conversation restore、replayは実装しない。必要なら利用者が新Sessionを作る。

## 21. Package / build

TypeScript: npm ci、npm run build、npm test、npm pack。distにJS/declarations/Workletを含め、npm run test:packageでtarballを別temp applicationへoffline installし、root/browser/Worklet exportsを検証した。

今回生成したnpm tgzは17,066 bytes（約16.7KiB、core/browser/Worklet/型を含む）。Python wheelは13,405 bytes。Silero/ONNX/model assetsはこのサイズに含まず、別途配信する。実ブラウザでのasset download量・初回起動時間は未測定。

Python: pip install -e、pip wheel --no-deps。tests/package_smoke.pyはwheelを別targetへ実際にinstallし、import元、SDK export、CLI entry metadata、py.typedを検証する。build生成物はgitignore。公開処理はない。

## 22. Local install

TypeScriptはbuild済みlocal pathまたはtgzをアプリへnpm install。Pythonはpip install -e ./sdk/python。WindowsでScriptsがPATH外の場合はpython -m yukkuri_realtimeが使える。今回の環境でもこのmodule起動を検証した。

## 23. Examples

Node実行可能なESM例としてsimple-tts/transcription/realtime-text、ブラウザ用browser-voiceを追加。core自体はTypeScriptで型・declarationを提供する。Pythonはsimple_tts/transcription/realtime_text。READMEからbuild/install/runまで辿れる。

ブラウザはリポジトリrootをlocalhost HTTP配信し、SDK distとWorkletへアクセスする。旧HTMLの音声plumbingを利用者にコピーさせず、BrowserAudioPlayer/BrowserMicrophoneの生成だけで接続できるようにした。

## 24. Testsと結果

- TypeScript: `npm test`、20テスト通過。
- Python SDK/CLI: `python -m unittest discover -s sdk/python/tests -v`、13テスト通過。
- 既存browser回帰: 25テスト通過。canonical Workletに対する10分source accounting/ring/resampler試験を含む。
- `go test ./...`通過。既存Protocol/Conversation/Interruption/Speculation/Audioの回帰を含む。
- 新Go fixtureの`go vet ./internal/sdktest`通過。
- npm packageの生成・隔離install・exports smoke通過。
- Python editable install、wheel build・隔離install・SDK/CLI/typing smoke通過。

SDK結合試験は実Go server、real HTTP、real WebSocket、deterministic fake STT/TTS/LLMを使う。追加のmock socket/real Python WS serverは不正orderingや未知eventの注入に使用した。外部課金・proprietary binaries・Whisper modelを試験要件にしない。

| 要求テスト | 対応 |
| --- | --- |
| TS 1〜5 | HTTP health/caps/cache refresh/TTS/structured error/request ID/timeout/AbortSignal |
| TS 6〜12 | real realtime connect/text/audio、version/capability、parsing/schema同期、future event |
| TS 13〜18 | pair/欠落/不正length/unknown binary、世代追跡・active/stale cancel、close/disconnect |
| TS 19〜23 | transcription connect/start/PCM分割/commit/final/cancel、one-shot |
| TS 24〜26 | C/R/P/B snapshot、受信時P=0、explicit ACK、範囲外ACK拒否 |
| TS 27〜30 | canonical Worklet pause/recovery/drain、ring/resampler回帰、mic lifecycle/backpressure、生成開始後AbortSignal |
| Python 1〜6 | health/caps/error/TTS/STT/session/timeout/cancel |
| Python 7〜13 | connect/text delta/audio/pair/source snapshot/明示ACK |
| Python 14〜17 | active generation cancel、close、HTTP/STT timeout、unknown event |
| CLI 1〜10 | help/health/caps/speak保存/transcribe WAV/invalid WAV/unavailable/API error/URL/env優先順位 |

既存全体vetのAquesTalk unsafe.Pointer警告とCGO/race実行制約は前工程の既知事項で、今回そのproviderコードは変更していない。TypeScript/Pythonの自動試験は実音声deviceの検証を代替しない。

## 25. Server側変更の理由

本番server/transport/protocol/runtimeへの変更は0。新規internal/sdktestは既存exported constructor/provider境界を使うテスト用実行ファイルのみ。SDKから新wire eventやrestore機能を要求せず、Public API v1内で完成させた。

## 26. Docs

[TypeScript](typescript-sdk.md)、[Python](python-sdk.md)、[CLI](cli.md)、README Quick Start、本報告を追加・更新。依存理由、callback/async境界、ACK責務、package/local install、asset配置、URL優先順位、time unit、known limitsを明記した。

## 27. 実機確認が必要な項目

実Chrome/Edge/Electronでのautoplay gesture、getUserMedia permission、Silero/ONNX asset取得、マイク・スピーカーの実render、背景タブ、長時間音声、実ネットワーク切断を確認する必要がある。実AquesTalk/Whisper/OpenAI/SmartTurnを通した会話、false interruption/backchannelの音響精度は今回のmock provider試験の範囲外。

Node/Pythonの外部playerは自身のsource accountingを検証すること。Windows以外のpackage/runtime起動、Python 3.11/Node最小対応版そのもののmatrix実行は未実施。今回の環境はWindows、Node22.21.1、Python3.14.3。

## 28. 既知の問題・制約

- Server availabilityはconfigured readinessであり外部providerへのlive probeではない。
- 初回mic利用はONNX/model assetのdownloadとbrowser permissionに依存する。SDK coreへ隠れたCDN依存はない。
- Python CLIはPython環境が必要。Node/Pythonにspeaker再生は含めない。
- HTTP raw PCMのsilent truncationはPublic APIに総frame/checksumがないため完全検出できない。WAV/transport破損は検出する。
- Schemaはstructural subset。state machine/credit算術は実装と契約試験で保証する。
- callbackはreceive loopをブロックしないこと。アプリ側のasync callback/task例外はアプリが扱う。
- キャンセル直後はprovider終了までSTT slotが一時的に使用中になり得る。自動retry/reconnectはしない。
- SDKのversionは0.1.0、wire majorは1。npm/PyPIへは未公開。

## 29. 次工程⑨ STT Runtimeへの引き継ぎ

SDKはSTT provider名・process/model path・CPU/GPUの詳細を知らず、transcription capabilityとPCM16 mono16k/final transcriptへ依存する。CPU/GPU selectionを追加する場合もprovider内部または明示capabilityの追加として扱い、現行clientのmanual start/PCM/commit/final/cancelを維持する。

新providerのtimeout/cancel/empty transcript/admission動作はGo fixture契約とSDK結合試験へ追加する。partialを導入するなら新eventとして追加し、現在のfinal-only commit promiseをpartialで完了させない。今回CPU/GPU selection、Streaming STT、Speculative TTS、WebRTC、DB、auth、tool calling、RAGへは進んでいない。
