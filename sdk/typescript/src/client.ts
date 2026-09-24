import { YukkuriError, failure, publicError } from './errors.js';
import { RealtimeSession, TranscriptionSession } from './session.js';
import { validateSpeechWav } from './audio.js';
import type { Capabilities, OperationOptions, SpeakOptions, SpeechAudio, Transcript } from './types.js';

export interface ClientOptions {
  baseUrl?: string; httpTimeoutMs?: number; connectTimeoutMs?: number; closeTimeoutMs?: number;
  operationTimeoutMs?: number; capabilitiesTTLms?: number; fetch?: typeof fetch;
  webSocketFactory?: (url: string) => WebSocket;
}
export class YukkuriClient {
  readonly baseUrl: string;
  readonly options: Required<Omit<ClientOptions, 'baseUrl'>>;
  private cached?: { at: number; value: Capabilities };
  readonly realtime = { connect: (options: OperationOptions = {}) => this.connect(false, options) as Promise<RealtimeSession> };
  readonly transcription = { connect: (options: OperationOptions = {}) => this.connect(true, options) as Promise<TranscriptionSession> };
  constructor(options: ClientOptions = {}) {
    const u = new URL(options.baseUrl ?? 'http://127.0.0.1:8765');
    if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password || u.search || u.hash)
      throw failure('invalid_request', 'Use an HTTP(S) base URL without credentials, query or fragment.');
    this.baseUrl = u.href.replace(/\/$/, '');
    this.options = { httpTimeoutMs: 130000, connectTimeoutMs: 10000, closeTimeoutMs: 5000,
      operationTimeoutMs: 130000, capabilitiesTTLms: 30000, fetch: globalThis.fetch.bind(globalThis),
      webSocketFactory: url => new WebSocket(url), ...options };
  }
  private async request(path: string, init: RequestInit, options: OperationOptions = {}): Promise<{response: Response; bytes: Uint8Array}> {
    const controller = new AbortController();
    const abort = () => controller.abort();
    let timedOut = false;
    const timer = setTimeout(() => { timedOut = true; controller.abort(); }, options.timeoutMs ?? this.options.httpTimeoutMs);
    options.signal?.addEventListener('abort', abort, {once: true});
    if (options.signal?.aborted) controller.abort();
    try {
      const response = await this.options.fetch(this.baseUrl + path, {...init, signal: controller.signal});
      const bytes = new Uint8Array(await response.arrayBuffer());
      if (!response.ok) {
        let body: any; try { body = JSON.parse(new TextDecoder().decode(bytes)); } catch { /* Not public JSON. */ }
        if (publicError(body)) throw YukkuriError.from(body, undefined, response.headers.get('X-Request-ID') ?? undefined);
        throw new YukkuriError('http_error', `HTTP request failed (${response.status}).`, true, response.headers.get('X-Request-ID') ?? undefined);
      }
      return {response, bytes};
    } catch (e) {
      if (e instanceof YukkuriError) throw e;
      throw failure(timedOut ? 'timeout' : options.signal?.aborted ? 'cancelled' : 'connection_error',
        timedOut ? 'HTTP request timed out.' : options.signal?.aborted ? 'Operation cancelled.' : 'HTTP request or response stream failed.');
    } finally { clearTimeout(timer); options.signal?.removeEventListener('abort', abort); }
  }
  private async json(path: string, options: OperationOptions = {}): Promise<any> {
    const {bytes} = await this.request(path, {}, options);
    try { return JSON.parse(new TextDecoder().decode(bytes)); } catch { throw failure('protocol_error', 'Invalid JSON response.'); }
  }
  health(options: OperationOptions = {}): Promise<{status: string}> { return this.json('/health', options); }
  async capabilities(options: OperationOptions & {refresh?: boolean} = {}): Promise<Capabilities> {
    if (!options.refresh && this.cached && Date.now() - this.cached.at < this.options.capabilitiesTTLms) return this.cached.value;
    const value = await this.json('/v1/capabilities', options) as Capabilities;
    this.validateVersion(value);
    this.cached = {at: Date.now(), value}; return value;
  }
  validateVersion(c: Capabilities): void {
    if (c?.protocol_version !== '1' || !c.features || !c.limits)
      throw failure('unsupported_protocol', 'This SDK requires Public Protocol version 1.');
  }
  require(c: Capabilities, feature: string): void {
    const f = c.features[feature];
    if (!f?.available || f.version !== (feature === 'audio_flow_control' ? 'credit-v1' : '1'))
      throw failure('unsupported_capability', `Required capability is unavailable: ${feature}.`);
  }
  async speak(text: string, options: SpeakOptions = {}): Promise<SpeechAudio> {
    this.require(await this.capabilities(options), 'tts');
    const {voice, speed, provider} = options;
    const {response, bytes} = await this.request('/v1/audio/speech', {method: 'POST', headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({text, voice, speed, provider})}, options);
    const contentType = response.headers.get('Content-Type') ?? 'application/octet-stream';
    if (contentType.startsWith('audio/wav')) validateSpeechWav(bytes);
    return {audio: bytes, contentType, requestId: response.headers.get('X-Request-ID') ?? undefined,
      format: {encoding: contentType.startsWith('audio/wav') ? 'wav' : 'pcm_s16le',
        sample_rate: Number(response.headers.get('X-Audio-Sample-Rate')), channels: Number(response.headers.get('X-Audio-Channels'))}};
  }
  private async connect(transcription: boolean, options: OperationOptions): Promise<SessionResult> {
    const caps = await this.capabilities({...options, timeoutMs: options.timeoutMs ?? this.options.connectTimeoutMs});
    if (transcription) this.require(caps, 'transcription');
    const s = transcription ? new TranscriptionSession(this) : new RealtimeSession(this);
    await s.connect(options); return s;
  }
  async transcribe(pcm: Uint8Array, options: OperationOptions = {}): Promise<Transcript> {
    const session = await this.transcription.connect(options);
    try { await session.start(options); await session.sendAudio(pcm, options); return await session.commit(options); }
    finally { await session.close(); }
  }
}
type SessionResult = RealtimeSession | TranscriptionSession;
