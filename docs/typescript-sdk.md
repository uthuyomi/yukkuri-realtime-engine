# TypeScript SDK

`@yukkuri-realtime/client` 0.1.0 is a Public API v1 client. It supports modern browsers and Node.js 22+, uses ESM and includes declarations. No runtime dependency is required: core uses fetch, WebSocket, AbortController and typed arrays. Node's built-in WebSocket is available in [Node 22](https://nodejs.org/download/release/v22.15.0/docs/api/globals.html). Electron main can use the Node target; renderer uses the browser target and the Engine's Origin policy.

## Build and local installation

From this checkout:

```powershell
cd sdk/typescript
npm ci
npm run build
npm test
npm pack
```

No npm publication is performed. In an application, run `npm install /path/to/yukkuri-realtime-engine/sdk/typescript` after building it, or install the generated `.tgz`. The package includes dist JS, declarations and the Worklet asset. Source/test/build tooling is not needed at runtime. If your application's bundler moves assets, copy the exported `@yukkuri-realtime/client/audio-worklet.js` and pass its deployed URL to the player.

Exports: package root for core, `/browser` for optional browser helpers, `/audio-worklet.js` for the Worklet asset. There is no redundant `/node` implementation: Node and browser share the same core. CommonJS consumers can use dynamic `import()`; a CJS bundle is not supplied.

```ts
import {YukkuriClient} from '@yukkuri-realtime/client';
const client = new YukkuriClient({baseUrl: 'http://127.0.0.1:8765'});
await client.health();
const audio = await client.speak('ゆっくりしていってね');
// audio.audio: Uint8Array; audio.format; audio.contentType; audio.requestId
```

`speak(text, {voice?, speed?, provider?, signal?, timeoutMs?})` sends only supported HTTP fields. It buffers the completed response, propagates transport failures, and checks the RIFF length of WAV responses. A truncated WAV is `incomplete_audio`. For raw PCM, v1 has no final checksum/declared duration: a provider that ends a raw stream early without a transport error cannot always be detected. The SDK does not claim otherwise.

## Capabilities and errors

`await client.capabilities()` caches discovery for 30 seconds by default. `capabilities({refresh:true})` bypasses the cache; `capabilitiesTTLms` changes the TTL. Each connection also validates session.created's version/capabilities. TTS, STT and conversation calls check required configured feature versions; audio conversation requires credit-v1. Missing optional features do not prevent text-only connections.

`YukkuriError` unifies HTTP/WS errors: `code`, `message`, `recoverable`, `requestId`, `eventId`, `relatedEventId`, `generationId`. For WS, requestId identifies the HTTP handshake; relatedEventId identifies the individual operation. Network errors never dump raw server bodies. Local codes include `connection_error`, `connection_closed`, `unsupported_protocol`, `protocol_error`, `cancelled`, `incomplete_audio`, `microphone_error`; server codes retain their v1 meaning. Unknown server events are delivered to `event` and otherwise ignored.

## Realtime

```ts
const session = await client.realtime.connect();
session.on('transcript', e => console.log(e.text));
session.on('textDelta', e => console.log(e.delta));
session.on('error', e => console.error(e.code, e.message));
try {
  const generation = await session.sendText('札幌について教えて', {output: 'text'});
  await generation.done;
} finally { await session.close(); }
```

`sendText` commits user text to Conversation Runtime; it is not client-supplied response generation. It resolves with a server-owned `Generation` after generation.created. `generation.done` resolves to `done` or `cancelled`; it means generation/send completion, **not speaker drain**. `generation.cancel()` / `session.cancelGeneration()` scope cancellation to the tracked ID. A supplied stale ID does not cancel a newer generation.

Session operations:

| Method | Meaning |
| --- | --- |
| `startAudioInput({mode:'realtime'})` | Continuous mono PCM16 16k input with VAD metadata; requires endpoint runtime |
| `startAudioInput({mode:'manual'})` | Legacy manually committed voice conversation input |
| `sendAudio(Uint8Array, options?)` | Raw little-endian PCM only, split into bounded messages; network backpressure |
| `commitInput()` | Commit manual realtime voice input, triggering STT/conversation |
| `cancelInput()` / `stopAudioInput()` | Cancel/discard input; stop before switching to sendText |
| `cancelGeneration(id?)` | Cancel tracked/matching output |
| `ackPlayed(sourceFrames, id?)` | Report cumulative actual rendered source frames |
| `playbackSnapshot()` | Advanced inspection of C/R/P/B source-frame snapshot |
| `close()` | Idempotent bounded session.close handshake, then close transport |
| `sendEvent(ClientEvent)` | Advanced typed v1 wire access, not a separate protocol |

Raw continuous input users send VAD metadata through `sendEvent`; ordinary browser applications use BrowserMicrophone. Realtime start has no server ACK in v1, so late start errors arrive through `error`. Transcription start has an ACK and is awaited.

High-level events: `connected`, `transcript`, `textDelta`, `textDone`, `audio`, `generationStarted`, `generationDone`, `interruption`, `backchannel`, `error`, `closed`, plus typed raw `event`. `connect()` resolves after the connected transition; register normal listeners on that returned active session. `on()` returns unsubscribe. Callbacks run synchronously in receive order; handle application exceptions in the callback. A thrown callback cannot corrupt protocol parsing. Async callback promises are application-owned.

