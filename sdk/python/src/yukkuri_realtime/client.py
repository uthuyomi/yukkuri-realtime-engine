import asyncio
import time
from urllib.parse import urlsplit
import httpx
from .models import AudioFormat, Capabilities, SpeechAudio, Transcript, YukkuriError
from .audio import validate_speech_wav
from .session import RealtimeSession, TranscriptionSession


class _Sessions:
    def __init__(self, client, cls):
        self.client, self.cls = client, cls

    async def connect(self, *, timeout: float | None = None):
        caps = await self.client.capabilities(timeout=self.client.connect_timeout if timeout is None else timeout)
        if self.cls is TranscriptionSession:
            self.client.require(caps, "transcription")
        session = self.cls(self.client)
        await session.connect(timeout=timeout)
        self.client._sessions.add(session)
        session.on("closed", lambda _: self.client._sessions.discard(session))
        return session


class YukkuriClient:
    """Async canonical client. Use async with for HTTP and socket cleanup."""
    def __init__(self, base_url: str = "http://127.0.0.1:8765", *, http_timeout: float = 130,
                 connect_timeout: float = 10, close_timeout: float = 5, operation_timeout: float = 130,
                 capabilities_ttl: float = 30):
        u = urlsplit(base_url)
        if u.scheme not in ("http", "https") or not u.netloc or u.username or u.password or u.query or u.fragment:
            raise YukkuriError("invalid_request", "Use an HTTP(S) base URL without credentials, query or fragment.")
        self.base_url = base_url.rstrip("/")
        self.http_timeout, self.connect_timeout, self.close_timeout = http_timeout, connect_timeout, close_timeout
        self.operation_timeout, self.capabilities_ttl = operation_timeout, capabilities_ttl
        self._http = httpx.AsyncClient(timeout=http_timeout)
        self._cache = None
        self._sessions = set()
        self.realtime = _Sessions(self, RealtimeSession)
        self.transcription = _Sessions(self, TranscriptionSession)

    async def __aenter__(self):
        return self

    async def __aexit__(self, *args):
        await self.close()

    async def close(self):
        await asyncio.gather(*(s.close() for s in list(self._sessions)), return_exceptions=True)
        await self._http.aclose()

    async def _request(self, method: str, path: str, *, timeout=None, **kwargs):
        try:
            async with asyncio.timeout(self.http_timeout if timeout is None else timeout):
                response = await self._http.request(method, self.base_url + path, **kwargs)
                if not response.is_success:
                    try:
                        data = response.json()
                    except ValueError:
                        raise YukkuriError("http_error", f"HTTP request failed ({response.status_code}).", request_id=response.headers.get("X-Request-ID")) from None
                    raise YukkuriError.from_public(data, request_id=response.headers.get("X-Request-ID"))
                return response
        except (TimeoutError, httpx.TimeoutException):
            raise YukkuriError("timeout", "HTTP request timed out.") from None
        except httpx.HTTPError:
            raise YukkuriError("connection_error", "HTTP request or response stream failed.") from None

    async def health(self, *, timeout=None) -> dict:
        return self._json(await self._request("GET", "/health", timeout=timeout))

    @staticmethod
    def _json(response):
        try:
            return response.json()
        except ValueError:
            raise YukkuriError("protocol_error", "Invalid JSON response.") from None

    async def capabilities(self, *, refresh=False, timeout=None) -> Capabilities:
        if not refresh and self._cache and time.monotonic() - self._cache[0] < self.capabilities_ttl:
            return self._cache[1]
        caps = self._json(await self._request("GET", "/v1/capabilities", timeout=timeout))
        self.validate_version(caps)
        self._cache = (time.monotonic(), caps)
        return caps

    @staticmethod
    def validate_version(caps):
        if not isinstance(caps, dict) or caps.get("protocol_version") != "1" or not isinstance(caps.get("features"), dict) or not isinstance(caps.get("limits"), dict):
            raise YukkuriError("unsupported_protocol", "This SDK requires Public Protocol version 1.")

    @staticmethod
    def require(caps, feature):
        f = caps["features"].get(feature, {})
        if not f.get("available") or f.get("version") != ("credit-v1" if feature == "audio_flow_control" else "1"):
            raise YukkuriError("unsupported_capability", f"Required capability is unavailable: {feature}.")

    async def speak(self, text: str, *, voice=None, speed=None, provider=None, timeout=None) -> SpeechAudio:
        self.require(await self.capabilities(timeout=timeout), "tts")
        data = {"text": text}
        data.update({k: v for k, v in {"voice": voice, "speed": speed, "provider": provider}.items() if v is not None})
        r = await self._request("POST", "/v1/audio/speech", json=data, timeout=timeout)
        content_type = r.headers.get("Content-Type", "application/octet-stream")
        if content_type.startswith("audio/wav"):
            validate_speech_wav(r.content)
        try:
            fmt = AudioFormat("wav" if content_type.startswith("audio/wav") else "pcm_s16le", int(r.headers.get("X-Audio-Sample-Rate", 0)), int(r.headers.get("X-Audio-Channels", 0)))
        except ValueError:
            raise YukkuriError("protocol_error", "Invalid audio format headers.") from None
        return SpeechAudio(r.content, fmt, content_type, r.headers.get("X-Request-ID"))

    async def transcribe(self, pcm: bytes, *, timeout=None) -> Transcript:
        session = await self.transcription.connect()
        try:
            await session.start()
            await session.send_audio(pcm)
            return await session.commit(timeout=timeout)
        finally:
            await session.close()
