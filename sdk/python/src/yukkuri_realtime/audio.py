import struct
from .models import YukkuriError


def validate_speech_wav(audio: bytes) -> None:
    if len(audio) < 12 or audio[:4] != b"RIFF" or audio[8:12] != b"WAVE" or struct.unpack_from("<I", audio, 4)[0] + 8 != len(audio):
        raise YukkuriError("incomplete_audio", "Incomplete RIFF/WAVE response.")


def wav_to_pcm(audio: bytes) -> bytes:
    """Decode only complete PCM16 mono 16 kHz RIFF/WAVE; no implicit resampling."""
    validate_speech_wav(audio)
    pos, valid, pcm = 12, False, None
    while pos < len(audio):
        if pos + 8 > len(audio):
            raise YukkuriError("unsupported_format", "Truncated WAV chunk.")
        tag, size = struct.unpack_from("<4sI", audio, pos)
        end = pos + 8 + size
        if end + size % 2 > len(audio):
            raise YukkuriError("unsupported_format", "Truncated WAV data.")
        if tag == b"fmt ":
            if valid or size < 16 or struct.unpack_from("<HHIIHH", audio, pos + 8) != (1, 1, 16000, 32000, 2, 16):
                raise YukkuriError("unsupported_format", "WAV input must be PCM16 mono 16 kHz.")
            valid = True
        elif tag == b"data":
            if pcm is not None or size % 2:
                raise YukkuriError("unsupported_format", "Invalid WAV data chunk.")
            pcm = audio[pos + 8:end]
        pos = end + size % 2
    if not valid or not pcm:
        raise YukkuriError("unsupported_format", "WAV requires format and nonempty PCM data.")
    return pcm