Types include `Capabilities`, `PublicError`, `AudioFormat`, `Transcript`, `AudioPacket`, `Generation`, `ClientEvent`, `RealtimeEvent`, `ServerEvent`, `PROTOCOL_VERSION`. Known RealtimeEvent members form a discriminated union; ServerEvent additionally permits unknown extension envelopes. ClientEvent is generated from the shared structural schema using `node scripts/wire-types.mjs`; tests run its `--check`. Schema is not a complete state machine.

## Output audio and credit

The SDK joins response.audio.delta with the immediately following binary. Missing metadata, wrong lengths/format/offset/packet sequence, missing binary at close, or a missing-binary timeout are protocol errors. Stale generation pairs are consumed and discarded together. No Blob conversion promises can reorder messages: sockets request `arraybuffer`.

Credit-v1 is negotiated before generation. A two-second source-frame window is initialized from chunk.started, receipt advances R, and only explicit/render ACK advances P. Every credit is a snapshot `{C,R,P,B=R-P}`, never an additive grant. For Node/external players:

```ts
session.on('audio', packet => externalPlayer.enqueue(packet));
// In your player's actual render/drain callback, NOT the receive callback:
externalPlayer.onRendered((generationId, cumulativeSourceFrames) => {
  session.ackPlayed(cumulativeSourceFrames, generationId);
});
```

Keep generation IDs attached to external playback. Output device frames must be converted to native source frames by the external player's resampler. Old generation ACKs are ignored. Without playback ACK, output stalls at the bounded window and server credit timeout; saving realtime audio to disk is not playback and must not fake ACKs. Use HTTP speak for saving standalone TTS.

## Browser voice

```ts
import {BrowserAudioPlayer, BrowserMicrophone, sileroVoiceFactory} from '@yukkuri-realtime/client/browser';
// Inside a click handler, resume context BEFORE network waits (autoplay policy).
const context = new AudioContext();
await context.resume();
const session = await client.realtime.connect();
const player = await BrowserAudioPlayer.create(session, {context});
const microphone = new BrowserMicrophone(session, {
  player,
  voiceFactory: sileroVoiceFactory(options => MicVAD.new(options), {
    baseAssetPath: '/assets/vad/', onnxWASMBasePath: '/assets/onnx/'
  })
});
await microphone.start();
// Later: await microphone.close(); await player.close(); await session.close(); await context.close();
```

Create the player before the first generation. It reuses the canonical SDK Worklet's fixed ring, stateful resampler, source accounting, pause/resume and generation isolation. Only Worklet rendered native source positions call ackPlayed. generation.done flushes the resampler tail; final drain reaches exactly the total source frames. Receipt while paused does not count as playback. Matching recovery resumes preserved PCM, cancellation clears it, and a bounded decision watchdog requests authoritative interruption failure. Disconnect cleans up audio and microphone. A supplied AudioContext remains caller-owned; a helper-created context is closed by the helper.

BrowserMicrophone acquires getUserMedia with echoCancellation/noiseSuppression/autoGainControl. Silero provides continuous resampled 16 kHz frames, including silence, and speech_start/end/misfire metadata. PCM and VAD metadata share an ordered input queue; pending input is bounded to 256 KiB. Startup cancellation releases late-arriving tracks. No custom AEC/NS is implemented.

Silero is optional and injected: no ML dependency or automatic CDN fetch occurs in the package. The example explicitly loads vad-web 0.0.31 and onnxruntime-web 1.22.0 from CDN, matching the prior example. Production apps can self-host those model/WASM/Worklet assets and set CSP accordingly. VAD/ONNX downloads dominate size; they are not bundled with core. A custom VoiceFactory must supply already-resampled 16 kHz float frames and destroy its resources. See the [VAD project](https://github.com/ricky0123/vad).

## Transcription

```ts
const transcript = await client.transcribe(pcm16Mono16k);
const s = await client.transcription.connect();
try {
  await s.start();
  await s.sendAudio(pcm16Mono16k);
  console.log((await s.commit()).text);
} finally { await s.close(); }
```

`commit()` resolves only a final transcript, correlated to that commit. `cancel()` cancels pending input/STT and rejects the pending commit. The server may briefly report resource_limit while a cancelled provider is exiting; do not silently retry a submitted turn. `wavToPCM(wav)` validates/dechunks PCM16 mono16k RIFF/WAVE; no channel conversion or resampling is pretended. WAV headers are never sent as PCM.

## Timeouts, cancellation and connection lifecycle

Client defaults (milliseconds): HTTP 130000, connect 10000, close 5000, operation 130000. Each is configurable. HTTP/connection/transcription methods accept `signal` and `timeoutMs`. sendText's timeout covers acceptance; its AbortSignal remains attached through generation completion and requests generation.cancel. Use generation.cancel after acceptance when no signal was supplied. Cancelling acceptance before an ID is known closes that session to avoid cancelling an unrelated future generation.

`connecting → active → closing → closed`; disconnect is terminal and observable, pending requests fail, no reconnect/replay/restoration occurs. Create a new session explicitly. close discards unfinished work; it does not drain the speaker. Wait for your player's drain if completing playback matters.

Examples from repo root after SDK build:

```sh
node examples/typescript/simple-tts/main.mjs
node examples/typescript/transcription/main.mjs input.wav
node examples/typescript/realtime-text/main.mjs
python -m http.server 8080 --bind 127.0.0.1
```

Then open `http://127.0.0.1:8080/examples/typescript/browser-voice/`. Serve the repo root, because the example imports the locally built SDK. `examples/browser/realtime-test.html` links here; the old raw protocol diagnostic remains under `legacy-realtime-test.html`, using the same canonical Worklet.
