# Public API Finishing / Realtime Protocol Stabilization 実装報告

2026-09-24。指定された29項目の報告。既存の内部runtimeを統合し直さず、公開wire境界を追加・整理した。

## 1. 変更ファイル一覧

| ファイル | 主な変更 |
| --- | --- |
| `cmd/engine/main.go` | TTS/STT/LLM未設定・初期化失敗時の限定機能起動、追加Origin設定 |
| `examples/browser/realtime-test.html` | 正式error codeとlegacy_codeの双方に対応 |
| `internal/audio/flow.go` | credit timeoutの識別可能なsentinel error |
| `internal/engine/engine.go` | 設定情報を漏らさないTTS availability照会 |
| `internal/realtime/event.go` | 共有Envelope/Input formatへの互換alias |
| `internal/realtime/input.go` | legacy input cancel/stopでも入力・進行中STTを破棄 |
| `internal/realtime/session.go` | bounded legacy PCM、上限付きclose待機 |
| `internal/transport/http/conversation.go` | 必要providerの公開境界での確認 |
| `internal/transport/http/input.go` | worker追跡、turn/interruption error秘匿 |
| `internal/transport/http/realtime.go` | version/capabilities、検証、correlation、graceful close、エラー整理 |
| `internal/transport/http/realtime_writer.go` | requestごとのscoped writer、共有atomic lock、write timeout、worker group |
| `internal/transport/http/server.go` | routes、HTTP errors、request ID、provider/connection lifecycle |
| `internal/transport/http/speculation.go` | worker追跡、public provider名の除去 |

## 2. 新規ファイル一覧

- `README.md`
- `internal/audio/input.go`
- `internal/protocol/protocol.go`
- `internal/protocol/validation.go`
- `internal/protocol/protocol_test.go`
- `internal/transport/http/public.go`
- `internal/transport/http/transcription.go`
- `internal/transport/http/public_test.go`
- `docs/api.md`
- `docs/realtime-protocol.md`
- `docs/transcription-api.md`
- `docs/errors.md`
- `docs/protocol-versioning.md`
- `docs/protocol/client-events.schema.json`
- `docs/protocol/server-event.schema.json`
- `docs/public-api-finishing.md`

## 3. Public API一覧

GET `/health`、POST `/v1/audio/speech`、WS `/v1/realtime`、WS `/v1/transcription`を正式surfaceとした。GET `/v1/capabilities`も追加した。SDKがマイク・socketを開く前に設定済み機能と制限を確認する用途があり、独立discovery routeにする意味がある。

## 4. Protocol versioning

protocol majorは文字列`"1"`。path `/v1`とsession.createdで一致させる。version指定はsession.configureで検証し、未対応ならunsupported_capabilityを返す。optional field/event/capabilityの追加はv1内で許容し、既存単位・意味の変更はmajor更新の対象とした。詳細は[versioning](protocol-versioning.md)。

## 5. Capability model

`features`は名前→`{version,available,modes?}`のmap。tts、transcription、conversation、realtime_audio、realtime_input、interruption、backchannel、speculation、audio_flow_controlを公開する。creditは`version:"credit-v1"`。providerの実装名や設定objectをそのままmarshalしない。

session.configureはprotocolと任意のcredit-v1選択を受け付け、session.configuredで応答する。既存playback.configureも維持する。追加capabilityを理解しないclientにcredit送信を強制しない。speculationは内部最適化であり、未対応clientにもprecommit出力を要求しない。

## 6. Event envelope

server eventはtype/event_id/session_id/timestamp/dataを共通化し、空dataも`{}`とする。generation_idとrelated_event_idはoptional。turn/conversation/speculation/interruption IDは既存のdata位置を保持した。Event実体とconstructorはprotocol packageへ移し、realtimeはaliasで既存呼び出し元を維持する。

event_idは128bit乱数をhex化し、globalでの確率的一意性を意図する。連番・再送cursorではない。timestampはUTC correlation用であり、Audio Timelineのsource frame軸へ置き換えていない。

## 7. Event catalog

