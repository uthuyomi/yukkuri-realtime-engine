# Realtime WebSocket protocol v1

[English](realtime-protocol.md) | 日本語

`/v1/realtime`へ接続し、最初に`session.created`を受信します。内部のTurn、Interruption、Speculation、Conversation、Audio Runtimeはそれぞれ独立した状態機械です。

## Envelope・相関・binary framing

```json
{"type":"generation.created","event_id":"evt_...","session_id":"sess_...","timestamp":"2026-09-24T12:00:00Z","related_event_id":"client_1","generation_id":"gen_...","data":{"voice":"f1","speed":1.0}}
```

サーバー必須フィールドはstringのtype／event_id／session_id／timestampとobjectのdataです。timestampはUTC RFC3339、event_idはopaqueな一意IDです。generation_id／related_event_idは任意。turn／conversation／speculation／interruption IDはdata内にあります。timestampは観測用で、sample精度の再生clockではありません。

clientの必須envelopeはtypeです。必須data fieldのないイベントではdataの省略／null／空objectが可能。event_idは相関用で冪等キーではなく、重複送信を排除しません。任意のsession_idは接続と一致する必要があります。generation_idは対象generationを選びます。clientがserver ID／時刻／特権roleを決めることはできません。client IDはASCII英数字と`_ . : -`で128 bytes以内（空は許可）、server event IDはevt_＋128 random bitsのhexで、連番・replay cursorではありません。

| 接続 | Client binary | Server binary |
| --- | --- | --- |
| /v1/realtime | 直近の受理済みinput_audio.startのraw PCM | 対応response.audio.deltaの直後のraw PCM |
| /v1/transcription | startからcommit間のraw PCM | 送信しない |

入力はmono PCM16 LE 16kHzで、WAV header、base64、任意JSONへの付属payloadではありません。出力はresponse.audio.delta 1件にbinary 1 messageが直後に対応します。writerはmetadata／binary間へ他の制御やgenerationを挟みません。古いgenerationでも対応binaryまで読んで両方破棄します。上限はfragment単位ではなくWebSocket message全体に適用します。

## Session lifecycle

connecting → created → active → closing → closed。session.closeは新規受付を止め、generation／STT／credit待機／先行生成をcancelします。runtimeとtransport workerのcleanupは共有2秒枠です。その後、別の有限writeで`session.closed {cleanup_complete:true|false}`を返し、WS close 1000へ進みます。speaker drainは待たず、未発話PCMを破棄します。session終了時は各generationのdone／cancel通知を保証しません。

disconnectでも同様にcancelしますが、最終イベントは保証できません。server shutdownはhijack済みWebSocket contextもcancelします。cancelに従わないproviderはcleanup枠より長く残り、cleanup_complete=falseで示します。接続は終了し、新しい処理は受け付けません。永続session、resume token、replay、自動再接続復元はありません。新socketは新session／conversationです。

## Client → server catalog

genはenvelopeのgeneration_idを表します。表のfield名はwire識別子を保持しています。未知clientイベントはinvalid_request、未知serverイベント／fieldはclient側で無視します。既存v1の意味を変えない任意追加は許可します。未知data fieldは一般に許可しますが、user-only境界のinput_text.commitは拒否します。

