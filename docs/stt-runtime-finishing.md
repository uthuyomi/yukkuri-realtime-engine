# ⑨ STT Runtime Finishing / Performance 実装報告

> Historical STT implementation/CPU measurement record. Statements about unavailable CUDA hardware describe that earlier run, not current project capability. For the later user-reported GTX 1660 real-microphone observation, see [performance](performance.md). Step 10 does not independently reproduce that observation.

今回の実装は、既存provider boundaryを保ったままwhisper.cppをEngine所有の常駐runtimeへ変更するもの。CPU実行・Windowsビルド・回帰試験・変更前後の合成音声latency計測を実施した。**GTX 1660/CUDA実機性能、日本語実音声の精度は未実施**。Streamingはupstream調査の結果、今回のboundaryへ安全に導入できるincremental方式ではないと判断し、偽partialを追加していない。commit / push / publish / Releaseは行っていない。

## 1. 変更ファイル一覧

| ファイル | 変更 |
| --- | --- |
| `README.md` | CPU/CUDA/device/benchmark/reportへの導線 |
| `cmd/engine/main.go` | env config、runtime初期化/Close、起動時signal cancellation、安全な診断ログ |
| `docs/api.md` | optional runtime metadata、実効STT concurrency/admission |
| `docs/transcription-api.md` | final-only persistentの説明 |
| `docs/speculative-generation.md` | persistent/単一contextとの共有admission説明 |
| `internal/protocol/protocol.go` | optional `TranscriptionRuntime` wire metadata |
| `internal/providers/stt/limited/provider.go` | runtime情報転送、bounded admission、queue waitログ |
| `internal/providers/stt/limited/provider_test.go` | admission overflow検証 |
| `internal/providers/stt/whispercpp/provider_test.go` | child-processテストの入口 |
| `internal/transport/http/public.go` | safe runtime discoveryと実効上限 |
| `internal/transport/http/server.go` | runtimeに合わせた共有同時実行数 |
| `internal/transport/http/transcription.go` | unavailable/capacity/timeoutのstable error mapping |
| `sdk/typescript/src/types.ts` | optional runtime metadata型 |
| `sdk/python/src/yukkuri_realtime/models.py` | 同型のTypedDict |
| `sdk/python/src/yukkuri_realtime/__init__.py` | 追加型のexport |

## 2. 新規ファイル一覧

- `cmd/stt-bench/main.go`, `main_test.go`: ローカルbenchmark、WAV validation、JSON出力契約。
- `internal/providers/stt/runtime.go`: safe RuntimeInfoと分類済みerror。
- `internal/providers/stt/whispercpp/config.go`: device/model/resource設定、upstream pin。
- `internal/providers/stt/whispercpp/runtime.go`: lifecycle、queue、serialized inference、fallback、cancel/discard。
- `internal/providers/stt/whispercpp/worker.go`: upstream HTTP server / process CLI integration、actual probe、bounded diagnostics。
- `internal/providers/stt/whispercpp/child_windows.go`, `child_other.go`: Windows hidden child + Job Object、他OS通常cleanup。
- `internal/providers/stt/whispercpp/stats_windows.go`, `stats_other.go`: Windows memory/CPU instrumentation、未対応OSではnull。
- `internal/providers/stt/whispercpp/runtime_test.go`, `worker_test.go`: device/lifecycle/resource/実child/opt-in実機テスト。
- `internal/transport/http/stt_runtime_test.go`: discovery/error/admission契約。
- `scripts/setup-whisper.ps1`: pinned CPU/CUDA buildとローカル配置。
- `scripts/stt-baseline.py`: 変更前flagsのWindows計測再現。
- `docs/stt-runtime.md`, `docs/stt-performance.md`, `docs/stt-runtime-finishing.md`: 設計・setup・結果・本報告。
- `docs/benchmarks/stt-before-cpu.json`, `stt-persistent-installed-cpu.json`, `stt-persistent-pinned-cpu.json`, `stt-process-pinned-cpu.json`: 本文/pathを含まない計測値。

ローカルのruntime/upstream checkout、CPUビルド、モデル、生成WAV、raw診断ファイルは既存.gitignore配下。AquesTalk proprietary filesは変更していない。

## 3. 変更前STT benchmark

実装前に既存CLI/既存small/既存flagsを実行した。PCM s16le mono16kHzの合成無音2/5/10/30秒、各2回。発話全体wallは約12〜24秒。中央値は順に13.001 / 18.351 / 18.123 / 12.628秒。合成無音はlatency fixtureであってaccuracy fixtureではない。別診断で2秒入力のmodel load 658.86ms、encoder 10,253.96ms。loadだけがボトルネックとは言えなかった。詳細・全sampleは[performance](stt-performance.md)。

## 4. STT Runtime architecture