[Realtime protocol](realtime-protocol.md)にSession、Conversation、Input Audio/Text、Turn、Transcription、Generation、Response Text/Audio、Playback、Interruption、Backchannel、Speculation、Errorを分類した。両direction、required/optional fields、state restrictionsを表にした。

Worklet内部のplayback.completed/underrunやpause/resumeをWebSocket eventと誤認しないよう明記した。ClientData validator tableにあるclient eventがカタログから抜けるとテストが失敗する。

## 8. Error schema / codes

code/message/recoverableを固定schemaとし、旧codeはlegacy_codeへ残す。カテゴリはinvalid_request、invalid_state、unsupported_format、unsupported_capability、payload_too_large、resource_limit、provider_unavailable、transcription_failed、generation_failed、audio_flow_error、timeout、internal_error、origin_rejected。

raw Go/provider error文字列をmessageとして返さず、公開message tableから作る。unknown event、malformed JSON、状態違反でも原則socketを即切断しない。過大message・worker濫用は非recoverableとして終了できる。詳細は[errors](errors.md)。

## 9. /v1/transcription設計

独立接続で、Conversation Session/LLM/TTSを作らない。format検証、bounded PCM append、最終STT呼び出し、共有STT admissionを再利用する。start→binary→commit→committed→transcript.finalのmanual flowを実装した。

1接続1active STT。空音声commitは拒否するが、非空音声から空文字を認識した場合は正当なfinalとする。現在のwhisper.cpp実装に合わせ、partialやstreaming STTは公開しない。自動endpointingが必要なclientは既存realtime入力を利用する。

## 10. Binary framing

両WSのclient binaryは入力PCM。realtimeのserver binaryだけがresponse.audio.delta直後の出力PCM。transcriptionからbinaryは返さない。WAVヘッダー付き音声を入力PCMとして受け付けない。

metadata/binaryは共有writer lock内で連続writeする。requestごとのscoped writerも同じlockを共有するため、correlation追加でatomicityを壊していない。WS fragmentationを含めたmessage全体へ上限を適用する。

## 11. Session lifecycle

公開上はconnecting→created→active→closing→closed。内部runtimeを共通enumに統合していない。root connection context、Session context、generation/turn contextの既存キャンセルを利用する。server shutdownはHTTPだけでなく追跡中WS contextもcancelし、接続slot解放を待つ。

## 12. Graceful close

session.closeを実装した。新規受付停止、context cancel、runtime/transport workerの2秒待機、session.closedのcleanup_complete、正常close handshakeを行う。write/handshakeにも別の上限がある。credit待ち・STT待ち・speculationは同じcancelへ反応する。

contextを無視するproviderをGoから強制停止したとは主張しない。その場合も公開closeを永久待機させず、cleanup_complete=falseを返すことを試験した。切断時にはfinal eventの到達を保証しない。

## 13. Request/event correlation

client event_idはoptional、128byte以下のASCII token。scoped writerが関連する応答・errorへrelated_event_idを入れる。グローバルな「最後に受けたevent ID」は持たず、並行STT/LLMの応答を別requestへ誤帰属させない。

自動endpoint/telemetryは単一requestとの対応がないため、turn/generation/conversation/speculation IDを使用する。IDsはidempotencyを提供しない。HTTP handshake request IDはsession.createdにも通知する。

## 14. Generation API

generation.createはclient-supplied response用で、LLM invocationとは分離する。default output=audio、明示textも対応。IDはserver所有。現在generationのreplace、duplicate createは別generation、stale scoped cancelは無視、ID省略cancelはlegacy current-cancelと明文化した。

clientが供給するresponse.textはsystem/developer promptへ昇格しない。Conversationのuser exchangeを作らないmanual generationを、次回LLMの架空user turnとして扱わない既存方針を維持した。

## 15. input_text API

input_text.commitはtext必須、output=text/audio（default text）。unknown data fieldsを拒否するstrict trust boundaryを維持し、role/messages/system_promptを受け付けない。必要providerが未設定なら公開エラーを返す。active audio inputとの混在制約も維持した。

## 16. Credit-v1正式仕様

configure→formal generation→chunk.started format→initial credit→delta/binary→played credit→generation.done→drainという順序を記載した。C/R/P/Bはsource-frame snapshotでB=R−P。送信可能量はmax(0,P+C−reserved)。加算grantではない。

