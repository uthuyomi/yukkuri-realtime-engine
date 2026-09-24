import type { YukkuriClient } from './client.js';
import type { ClientEvent } from './client-events.js';
import { YukkuriError, failure, publicError } from './errors.js';
import type { AudioMetadata, AudioPacket, Capabilities, CreditSnapshot, Envelope, OperationOptions, ServerEvent, Transcript } from './types.js';

export interface SessionEvents {
  connected: Session; event: ServerEvent; transcript: Transcript;
  textDelta: {delta: string; generationId: string}; textDone: {generationId: string};
  audio: AudioPacket; generationStarted: Generation; generationDone: Generation;
  interruption: Envelope; backchannel: Envelope; error: YukkuriError;
  closed: {code: number; cleanupComplete?: boolean};
}
type Waiter = {match: (e: Envelope) => boolean; resolve: (e: Envelope) => void; reject: (e: YukkuriError) => void; related?: string};
let sequence = 0;
const id = () => `sdk_${Date.now()}_${++sequence}`;
export class Generation {
  state: 'generating' | 'done' | 'cancelled' = 'generating';
  readonly done: Promise<'done' | 'cancelled'>;
  private finish!: (state: 'done' | 'cancelled') => void;
  private reject!: (e: YukkuriError) => void;
  constructor(readonly id: string, private session: Session) {
    this.done = new Promise((resolve, reject) => { this.finish = resolve; this.reject = reject; });
    void this.done.catch(() => {});
  }
  cancel(): void { this.session.cancelGeneration(this.id); }
  /** @internal */ complete(state: 'done' | 'cancelled'): void { if (this.state !== 'generating') return; this.state = state; this.finish(state); }
  /** @internal */ fail(e: YukkuriError): void { this.reject(e); }
}
export class Session {
  state: 'connecting' | 'active' | 'closing' | 'closed' = 'connecting';
  id = ''; capabilities!: Capabilities; generation?: Generation;
  requestId?: string;
  interruptionTimeoutMs = 1500;
  protected socket?: WebSocket;
  private listeners = new Map<keyof SessionEvents, Set<(event: any) => void>>();
  private waiters = new Set<Waiter>();
  private pending?: Envelope<string, AudioMetadata>;
  private pairTimer?: ReturnType<typeof setTimeout>;
  private closeTask?: Promise<void>;
  private cleanupComplete?: boolean;
  private input = false;
  protected continuousInput = false;
  private terminal?: YukkuriError;
  private audio?: {id: string; rate: number; received: number; played: number; capacity: number; chunk: number; packet: number};
  constructor(protected client: YukkuriClient, readonly transcriptionOnly = false) {}
  on<K extends keyof SessionEvents>(name: K, listener: (value: SessionEvents[K]) => void): () => void {
    const set = this.listeners.get(name) ?? new Set(); this.listeners.set(name, set); set.add(listener);
    return () => set.delete(listener);
  }
  protected emit<K extends keyof SessionEvents>(name: K, value: SessionEvents[K]): void {
    for (const fn of this.listeners.get(name) ?? []) {
      // User callback failures must not reorder transport messages or corrupt pairing.
      try { fn(value); } catch { /* Applications own callback errors. */ }
    }
  }
  private wait(match: (e: Envelope) => boolean, options: OperationOptions = {}, related?: string, onCancel?: () => void): Promise<Envelope> {
    return new Promise((resolve, reject) => {
      const done = () => { clearTimeout(timer); options.signal?.removeEventListener('abort', abort); this.waiters.delete(w); };
      const w: Waiter = {match, related, resolve: e => { done(); resolve(e); }, reject: e => { done(); reject(e); }};
      const cancel = (code: string) => { w.reject(failure(code, code === 'timeout' ? 'Operation timed out.' : 'Operation cancelled.')); onCancel?.(); };
      const abort = () => cancel('cancelled');
      const timer = setTimeout(() => cancel('timeout'), options.timeoutMs ?? this.client.options.operationTimeoutMs);
      this.waiters.add(w); options.signal?.addEventListener('abort', abort, {once: true});
      if (options.signal?.aborted) abort();
      else if (this.state === 'closed') w.reject(this.terminal ?? failure('connection_closed', 'Session is closed.'));
    });
  }
  async connect(options: OperationOptions = {}): Promise<void> {
    if (this.socket || this.state !== 'connecting') throw failure('invalid_state', 'Create a new session to connect again.');
    const url = new URL(this.client.baseUrl + (this.transcriptionOnly ? '/v1/transcription' : '/v1/realtime'));
    url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:';
    const opts = {...options, timeoutMs: options.timeoutMs ?? this.client.options.connectTimeoutMs};
    if (options.signal?.aborted) throw failure('cancelled', 'Connection cancelled.');
    try {
      this.socket = this.client.options.webSocketFactory(url.href); this.socket.binaryType = 'arraybuffer';
      const created = this.wait(e => e.type === 'session.created', opts);
      this.socket.onmessage = event => {
        try { this.receive(event.data); } catch (e) { this.fatal(e instanceof YukkuriError ? e : failure('protocol_error', 'Invalid server event.')); }
      };
      this.socket.onerror = () => this.fatal(failure('connection_error', 'WebSocket connection failed.'));
      this.socket.onclose = event => this.finishClose(event.code);
      const first = await created;
      if (first.data.protocol_version !== '1') throw failure('unsupported_protocol', 'This SDK requires protocol version 1.');
      this.capabilities = first.data.capabilities as Capabilities;
      this.client.validateVersion(this.capabilities);
      if (this.transcriptionOnly) this.client.require(this.capabilities, 'transcription');
      this.id = first.session_id;
      this.requestId = typeof first.data.request_id === 'string' ? first.data.request_id : undefined;
      this.interruptionTimeoutMs = Number(first.data.interruption_timeout_ms) || 1500;
      this.state = 'active';
      const flow = this.capabilities.features.audio_flow_control;
      const config: any = {protocol_version: '1'};
      if (!this.transcriptionOnly && flow?.available && flow.version === 'credit-v1') config.audio_flow_control = 'credit-v1';
      await this.request('session.configure', config, 'session.configured', opts);
      this.emit('connected', this);
    } catch (e) {
      const err = e instanceof YukkuriError ? e : failure('connection_error', 'Connection failed.'); this.fatal(err); throw err;
    }
  }
  private receive(payload: unknown): void {
    if (payload instanceof ArrayBuffer) { this.binary(new Uint8Array(payload)); return; }
    if (typeof payload !== 'string' || payload.length > 1024 * 1024) throw failure('protocol_error', 'Invalid server message.');
    if (this.pending) throw failure('protocol_error', 'Audio metadata was not followed by binary PCM.');
    const e = JSON.parse(payload) as Envelope;
    if (!e || typeof e.type !== 'string' || typeof e.session_id !== 'string' || typeof e.event_id !== 'string' ||
      typeof e.timestamp !== 'string' || !e.data || typeof e.data !== 'object' || Array.isArray(e.data) ||
      (this.id && e.session_id !== this.id)) throw failure('protocol_error', 'Invalid event envelope.');
    this.handle(e);
    this.emit('event', e);
    for (const w of [...this.waiters]) if (w.match(e) && (!w.related || w.related === e.related_event_id)) w.resolve(e);
  }
  private handle(e: Envelope): void {
    const gen = e.generation_id ?? '';
    switch (e.type) {
      case 'session.closed': this.cleanupComplete = e.data.cleanup_complete === true; break;
      case 'error': {
        if (!publicError(e.data)) throw failure('protocol_error', 'Invalid error event.');
        const error = YukkuriError.from(e.data, e, this.requestId);
        for (const w of [...this.waiters]) if (!w.related || w.related === e.related_event_id) w.reject(error);
        if (gen && gen === this.generation?.id) this.generation.fail(error);
        if (!error.recoverable) this.fatal(error); else this.emit('error', error);
        break;
      }
      case 'generation.created':
        if (!gen) throw failure('protocol_error', 'Generation ID is missing.');
        this.generation?.complete('cancelled'); this.audio = undefined;
        this.generation = new Generation(gen, this); this.emit('generationStarted', this.generation); break;
      case 'generation.cancelled':
        if (gen === this.generation?.id) { this.generation.complete('cancelled'); this.audio = undefined; } break;
      case 'generation.done':
        if (gen === this.generation?.id) {
          if (e.data.source_frames !== undefined && e.data.source_frames !== (this.audio?.received ?? 0))
            throw failure('protocol_error', 'Incomplete source audio stream.');
          this.generation.complete('done'); this.emit('generationDone', this.generation);
        } break;
      case 'input_audio.transcript.final':
        if (typeof e.data.text !== 'string' || typeof e.data.language !== 'string' || typeof e.data.turn_id !== 'string') throw failure('protocol_error', 'Invalid transcript event.');
        this.emit('transcript', e.data as unknown as Transcript); break;
      case 'response.text.delta':
        if (typeof e.data.text !== 'string') throw failure('protocol_error', 'Invalid text delta.');
        this.emit('textDelta', {delta: e.data.text, generationId: gen}); break;
      case 'response.text.done': this.emit('textDone', {generationId: gen}); break;
      case 'response.audio.chunk.started':
        if (gen !== this.generation?.id || this.generation.state === 'cancelled') break;
        if (e.data.channels !== 1 || e.data.bits_per_sample !== 16 || !Number.isInteger(e.data.sample_rate) ||
          Number(e.data.sample_rate) < 1000 || Number(e.data.sample_rate) > 192000) throw failure('protocol_error', 'Unsupported output format.');
        if (!this.audio) { this.audio = {id: gen, rate: Number(e.data.sample_rate), received: 0, played: 0, capacity: Number(e.data.sample_rate) * 2, chunk: -1, packet: -1}; this.credit(); }
        else if (this.audio.rate !== e.data.sample_rate) throw failure('protocol_error', 'Source rate changed within generation.');
        break;
      case 'response.audio.delta':
        if (this.transcriptionOnly) throw failure('protocol_error', 'Transcription cannot produce audio.');
        this.pending = e as unknown as Envelope<string, AudioMetadata>;
        this.pairTimer = setTimeout(() => this.fatal(failure('protocol_error', 'Audio binary did not arrive.')), this.client.options.connectTimeoutMs); break;
      default:
        if (e.type.startsWith('interruption.')) this.emit('interruption', e);
        if (e.type === 'input_audio.backchannel') this.emit('backchannel', e);
    }
  }
  private binary(pcm: Uint8Array): void {
    const e = this.pending; this.pending = undefined; clearTimeout(this.pairTimer);
    if (!e) throw failure('protocol_error', 'Unexpected binary PCM without metadata.');
    const d = e.data;
    if (pcm.length !== d.bytes || pcm.length !== d.source_frames * 2 || d.channels !== 1 || d.bits_per_sample !== 16 ||
      !Number.isSafeInteger(d.source_frames) || d.source_frames <= 0) throw failure('protocol_error', 'PCM metadata length or format mismatch.');
    if (!this.generation || e.generation_id !== this.generation.id || this.generation.state === 'cancelled') return; // Consume the old pair before discarding.
    const a = this.audio;
    if (!a || d.sample_rate !== a.rate || d.source_start_frame !== a.received || !Number.isSafeInteger(d.speech_sequence) || !Number.isSafeInteger(d.audio_sequence) ||
      (d.speech_sequence === a.chunk ? d.audio_sequence !== a.packet + 1 : d.speech_sequence !== a.chunk + 1 || d.audio_sequence !== 0))
      throw failure('protocol_error', 'PCM source position or packet sequence mismatch.');
    a.received += d.source_frames; a.chunk = d.speech_sequence; a.packet = d.audio_sequence;
    if (a.received - a.played > a.capacity) throw failure('resource_limit', 'Audio exceeded the playback window.');
    this.credit(); // Receipt is NOT playback.
    this.emit('audio', {generationId: a.id, pcm, metadata: d, format: {encoding: 'pcm_s16le', sample_rate: a.rate, channels: 1}});
  }
  private fatal(error: YukkuriError): void {
    if (this.state === 'closed') return;
    this.terminal = error; this.emit('error', error); this.socket?.close(); this.finishClose(1006);
  }
  private finishClose(code: number): void {
    if (this.state === 'closed') return;
    if (this.pending && !this.terminal) { this.terminal = failure('protocol_error', 'Connection closed before audio binary.'); this.emit('error', this.terminal); }
    this.state = 'closed'; clearTimeout(this.pairTimer); this.pending = undefined;
    const err = this.terminal ?? failure('connection_closed', 'Session closed.');
    for (const w of [...this.waiters]) w.reject(err);
    this.generation?.fail(err); this.audio = undefined;
    this.emit('closed', {code, cleanupComplete: this.cleanupComplete});
  }
  /** Advanced typed wire access. No reconnect or replay is performed. */
  sendEvent(event: ClientEvent): string {
    if (this.state !== 'active' || this.socket?.readyState !== 1) throw failure('invalid_state', 'Session is not active.');
    const eventId = event.event_id ?? id(); const payload = JSON.stringify({...event, event_id: eventId});
    if (new TextEncoder().encode(payload).length > (this.capabilities.limits.json_bytes ?? 65536)) throw failure('payload_too_large', 'JSON event is too large.');
    this.socket.send(payload); return eventId;
  }
  protected request(type: string, data: any, reply: string, options: OperationOptions = {}, onCancel?: () => void): Promise<Envelope> {
    if (options.signal?.aborted) return Promise.reject(failure('cancelled', 'Operation cancelled.'));
    const eventId = id();
    const result = this.wait(e => e.type === reply, options, eventId, onCancel);
    try { this.sendEvent({type, data, event_id: eventId} as ClientEvent); }
    catch (e) { for (const w of this.waiters) if (w.related === eventId) w.reject(e as YukkuriError); }
    return result;
  }
  async sendAudio(pcm: Uint8Array, options: OperationOptions = {}): Promise<void> {
    if (!this.input) throw failure('invalid_state', 'Start audio input first.');
    if (pcm.length % 2) throw failure('unsupported_format', 'PCM16 requires whole samples.');
    const max = Math.min(this.capabilities.limits.input_binary_bytes ?? 65536, 65536) & ~1;
    for (let p = 0; p < pcm.length; p += max) {
      if (options.signal?.aborted) { this.cancelInput(); throw failure('cancelled', 'Audio input cancelled.'); }
      const started = Date.now();
      while ((this.socket?.bufferedAmount ?? 0) > 256 * 1024) {
        if (Date.now() - started > (options.timeoutMs ?? this.client.options.connectTimeoutMs) || options.signal?.aborted) {
          this.cancelInput(); throw failure(options.signal?.aborted ? 'cancelled' : 'timeout', 'Audio send interrupted.');
        }
        await new Promise(r => setTimeout(r, 5));
      }
      if (this.state !== 'active' || this.socket?.readyState !== 1) throw failure('connection_closed', 'Session is closed.');
      this.socket.send(pcm.slice(p, p + max));
    }
  }
  protected setInput(value: boolean): void { this.input = value; }
  cancelInput(): void { this.sendEvent({type: 'input_audio.cancel'}); this.input = this.continuousInput; }
  stopAudioInput(): void { this.sendEvent({type: 'input_audio.stop'}); this.input = false; this.continuousInput = false; }
  cancelGeneration(generationId = this.generation?.id): void {
    if (generationId) this.sendEvent({type: 'generation.cancel', generation_id: generationId});
  }
  playbackSnapshot(): CreditSnapshot | undefined {
    const a = this.audio; return a && {capacity_source_frames: a.capacity, received_source_frames: a.received, played_source_frames: a.played, buffered_source_frames: a.received - a.played};
  }
  ackPlayed(playedSourceFrames: number, generationId = this.generation?.id): void {
    const a = this.audio;
    if (!a || generationId !== a.id) return;
    if (!Number.isSafeInteger(playedSourceFrames) || playedSourceFrames < 0 || playedSourceFrames > a.received)
      throw failure('invalid_request', 'Playback position must be a received source-frame position.');
    if (playedSourceFrames < a.played) return;
    a.played = playedSourceFrames; this.credit();
    this.sendEvent({type: 'playback.progress', generation_id: a.id, data: {played_source_frames: a.played}});
  }
  private credit(): void {
    if (this.audio && this.state === 'active') this.sendEvent({type: 'playback.credit', generation_id: this.audio.id, data: this.playbackSnapshot()!});
  }
  close(): Promise<void> {
    if (this.closeTask) return this.closeTask;
    if (this.state === 'closed') return Promise.resolve();
    this.closeTask = (async () => {
      try {
        if (this.state === 'active') {
          const result = this.request('session.close', {}, 'session.closed', {timeoutMs: this.client.options.closeTimeoutMs});
          this.state = 'closing'; await result;
        }
      } finally { this.socket?.close(1000); this.finishClose(1000); }
    })(); return this.closeTask;
  }
}
export class RealtimeSession extends Session {
  async sendText(text: string, options: OperationOptions & {output?: 'text' | 'audio'} = {}): Promise<Generation> {
    this.client.require(this.capabilities, 'conversation');
    if (options.output === 'audio') { this.client.require(this.capabilities, 'tts'); this.client.require(this.capabilities, 'audio_flow_control'); }
    const event = await this.request('input_text.commit', {text, output: options.output ?? 'text'}, 'generation.created', options,
      () => { if (this.state === 'active') void this.close().catch(() => {}); });
    if (this.generation?.id !== event.generation_id) throw failure('invalid_state', 'Generation was superseded.');
    const generation = this.generation!;
    const abort = () => { if (this.state === 'active') generation.cancel(); };
    options.signal?.addEventListener('abort', abort, {once: true});
    void generation.done.finally(() => options.signal?.removeEventListener('abort', abort)).catch(() => {});
    if (options.signal?.aborted) abort();
    return this.generation!;
  }
  startAudioInput(options: {mode?: 'realtime' | 'manual'} = {}): void {
    this.client.require(this.capabilities, 'transcription');
    if (options.mode !== 'manual') this.client.require(this.capabilities, 'realtime_input');
    this.sendEvent({type: 'input_audio.start', data: {sample_rate: 16000, channels: 1, encoding: 'pcm_s16le', ...(options.mode !== 'manual' ? {mode: 'realtime'} : {})}});
    this.continuousInput = options.mode !== 'manual';
    this.setInput(true);
  }
  commitInput(): void { this.sendEvent({type: 'input_audio.commit'}); this.setInput(false); }
}
export class TranscriptionSession extends Session {
  private commitAbort?: AbortController;
  constructor(client: YukkuriClient) { super(client, true); }
  async start(options: OperationOptions = {}): Promise<void> {
    await this.request('input_audio.start', {sample_rate: 16000, channels: 1, encoding: 'pcm_s16le'}, 'input_audio.started', options);
    this.setInput(true);
  }
  async commit(options: OperationOptions = {}): Promise<Transcript> {
    if (this.commitAbort) throw failure('invalid_state', 'Transcription is already in progress.');
    const controller = new AbortController(); this.commitAbort = controller;
    const abort = () => controller.abort(); options.signal?.addEventListener('abort', abort, {once: true});
    if (options.signal?.aborted) controller.abort();
    this.setInput(false);
    try {
      const e = await this.request('input_audio.commit', {}, 'input_audio.transcript.final', {...options, signal: controller.signal},
        () => { if (this.state === 'active') super.cancelInput(); });
      return e.data as unknown as Transcript;
    } finally { options.signal?.removeEventListener('abort', abort); this.commitAbort = undefined; }
  }
  override cancelInput(): void { this.commitAbort?.abort(); super.cancelInput(); }
  async cancel(options: OperationOptions = {}): Promise<void> {
    this.commitAbort?.abort();
    this.setInput(false); await this.request('input_audio.cancel', {}, 'input_audio.cancelled', options);
  }
}
