# Providers

[English](providers.md) | 日本語

実装は`internal/providers/`にあります。STTは`Transcribe`、LLMは`Generate`／streamの`Recv`、TTSは`Synthesize`、turn検出は`Detect`、相づちは`Classify`を実装します。EngineはTTSを登録し、他の境界はtransportのsetterで接続します。Goによる構成境界であり、公開dynamic-plugin loaderではありません。利用可否は`/v1/capabilities`で確認しますが、設定済みでもremote API／sidecarの成功を保証しません。

## AquesTalk / AqKanji2Koe

Windows adapterはAquesTalk1 DLLを動的ロードし、AqKanji2Koeで日本語textを変換してmono 8 kHz WAVを返します。既定声種はf1、設定声種はf1/f2/f3/m1/m2/r1/dvd/imd1/jgr。標準起動プログラムは全設定DLLと辞書を必要とします。[配置先](quickstart.ja.md)を参照してください。

AquesTalkとAqKanji2Koeは第三者のプロプライエタリソフトウェアです。このリポジトリからDLL、辞書、SDKライブラリ／ヘッダー、キーなどの制限付きSDK資産を再配布しません。各自で取得し、AQUESTの条件に従ってください。独自コードとプロジェクト作成文書の[MIT License](../LICENSE)は、これらの資産の権利を付与せず、AQUESTのライセンスを変更しません。検証済みのソース候補と整理後のローカル到達可能履歴には含まれません。Step 10-DではGitHubの公開mainも置換し、これらの資産が到達不能であることを新規cloneで確認しました。GitHub内部の保存物やキャッシュの消去を保証するものではありません。[リリース報告](release-quality.md)を参照してください。配布元の[AquesTalk製品ページ](https://www.a-quest.com/products/aquestalk.html)、[AqKanji2Koe製品ページ](https://www.a-quest.com/products/aqkanji2koe.html)とそのライセンス案内を確認してください。この文書で個別の法的権利を判断しません。

`speed`は倍率です。省略／0は100%、正数は100倍して整数のpercentに変換し、50〜300%を受け付けます。通常速度は`100`でなく`1.0`です。未知の声種、変換失敗、native合成失敗は情報を制限したgeneration errorになります。変換出力バッファは8192 bytesで、長文や複雑な入力は変換に失敗する場合があります。native呼出しは同期処理で、実行途中のDLL処理をcontext cancelで中断できません。DLLの並行動作は利用者の許諾済みbuildで確認が必要です。

Go設定には`DevKey`、`UsrKey`、`Kanji2KoeDevKey`がありますが、標準実行ファイルは設定せず、対応環境変数も公開していません。秘密はソースやartifactに含めません。資産欠落／不正時はTTSを無効化し、healthやtext会話は独立して利用できます。

## whisper.cpp STT

既定は常駐・確定結果のみ・small・日本語です。1つのupstream whisper-serverと直列推論contextをsession間で共有します。process方式は要求ごとにwhisper-cliを使う診断／互換用で、既定ではありません。固定revisionとセットアップは[STT runtime](stt-runtime.md)を参照してください。

| STT_DEVICE | 動作 |
| --- | --- |
| auto | CUDAロードと実推論を確認し、初期化失敗なら回収後CPUを試します。要求中のCUDA失敗はその要求を失敗させ、次の要求で切り替えます。 |
| cpu | CPU実行ファイルと`-ng`を使い、CUDAを試しません。 |
| cuda | CUDA起動・モデル割当の証拠・実推論成功を必要とします。CPUへfallbackしません。 |

device選択でモデルは変えません。`STT_MODEL`は既知のファイル名形式、`STT_MODEL_PATH`は利用者が用意したcustomモデルを選びます。モデルの同梱やEngine起動時の自動取得はありません。実行ファイル／モデル欠落時はSTTを無効化します。cancel／errorでchildを終了・回収し、次の対象要求で再ロードします。初期化失敗後はEngine再起動が必要です。queue／resource上限は[設定](configuration.ja.md)を参照してください。

## Smart Turn

Goの`smartturn`は既定で`http://127.0.0.1:8766/predict`を呼びます。loopback HTTP限定、timeout 3秒、redirect禁止です。CPU ONNXサイドカーは直近最大8秒のPCM16 mono16kから終了確率を返します。STTではなくendpoint判定です。setupはモデルrevision／hashと依存を固定します。既存ライセンス表示は`tools/turn-detector/SMART-TURN-LICENSE`に保持しています。取得モデル・依存には個別の条件があります。

capability設定時に到達確認はしません。停止・busy時には上限付きのendpoint失敗／fallback処理が働きます。ログとsidecar healthを確認してください。VADの責任はクライアントにあります。

## LLMと相づち

現在のLLM実装はOpenAI Responses SSE（`/responses`）です。`OPENAI_API_KEY`、`OPENAI_MODEL`で設定します。ソースの既定モデルは`gpt-5.6-luna`で、利用者のアカウントで使えるモデルを設定してください。外部providerへ会話内容を送信し、API料金やネットワーク条件はEngine外部の要因です。キーがなければ会話は無効です。`BaseURL`はGo設定であり環境変数ではありません。通常LLM要求はcancelに従いますが、サーバーの固定総応答deadlineはありません。

`multisignal-ja-v1`は時間・音響・endpoint確率・任意の意味的証拠を使う相づちヒューリスティックです。音響特徴による復帰は既定onで、無効化できます。学習済み意図認識ではなく、短い訂正は曖昧になる場合があります。復帰では既存PCM／履歴を再開し、真の割り込みではcancelします。

## 第三者のライセンス

whisper.cpp、Smart Turn、Silero VAD/vad-web、ONNX Runtime、coder/websocket、SDK／ブラウザの依存ソフトウェア、取得モデルには、それぞれのライセンスと条件が適用されます。本プロジェクトのMITによって、これらのライセンスを変更するものではありません。
