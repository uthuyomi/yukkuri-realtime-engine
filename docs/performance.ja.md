# 性能と計測の意味

[English](performance.md) | 日本語

## 計測値の定義

現在のBrowser Voiceは`history.mjs`でサーバーRFC3339時刻をナノ秒精度で差分計算し、その後msへ変換します。表示の丸めは保存値を変えません。イベント生成時刻はprovider内部の計測境界とは異なります。

| UI metric | 現在の計測と制約 |
| --- | --- |
| EOT → STT final | `input_audio.turn`のstate=complete生成 → final transcript生成。EOTはサーバー確定通知で、音響的な発話終了ではない |
| STT final → LLM first delta | final transcript → 最初のresponse.text.delta生成。promotionでbuffer済み出力を使う場合、先行推論の実時間とは異なる |
| LLM first delta → TTS start | 未取得。公開TTS開始時刻なし |
| TTS start → first audio ready | 未取得。公開TTS開始時刻なし |
| Server TTFA | EOT通知 → 合成後の最初のresponse.audio.chunk.started通知。provider内部のfirst-readyそのものではない |
| Client TTFA | performance.nowでEOT通知受信 → 最初のPCM受信。別時計同士の差分ではない |
| Audible TTFA | 未取得。PCM受信／render進捗だけでは物理的な可聴開始を測れない |
| Interruption latency | 同じgeneration／interruptionのsuspected → confirmedサーバー通知。マイク検知からspeaker停止までの時間ではない |
| total | EOT → generation.done／cancel／error通知。text入力はgeneration.created起点。生成終了は再生drainではない |

欠測・相関不明・逆順で負になる差分は`—`で、0の推定値にしません。generation／session IDと会話metadataでturnを結合し、先行生成の通知だけで通常turnを作りません。best-effort metadata欠落時は相関不明やpendingが残る場合があります。

履歴はページメモリだけに保持します。Server TTFA統計はcompletedかつ計測済みのturnが対象で、interrupted／cancelled／failedは除外します。音声の`generation.done`だけではcompleted統計へ入れず、conversation完了を待ちます。p50／p95はnearest-rankで、min／max／件数も表示します。text-onlyに音声TTFAはありません。

## ユーザー報告の初期実マイク観測

NVIDIA GeForce GTX 1660 6GB、whisper.cpp、CUDA、small、persistent runtime。完了4ターン、割り込み1ターンを除外。報告されたServer TTFAは**p50 2.11秒、p95 2.40秒、min 2.09秒、max 2.40秒**です。

| Turn | Server TTFA | STT | LLM |
| --- | --- | --- | --- |
| 1 | 2.11 s | 1.58 s | 273 ms |
| 2 | 2.40 s | 1.50 s | 479 ms |
| 3 | 2.12 s | 1.39 s | 326 ms |
| 5 | 2.09 s | 1.27 s | 795 ms |

数値はリリース準備時にプロジェクト所有者から提供されました。raw event trace、丸め前の値、provider／networkの全設定、当時の集計実装は未提供です。表示された丸め後TTFAにnearest-rankを適用するとp50 2.11秒・p95 2.40秒ですが、補間medianでは異なります。報告値を保持し、raw sampleやTTS時間を捏造しません。2.09秒の行は丸め後の内訳合計がtotalをわずかに超えます。raw traceがないため丸めや境界差の原因を確定できません。

極めて少数の実マイクdemo観測で、**管理されたベンチマークではなく**、遅延保証や比較性能の根拠ではありません。Step 10で独立した再測定はしていません。networkや外部LLM／providerの動作に影響されます。当時のServer TTFAという報告名を可聴時間へ読み替えません。現在のブラウザは上表の通知間隔として定義しています。

## 再測定と過去資料

[起動手順](quickstart.ja.md)に従い、Engine／SDK commit、CPU／GPU／driver、whisper revision／model hash／device、音声長、endpoint設定、先行生成状態、LLM／network、相関付きraw eventを記録します。cold／warm STT、生成完了、再生完了を分離してください。十分な完了件数と除外件数を報告し、許可なく文字起こしや録音を公開しないでください。

`go run ./cmd/stt-bench -device cuda -mode persistent -model small -durations 2,5,10,30 -repeat 2`はprovider側fixture測定で、マイクからのend-to-endではありません。過去の[CPU測定](stt-performance.md)は無音fixtureと異なるhardwareです。今回の観測と混ぜたり、CPU/CUDA高速化率を推論したりしないでください。