| Event | 必須フィールド | 任意フィールド | 動作・制約 |
| --- | --- | --- | --- |
| Session: `session.configure` | data.protocol_version string | audio_flow_control=`credit-v1` | v1のみ。credit選択はgeneration開始前。session.configuredで応答。 |
| Session: `session.close` | none | event_id | 開いているsessionを終了し、以後の仕事を受け付けない。 |
| Session: `ping` | none | event_id | pong応答。WebSocket制御frameのpingとは別。 |
| Generation: `generation.create` | none | output=`text`/`audio`, voice string, speed nonnegative number | 既定audio。現在のgenerationを置換／cancel。応答textはclientが供給し、LLMは呼ばない。 |
| Generation: `generation.cancel` | none for legacy | gen | 一致generationをcancel。古いIDは無視。ID省略は現在のgeneration。 |
| Response Text: `response.text.delta` | data.text string | gen | client提供の応答生成へ投入。古いscoped IDは無視。textとpipelineに上限。 |
| Response Text: `response.text.done` | none | gen | 提供応答の終了。text完了、または音声chunkをflush。 |
| Input Text: `input_text.commit` | data.text non-whitespace string | output=`text`/`audio` | 既定text。user turnをcommitしてLLMを呼ぶ。audio入力中は拒否。role/system/developerなし。 |
| Input Audio: `input_audio.start` | sample_rate=16000, channels=1, encoding=`pcm_s16le` | mode=`realtime` or empty | 空modeはmanual buffer。realtimeにはendpoint runtimeが必要。方式変更前に停止。 |
| Input Audio: `input_audio.commit` | none | event_id | manual専用、空PCM不可。final STTから会話へ。realtimeはサーバーがcommit。 |
| Input Audio: `input_audio.cancel` | none | event_id | 入力破棄と旧STT cancel。continuousはlisteningへ、manualは収集終了。 |
| Input Audio: `input_audio.stop` | none | event_id | 入力停止、buffer破棄、入力側処理cancel。 |
| Turn: `input_audio.speech_start` | none | gen, interruption_id | realtime入力中のVAD開始。指定genへの割り込み疑いを発生し得る。 |
| Turn: `input_audio.speech_end` | none | event_id | realtime入力中のVAD終了。これ自体はPCMをcommitしない。 |
| Turn: `input_audio.vad_misfire` | none | event_id | realtime入力中の誤検出／復帰metadata。 |
| Playback: `playback.configure` | flow_control=`credit-v1` | event_id | generation前のcredit交渉の旧表記。独立ACKなし。 |
| Playback: `playback.credit` | gen; capacity_source_frames, received_source_frames, played_source_frames, buffered_source_frames integers ≥0 | event_id | generation別の累積snapshot。計算整合性を検証し、古いgenerationは無視。 |
| Playback: `playback.progress` | played_source_frames integer ≥0 OR played_seconds number ≥0 | gen, both positions | native source framesが優先。旧secondsはsource rate基準。常にgenを指定すること。 |
| Playback: `playback.paused` | gen, interruption_id; one played position | both played positions | active pause token一致時のみ。読取位置を確定し、bufferは破棄しない。 |
| Playback: `playback.overflow` | none | gen, interruption_id | client音声の終端失敗。現在のgenerationをcancel、古いgenは無視。 |
| Interruption: `interruption.suspected` | gen, interruption_id | event_id | clientは即時pauseして判定を待つ。 |
| Interruption: `interruption.failed` | gen, interruption_id | event_id | client判定watchdog失敗。サーバーの正式cancel経路へ。 |

## Server → client catalog

すべてに必須server envelopeがあります。表のフィールドは特記以外data内です。same common fieldsはspeculation_id／turn_id／revision／state／stage／duration_msです。

