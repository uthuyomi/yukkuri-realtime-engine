# Browser Voice

リポジトリルートを localhost の HTTP サーバーで配信し、このディレクトリの
`index.html` を開いてください。先に `sdk/typescript` で `npm run build` を実行します。
音声入力には Engine、SmartTurn、STT、LLM、TTS とマイクの許可が必要です。
Silero / ONNX の既存 CDN 読み込み以外に UI ライブラリは追加していません。

## 履歴と計測

`history.mjs` の `TurnHistory` がテキスト、状態、計測値を turn 単位で管理します。
正式な `generation.created` でのみ履歴に公開し、`conversation.item.updated` の
`turn_id` / `generation_id` で音声入力を結合します。結合キーには session ID も含めます。
メタデータが遅れて到着しても再計算し、相関情報を取得できない場合は推測で補いません。
テキスト入力は `sendText` が返した generation に入力文を対応付けます。
speculation の通知だけでは履歴を作らず、promotion 後の正式イベントを使います。

- EOT はサーバーの `input_audio.turn` / `complete` 通知の生成時刻です。
- STT は EOT → `input_audio.transcript.final`、LLM は final → 最初の text delta の通知間隔です。
- Server TTFA は EOT → 最初の `response.audio.chunk.started` の通知間隔です。
  この通知は合成後に発行されます。プロバイダー内部の first-audio-ready 時刻そのものではありません。
- Client TTFA はブラウザ内の EOT 通知受信 → 最初の PCM 受信です。
  サーバーとブラウザの時計を混ぜません。ネットワーク片道時間や可聴開始時間を表しません。
- TTS 開始と Audible TTFA は現行イベントから取得できず、未計測です。
- 割り込みは同じ generation / interruption ID の suspected → confirmed 通知間隔です。
  音声停止までの時間ではありません。
- total は EOT → 生成終了通知です。EOT がないテキスト入力は generation.created を起点にします。
  音声再生完了までの時間ではありません。

音声の `generation.done` は再生完了を意味しません。音声turnは conversation の
completed 通知を待って通常統計に含めます。失敗後の cancelled 通知でも failed を保持します。
統計は completed かつ Server TTFA 計測済みの turn のみ、p50 / p95 は nearest-rank です。
ナノ秒精度のサーバー時刻を整数で差分計算してから ms に変換し、表示時だけ丸めます。
未取得・対象外は `—` と表示します。

履歴は再接続後もページ内メモリに保持され、再読み込みで消えます。
localStorage などの永続化は使用しません。

## 検証

リポジトリルートで `node --test examples/typescript/browser-voice/history.test.mjs`。
相関イベントの順序逆転、generation / session の分離、raw precision、終端状態、
speculation、欠測、パーセンタイルを検証します。
