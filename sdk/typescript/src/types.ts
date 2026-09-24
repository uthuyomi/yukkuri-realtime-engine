export const PROTOCOL_VERSION = '1' as const;
export interface AudioFormat { encoding: string; sample_rate?: number; channels: number }
export interface TranscriptionRuntime {
  backend: string; requested_device: string; selected_device?: string; model: string;
  persistent: boolean; state: string; fallback_from?: string; fallback_reason?: string;
}
export interface Capability { version: string; available: boolean; modes?: string[]; runtime?: TranscriptionRuntime }
export interface Capabilities {
  protocol_version: string; features: Record<string, Capability>; endpoints: Record<string, string>;
  input_audio_formats: AudioFormat[]; output_audio_formats: AudioFormat[]; limits: Record<string, number>;
}
export interface PublicError { code: string; message: string; recoverable: boolean; legacy_code?: string }
export interface Envelope<T extends string = string, D = Record<string, unknown>> {
  type: T; data: D; event_id: string; session_id: string; timestamp: string;
  related_event_id?: string; generation_id?: string;
}
export interface Transcript { text: string; language: string; turn_id: string }
export interface AudioMetadata {
  speech_sequence: number; audio_sequence: number; bytes: number; source_frames: number;
  source_start_frame: number; sample_rate: number; channels: number; bits_per_sample: number;
}
export type RealtimeEvent =
  | Envelope<'session.created', { protocol_version: string; capabilities: Capabilities; request_id: string; interruption_timeout_ms?: number }>
  | Envelope<'session.configured', { protocol_version: string; audio_flow_control?: string }>
  | Envelope<'session.closed', { cleanup_complete: boolean }>
  | Envelope<'error', PublicError>
  | Envelope<'input_audio.transcript.final', Transcript>
  | Envelope<'response.text.delta', { text: string }>
  | Envelope<'response.audio.delta', AudioMetadata>
  | Envelope<'response.audio.chunk.started', { sequence: number; text: string; codec: string; sample_rate: number; channels: number; bits_per_sample: number }>
  | Envelope<'generation.created', { source?: string; output?: string; voice?: string; speed?: number }>
  | Envelope<'generation.done', { source_frames?: number; sample_rate?: number }>
  | Envelope<'generation.cancelled', { reason?: string }>
  | Envelope<'response.text.done' | 'pong', Record<string, never>>
  | Envelope<'interruption.suspected' | 'interruption.recovered' | 'interruption.confirmed' | 'input_audio.backchannel', { interruption_id: string; state: string; reason: string; playback: Record<string, unknown> }>
  | Envelope<'input_audio.started' | 'input_audio.committed' | 'input_audio.cancelled' | 'input_audio.turn' | 'conversation.item.updated' | 'response.audio.chunk.done' | 'speculation.started' | 'speculation.ready' | 'speculation.promoted' | 'speculation.invalidated' | 'speculation.cancelled' | 'speculation.fallback'>;
/** Unknown additions retain their envelope and are delivered to the raw listener. */
export type ServerEvent = RealtimeEvent | Envelope;
export interface CreditSnapshot {
  capacity_source_frames: number; received_source_frames: number;
  played_source_frames: number; buffered_source_frames: number;
}
export interface AudioPacket { generationId: string; pcm: Uint8Array; format: AudioFormat; metadata: AudioMetadata }
export interface SpeechAudio { audio: Uint8Array; format: AudioFormat; contentType: string; requestId?: string }
export interface OperationOptions { signal?: AbortSignal; timeoutMs?: number }
export interface SpeakOptions extends OperationOptions { voice?: string; speed?: number; provider?: string }
