import type { Envelope, PublicError } from './types.js';
export class YukkuriError extends Error {
  readonly name = 'YukkuriError';
  constructor(public code: string, message: string, public recoverable = true,
    public requestId?: string, public eventId?: string, public relatedEventId?: string, public generationId?: string) { super(message); }
  static from(data: PublicError, event?: Envelope, requestId?: string): YukkuriError {
    return new YukkuriError(data.code, data.message, data.recoverable, requestId, event?.event_id, event?.related_event_id, event?.generation_id);
  }
}
export const failure = (code: string, message: string) => new YukkuriError(code, message);
export function publicError(value: unknown): value is PublicError {
  const v = value as PublicError | null;
  return !!v && typeof v.code === 'string' && typeof v.message === 'string' && typeof v.recoverable === 'boolean';
}
