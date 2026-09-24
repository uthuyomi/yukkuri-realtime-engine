from dataclasses import dataclass
from typing import Any, Literal, NotRequired, Required, TypedDict

PROTOCOL_VERSION = "1"


class TranscriptionRuntime(TypedDict):
    backend: str
    requested_device: str
    selected_device: NotRequired[str]
    model: str
    persistent: bool
    state: str
    fallback_from: NotRequired[str]
    fallback_reason: NotRequired[str]


class Capability(TypedDict):
    version: str
    available: bool
    modes: NotRequired[list[str]]
    runtime: NotRequired[TranscriptionRuntime]


class Capabilities(TypedDict):
    protocol_version: str
    features: dict[str, Capability]
    endpoints: dict[str, str]
    limits: dict[str, int]
    input_audio_formats: list[dict[str, Any]]
    output_audio_formats: list[dict[str, Any]]


class RealtimeEvent(TypedDict, total=False):
    type: Required[str]
    data: Required[dict[str, Any]]
    event_id: Required[str]
    session_id: Required[str]
    timestamp: Required[str]
    generation_id: str
    related_event_id: str


@dataclass(frozen=True)
class AudioFormat:
    encoding: str
    sample_rate: int
    channels: int


@dataclass(frozen=True)
class SpeechAudio:
    audio: bytes
    format: AudioFormat
    content_type: str
    request_id: str | None = None


@dataclass(frozen=True)
class Transcript:
    text: str
    language: str
    turn_id: str


@dataclass(frozen=True)
class AudioPacket:
    generation_id: str
    pcm: bytes
    format: AudioFormat
    metadata: dict[str, int]


Output = Literal["text", "audio"]


class YukkuriError(Exception):
    def __init__(self, code: str, message: str, recoverable: bool = True, *,
                 request_id: str | None = None, event_id: str | None = None,
                 related_event_id: str | None = None, generation_id: str | None = None):
        super().__init__(message)
        self.code, self.message, self.recoverable = code, message, recoverable
        self.request_id, self.event_id = request_id, event_id
        self.related_event_id, self.generation_id = related_event_id, generation_id

    @classmethod
    def from_public(cls, data: dict, event: dict | None = None, request_id: str | None = None):
        if not isinstance(data, dict) or not isinstance(data.get("code"), str) or not isinstance(data.get("message"), str) or not isinstance(data.get("recoverable"), bool):
            return cls("protocol_error", "Invalid public error response.", request_id=request_id)
        event = event or {}
        return cls(data["code"], data["message"], data["recoverable"], request_id=request_id,
                   event_id=event.get("event_id"), related_event_id=event.get("related_event_id"), generation_id=event.get("generation_id"))
