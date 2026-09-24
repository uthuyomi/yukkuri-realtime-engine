import { failure } from './errors.js';
/** Validate a complete RIFF/WAVE PCM16 mono 16 kHz file, without resampling. */
export function wavToPCM(wav: Uint8Array): Uint8Array {
  const v = new DataView(wav.buffer, wav.byteOffset, wav.byteLength);
  const tag = (n: number) => String.fromCharCode(...wav.subarray(n, n + 4));
  if (wav.length < 12 || tag(0) !== 'RIFF' || tag(8) !== 'WAVE' || v.getUint32(4, true) + 8 !== wav.length)
    throw failure('unsupported_format', 'Expected a complete RIFF/WAVE file.');
  let valid = false, pcm: Uint8Array | undefined;
  for (let p = 12; p < wav.length;) {
    if (p + 8 > wav.length) throw failure('unsupported_format', 'Truncated WAV chunk.');
    const n = v.getUint32(p + 4, true), end = p + 8 + n;
    if (end + (n % 2) > wav.length) throw failure('unsupported_format', 'Truncated WAV data.');
    if (tag(p) === 'fmt ') {
      if (valid || n < 16 || v.getUint16(p + 8, true) !== 1 || v.getUint16(p + 10, true) !== 1 ||
        v.getUint32(p + 12, true) !== 16000 || v.getUint32(p + 16, true) !== 32000 ||
        v.getUint16(p + 20, true) !== 2 || v.getUint16(p + 22, true) !== 16)
        throw failure('unsupported_format', 'WAV input must be PCM16 mono 16 kHz.');
      valid = true;
    } else if (tag(p) === 'data') {
      if (pcm || n % 2) throw failure('unsupported_format', 'Invalid WAV data chunk.');
      pcm = wav.subarray(p + 8, end);
    }
    p = end + (n % 2);
  }
  if (!valid || !pcm?.length) throw failure('unsupported_format', 'WAV requires format and nonempty PCM data.');
  return pcm;
}
/** Detect truncated WAV TTS responses without assuming input-STT sample rate. */
export function validateSpeechWav(audio: Uint8Array): void {
  if (audio.length < 12 || String.fromCharCode(...audio.subarray(0, 4)) !== 'RIFF' ||
    String.fromCharCode(...audio.subarray(8, 12)) !== 'WAVE' ||
    new DataView(audio.buffer, audio.byteOffset, audio.byteLength).getUint32(4, true) + 8 !== audio.length)
    throw failure('incomplete_audio', 'Incomplete WAV response.');
}