| Event | 必須フィールド | 任意フィールド | 動作・制約 |
| --- | --- | --- | --- |
| Session: `session.created` | protocol_version, capabilities, request_id | realtime: transport, audio_flow_control, legacy_audio_window_ms, conversation_id, interruption_timeout_ms; transcription: connection_type | 接続ごとに1回、最初のイベント。 |
| Session: `session.configured` | protocol_version | audio_flow_control; related_event_id | 明示設定成功。 |
| Session: `session.closed` | cleanup_complete boolean | related_event_id | 正常終了の最終metadata。replay保証なし。 |
| Session: `pong` | empty object | related_event_id | JSON pingへの応答。 |
| Conversation: `conversation.item.updated` | conversation_id, item_id, role, status, content_bytes, sent_text_bytes, played_chunks, generated_frames, sent_frames, played_frames | turn_id, generation_id; envelope gen | best-effort metadataのみ。transcript／生成text／system promptは含まない。 |
| Input Audio: `input_audio.started` | sample_rate, channels, encoding | mode, related_event_id | transcription endpointで入力設定を受理。 |
| Input Audio: `input_audio.committed` | turn_id, bytes | related_event_id | transcriptionの空でないbufferをSTT用に受理。 |
| Input Audio: `input_audio.cancelled` | turn_id (possibly empty) | related_event_id | transcriptionのcancel／stop確認。 |
| Turn: `input_audio.turn` | state | turn_id, reason, error | realtime turn状態。errorは安全化し、JSONにPCMは含めない。 |
| Transcription: `input_audio.transcript.final` | text, language, turn_id | related_event_id where a manual commit caused work | 正式な確定発話。現在partialイベントなし。 |
| Generation: `generation.created` | envelope gen | client supplied: voice, speed; conversation: source, output; promoted: source, speculation_id; related_event_id | 正式／確定出力のみ。gen IDはサーバー所有。 |
| Generation: `generation.done` | object | audio: source_frames, sample_rate; envelope gen | TTS／送信pipeline完了で、speaker完了ではない。text-onlyは空object。 |
| Generation: `generation.cancelled` | object | reason; envelope gen | 未再生出力を破棄。旧generationのイベントで新出力を消さない。 |
| Response Text: `response.text.delta` | text | envelope gen, related_event_id | LLM出力delta。生成／監査textと再生済み履歴は別。 |
| Response Text: `response.text.done` | empty object | envelope gen | LLM stream完了。TTS／audioは残り得る。 |
| Response Audio: `response.audio.chunk.started` | sequence, text, codec, sample_rate, channels, bits_per_sample | envelope gen | 意味単位chunk。credit待機前に形式を通知。textは正規化済みTTS入力。 |
| Response Audio: `response.audio.delta` | speech_sequence, audio_sequence, bytes, source_frames, source_start_frame, sample_rate, channels, bits_per_sample | envelope gen | 直後に必ず対応する1つのbinary PCM message。 |
| Response Audio: `response.audio.chunk.done` | sequence, bytes | envelope gen | chunk PCM送信済み。clientでbuffer／pause中の可能性。 |
| Interruption: `interruption.suspected` | interruption_id, state, reason, playback | turn_id, decision, error; envelope gen | サーバーが暫定pauseに入った。 |
| Interruption: `interruption.recovered` | interruption_id, state, reason, playback | turn_id, decision, error; envelope gen | 一致clientは保持した読取位置から再開。 |
| Interruption: `interruption.confirmed` | interruption_id, state, reason, playback | turn_id, decision, error; envelope gen | 未再生PCMを破棄。通常generation.cancelledが続く。 |
| Backchannel: `input_audio.backchannel` | interruption_id, state, reason, playback | turn_id, decision; envelope gen | 相づちと分類した復帰。新user／assistant turnを作らない。 |
| Speculation: `speculation.started` | speculation_id, turn_id, revision, state, stage, duration_ms | reason and timing fields | 背景STT候補。出力の許可ではない。 |
| Speculation: `speculation.ready` | same common fields | stt_duration_ms, llm_first_delta_ms | STTまたはbuffer済みLLM準備完了。まだ確定発話ではない。 |
| Speculation: `speculation.promoted` | same common fields | generation_id, saved_ms; envelope gen | 正式generationが有効候補を採用。 |
| Speculation: `speculation.invalidated` | same common fields | reason, wasted_ms | 候補を無効化。古いtext／PCMは公開不可。 |
| Speculation: `speculation.cancelled` | same common fields | reason, wasted_ms | 候補cancel。 |
| Speculation: `speculation.fallback` | same common fields | reason, generation_id, wasted_ms | 最適化失敗。通常の確定処理が継続し得る。 |
| Error: `error` | code, message, recoverable | legacy_code; related_event_id, gen | 安定error code。raw Go／provider errorではない。 |

先行生成のtiming／saved_ms／wasted_ms／stage／reasonは観測情報で、commitやreplayの契約ではありません。turn stateはidle、listening、speaking、possible_end、waiting、complete。conversation statusはpending、committed、completed、interrupted、cancelledです。

interruptionのplayback objectは互換性のため`GenerationID, SampleRate, Channels, GeneratedFrames, SentFrames, PlayedFrames, Paused, PausePlayedFrames, StartedAt, UpdatedAt`という既存表記を維持します。frameとrateはsource PCM基準です。Worklet内のplayback.underrun／playback.completed／format／audio／pause／resume／clear／doneはWebSocket client要求ではありません。

## Generationと会話

出力はsessionあたり最大1つのactive generationです。createは毎回新IDを作り、重複client event_idでも置換になります。置換は旧contextをcancel、credit待機を解除、再生済み履歴を確定し、音声状態を初期化します。古いscoped cancelは無視します。ID省略cancelは旧互換のためだけで、SDKはIDを付けます。

generation.create＋client response.text.delta/doneは提供応答／TTS生成で、LLMやuser commitは行いません。userのないassistantを次回contextの架空会話にしません。input_text.commitはサーバー所有の履歴／system指示でLLMを呼びます。既定はtext（generation.createはaudio）。role、messages、custom system promptを受け付けず、音声入力中は停止してから送ります。

音声はmanual commitまたはSmart Turn endpoint → final STT → user commit → 正式応答です。先行生成はそれ以前にtext／PCMを公開できません。誤割り込みでは同generation／itemを復帰し、真の割り込みでは完全に再生確認した意味単位chunkだけを次回contextへ含めます。

## Credit-v1とsource frames