Engine → shared bounded admission → one Runtime → one upstream whisper-server/model/context。input/session/conversation/audio state machineは統合していない。既存`Provider.Transcribe`は維持し、optional runtime metadata interfaceだけ追加。/v1/transcription、Realtime、speculationが同じbudgetを使う。

## 5. persistent方式の選定理由

実際のインストール済みserverとupstream sourceを調査。常駐モデル、mutex、multipart inference、health、cancel callbackを確認した。native APIはCGO/ABI維持とnative crash isolationが追加課題。独自IPCの必要性はなかったためupstream HTTPを採用。CPUビルドで検証したpinは`d09f61a708f3487afa956ff578e60eae5e7a233c`。[比較・一次資料](stt-runtime.md#architecture-and-choice)。

## 6. CPU backend

`STT_DEVICE=cpu`は必ず`-ng`、CUDAを試さない。CPU用exe/DLLは専用ディレクトリ。既存root配置へのfallbackを維持する。古いCLI providerは比較用に保持し、Engine既定はserver常駐。VS2022/CMakeによるCPU build成功、実モデルmultiple utterance成功。

## 7. CUDA backend

専用CUDA build/runtime配置。exeの存在だけでなく、起動、モデルのCUDA allocation、backend evidence、実probe推論成功を必須とした。upstream内部のsilent CPU fallbackも拒否する。CUDAの実ビルド/実推論は未実施。fake worker/HTTP子processで選択・拒否の契約を試験したが、GPU実機試験とは区別する。

## 8. GTX 1660対応

対象はGTX1660、compute capability7.5。build architecture `75`を指定可能なsetup pathを追加した。型番判定やallowlistはない。CUDA Toolkit/driver/MSVC/DLLの手順は[setup](stt-runtime.md#cuda-setup-and-gtx-1660)。この実行環境はIntel Iris Xeで、GTX1660接続・CUDA Toolkitがなく、対応の最終実機確認は残る。

## 9. device auto-selection

既定auto。CUDA候補を実際に起動・検証し、成功時のみ選択。CUDA未配置の実環境でauto→CPU、safe fallback metadata、実推論成功を確認した。nvidia-smi/GPU名を判定条件にしていない。現在対応する候補はCUDA/CPUであり、Intel/Vulkan/DirectMLを追加したという意味ではない。

## 10. fallback semantics

auto初期化失敗時はCUDA workerを回収してCPUへ。timeout budgetはbackendごと。実行中CUDA障害はそのrequestを失敗させ、次requestからCPUへ切替可能。同requestの二重finalは作らない。明示cudaは常にCUDAを維持し、実際に未配置状態でbenchmarkがexit1になることを確認した。fallback理由はsafe categoryのみ。起動時失敗はSTT unavailableとし、Engineの他機能は限定起動可能。

## 11. model/device separation

`STT_MODEL=small`のまま。GPUでmedium、CPUでtinyへ変える処理なし。custom model pathはpublic/logに`custom`のみ。ja、threads4、best-of5、beam5を保ち、serverの異なる既定値を明示的に補正した。language autoも選択可能。精度を下げた高速化はしていないが、日本語実音声のquality equivalenceは別途評価が必要。

## 12. startup/runtime lifecycle

起動→model load→probe→ready→複数infer→shutdown cancel→kill/wait/cleanup。初期probeは実利用可能性検査で、別途時間計上。起動中にもsignal contextを尊重する。通常成功時は再ロードなし。cancel/crashでは安全のため破棄し次回reload。Closeは並行呼び出しでもcleanup完了を待つ。

## 13. concurrency

同一モデルcontextは1推論に直列化。Public limitを従来の2と偽らず、runtime時は`concurrent_stt=1`、既定admission8をcapabilitiesへ。generic providerの旧2枠は保持。ordinary queueはbounded/cancellable、speculative admissionはnonblocking。process/model/contextをsession数に比例して増やさない。

## 14. cancellation

input/turn/speculation/session/disconnect/shutdownの既存ctxを受け取り、返却直前にもctxを確認。late successでも破棄する。HTTP cancel後にworkerをkill/reapしてslotを解放し、obsolete inferenceが次requestを塞ぐことを防ぐ。固定CPU実機で100ms timeout時の結果破棄・worker終了を検証した。cancel後の次回にはモデルprobe込みreloadコストが発生する。

## 15. failure isolation

native inferenceは子process内。crashはstructured failureとなりEngine panicにしない。stderr/stdout/HTTP responseはbounded、publicへraw errorを出さない。Windows Job Objectは親handle close時にworkerを終了し、child process数1を制限。通常cancel/crash/shutdown/no-child-survivalを実child testで確認。生成直後のJob assignment前には短いOS windowがある点を文書化した。

## 16. Streaming STT方式

今回は未実装。upstream whisper-streamはSDL捕捉＋重複windowへのwhisper_full再実行。現在のHTTP方式にはhost PCMのincremental decode/revision streamがない。CPU encoder約10秒に対してsubsecond窓の反復推論を追加するのは資源・latency上も不適切。native/isolated streaming bridgeと日本語精度評価を次案にした。これはユーザー指定の「不適切なら理由と次案を報告」に該当する。詳細は[調査と次案](stt-runtime.md#streaming-decision-and-next-implementation)。

## 17. partial transcript

追加していない。final-onlyから偽partialを作らない。first_partial_msはnull。formal User item、generation、audioをpartialから作る経路もない。今後のpartial導入にはauthoritative final、revision、bounded queue、cancelの独立契約が必要。

## 18. Public Protocol変更

v1のまま。イベント/PCM framing/final/commit promise/session.closeは維持。optional `features.transcription.runtime` と実効STT limitsを追加。既存error codeへのmappingを整理。device configはserver側。reconnect restorationや認証endpointは追加していない。

## 19. SDK変更

TS `TranscriptionRuntime` / Python `TranscriptionRuntime` TypedDictとoptional capability fieldだけ。partial eventは未追加。TypeScript20件、Python/CLI13件の既存回帰試験成功。CLI `transcribe`の使い方やGPU選択責務は変えていない。

## 20. Speculation統合

既存snapshot STTとcommit barrierを維持し、新しいshared admissionを利用する。max workers/attempts/cooldown/revision/lifetime/bufferの既存制御は維持。新しいpartial-driven LLM requestは開始しない。runtime1枠化によるpromotion率・実会話latencyの影響は日本語実機測定へ引き継ぐ。

## 21. observability

起動時backend/requested/selected/model/persistent/fallback、安全なstate。transport queue waitとruntime wait、request duration、cancel flag、last measurement。Windows peak working set/CPU time。本文、PCM、system prompt、秘密/path/raw CUDA errorは出力しない。public correlationは既存event/turn/request IDを保つ。Runtimeログはpayload-free timingであり、全requestのtrace IDをprovider interfaceへ追加したわけではない。

## 22. benchmark tool

`go run ./cmd/stt-bench`。legacy/process/persistent、cpu/auto/cuda、model、WAV/合成長、repeat、exe override。JSONLにwall、duration、RTF、EOT→final、startup/probe、利用できる内部phaseとmemory/CPU。未計測phaseはnull。旧flagsの詳細Windowsbaselineは`scripts/stt-baseline.py`。commands/解釈は[performance](stt-performance.md#commands)。

## 23. benchmark結果

固定CPU persistentは各長2回、wall約9.7〜10.7秒。中央値2/5/10/30秒順に10.597/10.075/9.716/10.205秒。初期化10.218秒は別計上。無音・少数回・負荷非統制の結果で、日本語会話がこの時間になる保証ではない。測定値の全件をJSONに保存した。

## 24. CPU vs CUDA比較

CPUのみ実測。CUDA process/persistent、GTX1660 VRAM/latencyは**未実施**。比較可能なtool/setupはあるが、比較したという主張はしない。GPU有無の判定テストは性能評価ではない。

## 25. process-per-utterance vs persistent比較

旧installed process、installed persistent、pinned process、pinned persistentを別系列で記録した。同一small/ja/search設定。繰り返しmodel loadがなくなることは実装とchild reuse testで確認。観測されたlatency差にはCPU負荷・cache・build provenanceも含まれるため、単純な高速化倍率を保証しない。

## 26. Streaming latency

first partial/finalization分離/EOT streamingは**未実施**。final-only request後のwallとRTFを測定。EOT→finalはbench内では既にbuffer済みPCM投入からの時間で、SmartTurn/network遅延を含まない。真のmic→EOT→finalはmanual項目。

## 27. resource usage

実測peak: old CPU process768.961MiB、pinned persistent777.613MiB、pinned process769.891MiB。persistentはlifetime peak。OS queryが取れない値はnull。worker/model/context各1、runtime queue既定8、PCM最大600秒相当、response64KiB、stderr fragment4096byte。public manual120秒・transcript16KiB等は維持。GPU/全Engine RAMは未測定。

## 28. testsと結果

| 検証 | 結果 |
| --- | --- |
| `go test ./...` | 全package成功（外部実機opt-inは通常skip） |
| 対象Go packagesの `go vet` | 成功。STT/bench/protocol/audio/realtime/conversation/speech/HTTPを対象 |
| `sdk/typescript: npm test` | build成功、20/20 |
| Python unittest discover | 13/13、CLI結合を含む |
| browser node tests | 25/25 |
| Windows pinned CPU setup/build | 成功 |
| 実small CPU persistent benchmark | 成功、複数utterance再利用 |
| 実small CPU cancel smoke | 成功、100ms timeout後resultなし/worker回収 |
| 実auto/missing CUDA fallback | CPU selected、推論成功、fallback metadata確認 |
| 実explicit CUDA unavailable | exit1、CPU成功に偽装しない |
| CUDA実device / GPU benchmark | 未実施 |
| Race detector | 未実施（現在CGO_ENABLED=0） |

要求テスト項目の対応:

| 項目 | 主なカバレッジ |
| --- | --- |
| 1–8 device/diagnostics/model分離 | RuntimeDeviceSelection、AutoInitializationTimeout、EnvironmentModelDeviceSeparation、CPUArguments、CUDA evidence/probe tests |
| 9–11 reuse/load/shutdown | PersistentReuseAndShutdown、UpstreamHTTPWorkerReuseAndReap |
| 12 concurrency/admission | BoundedRuntimeQueueAndShutdownCancellation、SharedAdmission、AdmissionQueueIsBounded、HTTP actual limit |
| 13 cancellation discard | CancellationDiscardsLateResultAndReloads |
| 14–16 session/disconnect/speculation | 既存TranscriptionCancellationAndCleanup、speculation revision/barrier/cancellation試験、およびruntime ctx-discard試験 |
| 17–18 crash/no zombie | CrashIsolationAndFallbackPolicy、UpstreamWorkerCancelTimeoutAndCrashReap、installed cancel smoke |
| 19–22 format/empty/timeout/bench | RuntimeTimeoutAndValidation、empty result API回帰、BenchmarkOutputContract、malformed WAV |
| 23–26 CPU/final/transcription/conversation | 実CPU系列、Go API/Conversation/Realtime一式 |
| 27–30 SDK/CLI/capabilities | TS20、Python13、RuntimeDiscoveryAndActualAdmissionLimit/RuntimeErrorsHaveStablePublicCodes |
| 31–45 streaming固有 | 非該当: streaming/partialを広告・実装していない |

外部Whisper/TTS/LLMの日本語実会話品質をmock回帰で確認済みとは扱っていない。

## 29. docs

[runtime](stt-runtime.md): architecture、CPU/CUDA setup、device/fallback、limits、privacy、streaming検討。 [performance](stt-performance.md): raw結果・matrix・再現command・manual精度計測。README/API/transcription/speculationの現在仕様も更新した。

## 30. setup/build/run方法

```powershell
./scripts/setup-whisper.ps1 -Backend cpu
$env:STT_DEVICE = 'auto'
$env:STT_MODEL = 'small'
go run ./cmd/engine
# 別terminal: 既存CLI
python -m yukkuri_realtime transcribe input.wav
```

CUDAは適合Toolkit/driver/MSVCを用意し、`setup-whisper.ps1 -Backend cuda -CudaArchitectures '75'`。必要ならsupported toolsetを`-VCToolset`指定。巨大binary/modelはGitへ入れない。詳細の依存DLLと取得・配置方針はruntime doc。

## 31. 実機確認が必要な項目

GTX1660上のCUDA build/init/probe、CPU強制との比較、missing DLL/driver/VRAM不足fallback、日本語2/5/10/30秒のCERとlatency、mic/SmartTurn含むEOT、同時sessionとspeculation採用率、cancel/reloadの体感遅延。AquesTalk/Whisper/OpenAIを接続した実会話・割り込み/回復・playback historyの聴感確認。CUDA runtime再配布対象hostでのDLL依存解決。

## 32. 既知の問題・制約

CPU smallは依然約10秒以上かかり、低latency会話の完成を主張できない。cancel時にモデルを破棄するため、頻繁なspeculation invalidationでreloadコストが増える。startup probeも推論1回分必要。CUDA readinessはpinned diagnostic vocabularyに依存し、未知buildではfail-closed。startup unavailableは自動無限再起動せずEngine再起動が必要。Streamingと日本語精度評価は未完。汎用OSのpeak memory/CPU instrumentはWindows以外null。Windows child作成→Job assignmentの短い区間は完全な親子atomic launchではない。

## 33. ⑩ Release / Qualityへの引き継ぎ

最優先はGTX1660実機でC/D matrixとJapanese quality/latencyを測ること。pin/Toolkit/toolset/driver/DLL/モデルchecksumを固定し、CPU-onlyとCUDA配布を別々のclean Windowsで検証する。起動probe・cancel/reload・単一admissionのtail latencyも判定対象にする。性能・精度の合格基準なしにReleaseのGPU対応完了とはしない。SDK v1 final-only契約は維持する。将来streamingはoptional provider/event、bounded revisions、commit barrier検証を揃えてから導入する。npm/PyPI publish、GitHub Release、LLM local inference等には今回進んでいない。
