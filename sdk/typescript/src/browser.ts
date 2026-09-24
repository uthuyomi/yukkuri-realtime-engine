import type { RealtimeSession } from './session.js';
import { failure, YukkuriError } from './errors.js';

/** Optional browser-only player. Create/resume from a user gesture. */
export class BrowserAudioPlayer {
  private off: (() => void)[] = [];
  private pauseToken?: {generationId: string; interruptionId: string};
  private watchdog?: ReturnType<typeof setTimeout>;
  private disposed = false;
  private constructor(readonly session: RealtimeSession, readonly context: AudioContext, readonly node: AudioWorkletNode, private ownsContext: boolean) {
    const post = (value: unknown) => node.port.postMessage(value);
    this.off.push(session.on('generationStarted', g => { this.clearPause(); post({type: 'generation', generationId: g.id}); }),
      session.on('audio', packet => {
        const samples = new Float32Array(packet.pcm.length / 2);
        const view = new DataView(packet.pcm.buffer, packet.pcm.byteOffset, packet.pcm.byteLength);
        for (let i = 0; i < samples.length; i++) samples[i] = view.getInt16(i * 2, true) / 32768;
        const d = packet.metadata;
        node.port.postMessage({type: 'audio', generationId: packet.generationId, samples, sourceRate: d.sample_rate,
          sourceFrames: d.source_frames, sourceStartFrame: d.source_start_frame, speechSequence: d.speech_sequence, audioSequence: d.audio_sequence}, [samples.buffer]);
      }), session.on('event', e => {
        const generationId = e.generation_id;
        if (e.type === 'response.audio.chunk.started') post({type: 'format', generationId, sourceRate: e.data.sample_rate});
        if (e.type === 'generation.done' && e.data.source_frames !== undefined) post({type: 'done', generationId, sourceFrames: e.data.source_frames});
        if (e.type === 'generation.cancelled' || e.type === 'interruption.confirmed') {
          post({type: 'clear', generationId}); if (generationId === this.pauseToken?.generationId) this.clearPause();
        }
        if (e.type === 'interruption.suspected' && generationId === session.generation?.id && !this.pauseToken)
          this.pause(String(e.data.interruption_id));
        if (e.type === 'interruption.recovered' && generationId === this.pauseToken?.generationId && e.data.interruption_id === this.pauseToken?.interruptionId) this.resume();
      }), session.on('closed', () => { void this.close(); }));
    node.port.onmessage = ({data: m}) => {
      if (this.disposed || session.state !== 'active' || m.generationId !== session.generation?.id) return;
      try {
        // Worklet receipt is not playback. Only native rendered source positions advance ACK.
        if (['playback.credit', 'playback.progress', 'playback.completed', 'playback.paused'].includes(m.type))
          session.ackPlayed(m.playedSourceFrames, m.generationId);
        if (m.type === 'playback.paused') session.sendEvent({type: 'playback.paused', generation_id: m.generationId,
          data: {interruption_id: m.interruptionId, played_source_frames: m.playedSourceFrames}});
        if (m.type === 'playback.overflow') session.sendEvent({type: 'playback.overflow', generation_id: m.generationId});
      } catch { session.cancelGeneration(m.generationId); post({type: 'clear', generationId: m.generationId}); }
    };
    node.connect(context.destination);
  }
  static async create(session: RealtimeSession, options: {context?: AudioContext; workletUrl?: string | URL} = {}): Promise<BrowserAudioPlayer> {
    if (session.generation) throw failure('invalid_state', 'Attach playback before starting the first generation.');
    const context = options.context ?? new AudioContext();
    try {
      await context.audioWorklet.addModule(options.workletUrl ?? new URL('./audio-worklet.js', import.meta.url));
      await context.resume();
      return new BrowserAudioPlayer(session, context, new AudioWorkletNode(context, 'pcm-player', {numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [1]}), !options.context);
    } catch (e) { if (!options.context) await context.close(); throw e; }
  }
  pause(interruptionId = `pause_${Date.now()}_${++pauseSequence}`): {generationId: string; interruptionId: string} | undefined {
    const generationId = this.session.generation?.id;
    if (!generationId) return;
    if (this.pauseToken?.generationId === generationId) return this.pauseToken;
    this.clearPause(); const token = {generationId, interruptionId}; this.pauseToken = token;
    this.node.port.postMessage({type: 'pause', ...token});
    this.watchdog = setTimeout(() => {
      if (this.pauseToken !== token) return;
      if (this.session.state === 'active') this.session.sendEvent({type: 'interruption.failed', generation_id: generationId, data: {interruption_id: interruptionId}});
      this.node.port.postMessage({type: 'clear', generationId}); this.clearPause();
    }, this.session.interruptionTimeoutMs + 1000);
    return token;
  }
  /** Only call for an authoritative matching recovery; the helper does this automatically. */
  resume(): void { if (this.pauseToken) this.node.port.postMessage({type: 'resume', ...this.pauseToken}); this.clearPause(); }
  private clearPause(): void { clearTimeout(this.watchdog); this.pauseToken = undefined; }
  async close(): Promise<void> {
    if (this.disposed) return; this.disposed = true; this.clearPause(); this.off.forEach(fn => fn());
    this.node.port.postMessage({type: 'clear'}); this.node.port.onmessage = null; this.node.disconnect(); this.node.port.close();
    if (this.ownsContext) await this.context.close();
  }
}
let pauseSequence = 0;
export interface VoiceCallbacks { frame: (frame: Float32Array) => void; speechStart: () => void; speechEnd: () => void; misfire: () => void }
export interface VoiceDetector { start(): Promise<void> | void; destroy(): Promise<void> | void }
export type VoiceFactory = (stream: MediaStream, callbacks: VoiceCallbacks) => Promise<VoiceDetector>;
/** Inject @ricky0123/vad-web's MicVAD.new and explicitly hosted assets. No CDN/ML dependency in core. */
export function sileroVoiceFactory(create: (options: any) => Promise<VoiceDetector>, assets: {baseAssetPath: string; onnxWASMBasePath: string}): VoiceFactory {
  return (stream, callbacks) => create({...assets, startOnLoad: false, model: 'v5', redemptionMs: 160, minSpeechMs: 96,
    getStream: async () => stream, resumeStream: async () => stream, pauseStream: async () => {},
    onFrameProcessed: (_probabilities: unknown, frame: Float32Array) => callbacks.frame(frame),
    onSpeechStart: callbacks.speechStart, onSpeechEnd: callbacks.speechEnd, onVADMisfire: callbacks.misfire});
}
export class BrowserMicrophone {
  private stream?: MediaStream; private detector?: VoiceDetector; private epoch = 0;
  private running = false; private starting?: Promise<void>; private speaking = false;
  private inputAbort?: AbortController;
  private inputStarted = false; private pendingBytes = 0; private sending = Promise.resolve();
  private off: (() => void)[];
  constructor(readonly session: RealtimeSession, private options: {voiceFactory: VoiceFactory; player?: BrowserAudioPlayer; onError?: (error: YukkuriError) => void; mediaDevices?: Pick<MediaDevices, 'getUserMedia'>}) {
    this.off = [session.on('closed', () => { void this.stop(); }), session.on('error', () => { void this.stop(); }),
      session.on('generationStarted', () => {
        if (!this.speaking) return;
        const token = options.player?.pause();
        if (token) session.sendEvent({type: 'interruption.suspected', generation_id: token.generationId, data: {interruption_id: token.interruptionId}});
      })];
  }
  start(): Promise<void> {
    if (this.running) return Promise.resolve(); if (this.starting) return this.starting;
    const epoch = ++this.epoch;
    this.starting = (async () => {
      let stream: MediaStream | undefined, detector: VoiceDetector | undefined;
      try {
        if (this.session.state !== 'active') throw failure('invalid_state', 'Connect the session before starting the microphone.');
        stream = await (this.options.mediaDevices ?? navigator.mediaDevices).getUserMedia({audio: {echoCancellation: true, noiseSuppression: true, autoGainControl: true}, video: false});
        if (epoch !== this.epoch) { stream.getTracks().forEach(t => t.stop()); return; }
        this.stream = stream;
        detector = await this.options.voiceFactory(stream, {frame: frame => this.frame(frame),
          speechStart: () => { if (!this.running || this.speaking) return; this.speaking = true;
            const token = this.options.player?.pause();
            this.control(() => this.session.sendEvent({type: 'input_audio.speech_start', generation_id: token?.generationId, data: token ? {interruption_id: token.interruptionId} : {}})); },
          speechEnd: () => { if (this.running) { this.speaking = false; this.control(() => this.session.sendEvent({type: 'input_audio.speech_end'})); } },
          misfire: () => { if (this.running) { this.speaking = false; this.control(() => this.session.sendEvent({type: 'input_audio.vad_misfire'})); } }});
        if (epoch !== this.epoch) { await detector.destroy(); stream.getTracks().forEach(t => t.stop()); return; }
        this.detector = detector; this.session.startAudioInput(); this.inputAbort = new AbortController(); this.inputStarted = true; this.running = true;
        await detector.start();
        if (epoch !== this.epoch) {
          try { await detector.destroy(); } catch { /* A concurrent stop may already have destroyed it. */ }
          stream.getTracks().forEach(t => t.stop());
        }
      } catch (e) { await this.stop(); throw e instanceof YukkuriError ? e : failure('microphone_error', 'Microphone or VAD startup failed.'); }
      finally { this.starting = undefined; }
    })(); return this.starting;
  }
  private frame(frame: Float32Array): void {
    if (!this.running) return;
    const bytes = new Uint8Array(frame.length * 2), v = new DataView(bytes.buffer);
    for (let i = 0; i < frame.length; i++) { const n = Math.max(-1, Math.min(1, frame[i])); v.setInt16(i * 2, n < 0 ? n * 32768 : n * 32767, true); }
    if (this.pendingBytes + bytes.length > 256 * 1024) { this.options.onError?.(failure('resource_limit', 'Microphone send queue is full.')); void this.stop(); return; }
    const epoch = this.epoch; this.pendingBytes += bytes.length;
    this.sending = this.sending.then(async () => { if (this.running && epoch === this.epoch) await this.session.sendAudio(bytes, {signal: this.inputAbort?.signal}); })
      .catch(() => { if (this.running && epoch === this.epoch) { this.options.onError?.(failure('connection_error', 'Microphone input failed.')); void this.stop(); } })
      .finally(() => { this.pendingBytes -= bytes.length; });
  }
  private control(send: () => unknown): void {
    const epoch = this.epoch;
    this.sending = this.sending.then(() => { if (this.running && epoch === this.epoch) send(); })
      .catch(() => { void this.stop(); });
  }
  async stop(): Promise<void> {
    ++this.epoch; this.running = false; this.speaking = false;
    this.inputAbort?.abort(); this.inputAbort = undefined;
    if (this.inputStarted && this.session.state === 'active') this.session.stopAudioInput(); this.inputStarted = false;
    const detector = this.detector, stream = this.stream; this.detector = undefined; this.stream = undefined;
    try { await detector?.destroy(); } catch { /* Release tracks even when VAD teardown fails. */ }
    finally { stream?.getTracks().forEach(t => t.stop()); }
  }
  async close(): Promise<void> { this.off.forEach(fn => fn()); await this.stop(); }
}