legacy clientは2秒bounded windowを維持する。新schema/error/lifecycle導入後もAudio Runtimeのring、resampler、source frame進捗、played semantic chunkの意味は変更していない。

## 17. Provider availability

Engine.HasTTSで設定有無を照会し、STT/LLMの既存setterと合わせてcapabilitiesへ反映する。entry pointはAquesTalk/Whisper初期化失敗やOPENAI_API_KEY未設定で即終了せず、該当サービスを未設定として起動する。transcription-onlyにLLM/TTSは不要になった。

大規模DIやprovider置換は行っていない。無効なtransport/endpoint設定など構成ミスは従来通り起動失敗になり得る。availabilityは設定状態であって毎回の外部API疎通probeではない。

## 18. Health/readiness

/healthはprocess/HTTP aliveのまま。configured readinessは/v1/capabilitiesのavailableで確認する方式とし、重複する/readiness endpointは追加しなかった。外部LLMキーの有効性やSmartTurn稼働をhealthとして断定しない。

## 19. HTTP error / request ID

speech errorsはcode/message/recoverable/request_id、互換用error文字列を持つ。X-Request-IDとbodyが一致する。400/413/429/502/503/504等を意味で使い分ける。成功、エラー、preflightにもserver生成IDを付け、内部のrequestログへ関連付ける。

audio response開始後のstream failureは既送信statusを変更できないので、途中終了として扱う。クライアント側のaudio完全性確認が必要である。

## 20. CORS / Origin policy

same-origin、HTTP(S) localhost/loopback、no-Origin native clientを既定で許可する。追加はexact origin allowlist。HTTP/WSで同一policyを使い、許可Originだけechoする。credentialsは許可せず、wildcard originも使わない。

file://のnull Originは既定で拒否する。sampleはlocalhost HTTPから開く。追加OriginはAPI_ALLOWED_ORIGINS、埋め込み用途ではConfigから指定できる。Origin制御は認証ではなく、今回は認証systemを実装していない。

## 21. Resource limits

JSON 64KiB、binary 64KiB、output packet 16KiB、HTTP 1MiB、decoded text 32KiB、transcript 16KiB、manual PCM 120秒、2並列STT、64WS、16transport workers/socketを整理した。Conversation、continuous turn、speculation、audio queue、credit timeoutは既存runtimeの境界を維持した。