1. generation前に`session.configure {protocol_version:"1",audio_flow_control:"credit-v1"}`を送りsession.configuredを待つか、旧playback.configureを使います。
2. 正式generationの初期source creditは0です。
3. response.audio.chunk.startedで形式を受け、clientはreceived=played=buffered=0の初期capacityを送ります。
4. serverは枠内のsource framesを予約し、metadata＋PCMをatomicに送ります。
5. clientはresample／render後に累積位置を報告します。pause中の受信だけではcreditを補充しません。
6. generation.doneの総source framesに対してtailをflushし、drain後に正確な最終played_source_framesを報告します。

```json
{"type":"playback.credit","generation_id":"gen_example","data":{"capacity_source_frames":16000,"received_source_frames":8000,"played_source_frames":4000,"buffered_source_frames":4000}}
```

C=capacity、R=received、P=played、B=bufferedで`B=R-P`、`0 <= P <= R <= server_reserved`。Cはsource rateの100ms〜30秒。送信可能数は`max(0,P+C-server_reserved)`で、予約はnetwork／MessagePortのin-flightも含みます。加算grantではなくsnapshotであり、重複でcreditを増やしません。古いreceived／playedは履歴進行も含め無視します。

credit待機中にSession／writer mutexを保持しません。cancel／置換／disconnect／30秒timeoutで解除し、制御イベントはaudio creditを消費しません。sourceと出力hardwareのframeを混ぜないでください。非交渉の旧互換モードも2秒windowと実再生ACKで上限を設け、無制限fallbackはしません。

## Transcription WebSocket

`/v1/transcription`はsession共通イベントとmanual input start/commit/cancel/stopのみです。modeは空またはmanualで、realtime VADやplayback／generationはunsupported_capabilityです。

```text
C input_audio.start {sample_rate:16000,channels:1,encoding:"pcm_s16le"}
S input_audio.started
C raw PCM binary (each <=65536 bytes, even length)
C input_audio.commit (event_id optional)
S input_audio.committed {turn_id,bytes}
S input_audio.transcript.final {turn_id,text,language}
```

1接続1active STTで、終了後startから繰り返します。commitのevent_idをrelated_event_idとして受理／final／errorへ対応付けます。空PCMは拒否し、空の認識結果は有効です。cancel／stopはbufferとSTTをcancelしinput_audio.cancelledで確認、以後古いfinalを出しません。provider終了まで一時的にresource_limitになる場合があります。会話／LLM／TTS／先行生成／transcript履歴を作りません。詳細は[transcription](transcription-api.md)。

## エラー・上限・互換性

```json
{"type":"error","event_id":"evt_example","session_id":"sess_example","timestamp":"2026-09-24T12:00:00Z","related_event_id":"client_42","data":{"code":"invalid_state","message":"This operation is not valid in the current state.","recoverable":true}}
```

公開errorはcode／message／recoverableと任意legacy_codeです。raw provider errorを返しません。recoverableは一般的な修正可能性で、cancel済みgenerationの復元保証ではありません。[code一覧](errors.md)を使用し、message文言を解析しないでください。

WS JSON／入力binary各65536 bytes、出力binary 16384 bytes、HTTP body 1 MiB、text 32768 bytes、final transcript 16384 bytes、manual PCM 120秒。上限超過は可能ならpayload_too_largeの後close 1009。不正JSON／未知イベントは通常recoverable error。HTTP/WS共通Origin policyとその他の実効上限は[API](api.md)を参照してください。無通信だけで切断するapplication idle/read timeoutはなく、JSON pingは任意診断で自動再接続ではありません。

構造契約は[client schema](protocol/client-events.schema.json)と[server envelope schema](protocol/server-event.schema.json)。状態／算術／方向の完全な仕様ではありません。v1でfieldの意味やbinary対応を黙って変えません。互換性を破る変更はmajor protocol/pathの更新が必要です。

## 使用フロー

text会話はinput_text.commit → generation.created → response.text.delta群 → response.text.done → generation.done。会話metadataは割り込んで届き、順序barrierではありません。

text→audioはoutput=audioを指定し、chunk.started → credit → metadata/binary群 → generation.done → 実drain後の最終ACKへ進みます。

mic会話はmode=realtimeでstartし、無音も含む連続PCMとVAD情報を送ります。serverのpossible_end／waiting／completeからfinal transcript、正式generation、応答へ進みます。

割り込み疑いではclientがGを保持pauseし、同じinterruption_idでspeech_start／playback.pausedを送ります。interruption.recoveredなら一致G/tokenを復帰、相づちならinput_audio.backchannelも通知します。真の割り込みならinterruption.confirmedとgeneration.cancelledで未再生PCMを破棄します。発話中に新generationが来た場合は独立interruption.suspectedも使えます。watchdogはinterruption.failedで正式cancelを要求し、永久pauseを避けます。