capabilitiesで主要値を取得でき、[API limits](api.md#limits-and-timeouts)に用途と責務を一覧化した。legacy入力にも総PCM上限を追加し、巨大JSON・fragmented binary・累積turnの上限を区別している。

## 22. Machine-readable schema

client-event structural schemaを実validatorのfield tableから生成し、server-envelope schemaもfixtureとして公開する。通常テストで生成結果とのdriftを検知する。更新は`go test ./internal/protocol -args -update`。

全state machineをJSON Schemaへ手書き複製してはいない。runtime state、credit arithmetic、byte budgetなどはschemaだけでは保証しないと明記した。HTTP OpenAPI generatorやSDK生成toolchainは今回追加していない。

## 23. Compatibility

有効な旧イベントflow、data省略/null、legacy input、input_text、text-only、credit-v1の旧configure、2秒bounded fallbackを維持した。server error codeのカテゴリ統一は意図的な正式化で、legacy_codeを残しbrowser sampleも更新した。

無制限PCM、誤format、未知client event、system role注入、任意cross-originは互換性を理由に許容しない。未知server eventはclientが無視する方針を明記している。

## 24. Security/privacy

provider raw error/path/key/model名/stackをpublic errorへ流さない。通常ログはIDs・event種別・サイズ・状態を中心とし、transcript、response本文、PCM、system promptを追加していない。Discoveryは手作りのsafe metadataであり設定struct全体のdumpではない。

暗号学的乱数IDに本文やsecretを埋め込まない。input_textのuser-only境界、speculationのcommit barrier、再生確認済みsemantic chunkだけの履歴方針を維持した。

## 25. Testsと結果

- gofmt実施。
- `go test ./...`通過。追加のPublic APIテスト、既存Input/Speculation/Conversation/Audio等を含む。
- 関連protocol/audio/realtime/conversation/speech/http/engineの`go vet`通過。
- `node --test examples/browser/*.test.cjs`: 25テスト通過。
- 全体`go vet ./...`: 既存AquesTalk `provider_windows.go:324`のunsafe.Pointer警告で失敗。
- `go test -race`: CGO無効のため起動不可。race検出済みとはしない。

指定45項目との対応:

| 番号 | 検証 |
| --- | --- |
| 1〜3 | session.created version、10000 event ID一意性、Envelope serializationとrequired keys |
| 4〜8 | malformed/null/型違い/巨大JSON、unknown client、invalid state、stable code |
| 9 / 45 | STT/TTSの偽secret・Windows path付きerrorがHTTP/WSへ出ない |
| 10〜14 | transcription connect、正しいPCM、format拒否、commit、空audio拒否・空final許容 |
| 15〜17 | transcription cancel、disconnect、graceful close、巨大binary |
| 18〜22 | 既存legacy/continuous/voice→audio、text→text、追加text→audio、実PCM framing |
| 23〜26 | generation cancel/stale cancel、credit待ち中のsession.close、provider cleanup、shutdown |
| 27〜29 | protocol configure、credit-v1 configure、既存2秒legacy window |
| 30〜34 | HTTP TTS成功・構造化error・request ID、health、capabilities未設定状態 |
| 35〜37 | HTTP/WSのloopback/exact origin許可、外部/null拒否、no-Origin native |
| 38〜43 | 既存multi-turn / played history / interruption / backchannel / speculation / audio flow回帰 |
| 44 | JSON/binary/累積PCM/transcript/接続枠、共有STT並列数、schema fixture同期 |

追加でcontextを無視するfake providerを使い、2秒cleanup budgetでcleanup_complete=falseを返すことを確認した。実WebSocket writerのmetadata/binary atomicityテストも継続している。

## 26. Docs

READMEからapi.md、realtime-protocol.md、transcription-api.md、errors.md、protocol-versioning.md、JSON Schema、本報告へ到達できる。simple TTS、text→text、text→audio、mic→transcript、mic→AI→audio、interruption、backchannel recovery、credit-v1のflowを記載した。

## 27. 実機確認が必要な項目

実ブラウザのlocalhost Origin/preflight、Electron origin、マイク・スピーカー、実AquesTalk/Whisper/OpenAI/SmartTurnを通した疎通は別途確認が必要。今回のAPI自動試験は実WebSocket/HTTPとmock provider、browser VMを使った。

長いWhisper処理中のcancel/timeout、タブ背景化、ネットワーク切断、実TTS出力途中のHTTP切断も実機で確認する。既存provider単体テストは回帰実行したが、外部API課金を伴う試験や新しい音質benchmarkは行っていない。

## 28. 既知の問題

- configured availabilityはremote health probeではない。
- 自動idle timeout、heartbeat scheduler、再接続復元、認証、永続化はない。
- providerがcontextを守らない場合、cleanup待機を終えてもその処理自体を強制停止できない。
- streaming HTTP開始後のerrorはJSONに置き換えられない。
- error code統一・厳格format/Origin/size検証は旧clientの不正な依存を変える可能性がある。
- schemaは構造subsetであり、stateful conformance検証の代わりではない。
- 実機検証、race detector、既存全体vet警告の制限は上記の通り。

## 29. SDK / CLIへの引き継ぎ

SDKはversion/capabilities確認、optional event ID、unknown server event無視、structured error処理、generation所有権、binary pair消費、credit snapshot計算、source frame進捗、close cleanupの順で実装できる。event IDで自動リトライを重複排除できるとは仮定しない。

transcription-only clientはmanual PCM collection/commit/final/cancelだけで構築できる。partial transcriptを期待しない。Browser SDKはlocalhost HTTPまたは明示allowlist Originを利用し、native SDK/CLIはOriginなしで接続できる。

TypeScript/Python SDK、CLI、Streaming STT、Speculative TTS、WebRTC、DB、認証、tool calling、RAGは実装していない。git commit / git pushも実行していない。
