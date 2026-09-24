import asyncio
import contextlib
import json
import uuid
from collections import defaultdict
from typing import AsyncIterator, Callable
from websockets.asyncio.client import connect
from websockets.exceptions import ConnectionClosed
from .models import AudioFormat, AudioPacket, Transcript, RealtimeEvent, YukkuriError, Output


class Generation:
    def __init__(self, generation_id: str, session):
        self.id, self._session = generation_id, session
        self.state = "generating"
        self._done = asyncio.get_running_loop().create_future()
        # Retrieve unobserved exceptions without hiding them from wait_done callers.
        self._done.add_done_callback(lambda f: f.exception() if not f.cancelled() else None)

    async def wait_done(self, *, timeout=None):
        try:
            async with asyncio.timeout(timeout):
                return await asyncio.shield(self._done)
        except TimeoutError:
            raise YukkuriError("timeout", "Generation wait timed out.") from None

    async def cancel(self):
        await self._session.cancel_generation(self.id)

    def _finish(self, result="done", error=None):
        if not self._done.done():
            self.state = "cancelled" if error else result
            self._done.set_exception(error) if error else self._done.set_result(result)


class Session:
    transcription_only = False

    def __init__(self, client):
        self.client = client
        self.state, self.id = "connecting", ""
        self.request_id = None
        self.capabilities = None
        self.generation: Generation | None = None
        self._ws = self._reader = None
        self._listeners = defaultdict(set)
        self._waiters = []
        self._pending = self._pair_timer = self._audio = None
        self._input = False
        self._continuous = False
        self._close_task = None
        self._cleanup = None
        self._terminal = None
        self._send_lock = asyncio.Lock()

    def on(self, name: str, callback: Callable):
        """Register a synchronous callback; returns an unsubscribe function."""
        self._listeners[name].add(callback)
        return lambda: self._listeners[name].discard(callback)

    def _emit(self, name, value):
        for callback in list(self._listeners[name]):
            try:
                callback(value)
            except Exception:
                pass  # A user callback cannot interrupt protocol parsing.

    async def events(self, *, capacity=256) -> AsyncIterator[RealtimeEvent]:
        """Bounded raw-event subscription. Subscribe before starting work."""
        queue = asyncio.Queue(maxsize=capacity)
        overflow = False

        def put(value):
            nonlocal overflow
            if queue.full():
                overflow = True
                queue.get_nowait()
            queue.put_nowait(value)

        off = self.on("event", put)
        off_close = self.on("closed", lambda _: put(None))
        try:
            while self.state != "closed" or not queue.empty():
                event = await queue.get()
                if overflow:
                    raise YukkuriError("resource_limit", "Event consumer cannot keep up.")
                if event is None:
                    return
                yield event
        finally:
            off()
            off_close()

    def _waiter(self, kind, related=None):
        future = asyncio.get_running_loop().create_future()
        entry = (kind, related, future)
        self._waiters.append(entry)
        future.add_done_callback(lambda _: self._waiters.remove(entry) if entry in self._waiters else None)
        return future

    async def connect(self, *, timeout=None):
        if self._ws is not None or self.state != "connecting":
            raise YukkuriError("invalid_state", "Create a new session to connect again.")
        timeout = self.client.connect_timeout if timeout is None else timeout
        url = self.client.base_url.replace("https://", "wss://", 1).replace("http://", "ws://", 1)
        try:
            async with asyncio.timeout(timeout):
                self._ws = await connect(url + ("/v1/transcription" if self.transcription_only else "/v1/realtime"),
                                         open_timeout=timeout, close_timeout=self.client.close_timeout,
                                         max_size=1024 * 1024, max_queue=16, proxy=None)
                first_wait = self._waiter("session.created")
                self._reader = asyncio.create_task(self._read())
                first = await first_wait
                if first["data"].get("protocol_version") != "1":
                    raise YukkuriError("unsupported_protocol", "This SDK requires protocol version 1.")
                self.id, self.capabilities = first["session_id"], first["data"]["capabilities"]
                self.request_id = first["data"].get("request_id")
                self.client.validate_version(self.capabilities)
                if self.transcription_only:
                    self.client.require(self.capabilities, "transcription")
                self.state = "active"
                data = {"protocol_version": "1"}
                flow = self.capabilities["features"].get("audio_flow_control", {})
                if not self.transcription_only and flow.get("available") and flow.get("version") == "credit-v1":
                    data["audio_flow_control"] = "credit-v1"
                await self._request("session.configure", data, "session.configured", timeout=timeout)
                self._emit("connected", self)
        except BaseException as exc:
            await self._shutdown()
            if isinstance(exc, (asyncio.CancelledError, YukkuriError)):
                raise
            if isinstance(exc, TimeoutError):
                raise YukkuriError("timeout", "Connection timed out.") from None
            raise YukkuriError("connection_error", "WebSocket connection failed.") from None

    async def _read(self):
        try:
            async for payload in self._ws:
                if isinstance(payload, bytes):
                    await self._binary(payload)
                    continue
                if self._pending:
                    raise YukkuriError("protocol_error", "Audio metadata was not followed by binary PCM.")
                e = json.loads(payload)
                if not isinstance(e, dict) or any(not isinstance(e.get(k), str) for k in ("type", "event_id", "session_id", "timestamp")) or not isinstance(e.get("data"), dict) or (self.id and self.id != e["session_id"]):
                    raise YukkuriError("protocol_error", "Invalid event envelope.")
                await self._handle(e)
                self._emit("event", e)
                for kind, related, future in list(self._waiters):
                    if not future.done() and kind == e["type"] and (not related or related == e.get("related_event_id")):
                        future.set_result(e)
        except asyncio.CancelledError:
            raise
        except ConnectionClosed:
            pass
        except Exception as exc:
            self._terminal = exc if isinstance(exc, YukkuriError) else YukkuriError("protocol_error", "Invalid server event.")
            self._emit("error", self._terminal)
        finally:
            if self._pending and not self._terminal:
                self._terminal = YukkuriError("protocol_error", "Connection closed before audio binary.")
                self._emit("error", self._terminal)
            self._finish_close()
            if self._ws:
                await self._ws.close()

    async def _handle(self, e):
        kind, d, gen = e["type"], e["data"], e.get("generation_id")
        if kind == "session.closed":
            self._cleanup = d.get("cleanup_complete")
        elif kind == "error":
            err = YukkuriError.from_public(d, e, self.request_id)
            for _, related, future in list(self._waiters):
                if not future.done() and (not related or related == e.get("related_event_id")):
                    future.set_exception(err)
            if self.generation and gen == self.generation.id:
                self.generation._finish(error=err)
            self._emit("error", err)
            if not err.recoverable:
                raise err
        elif kind == "generation.created":
            if not gen:
                raise YukkuriError("protocol_error", "Missing generation ID.")
            if self.generation:
                self.generation._finish("cancelled")
            self.generation, self._audio = Generation(gen, self), None
            self._emit("generation_started", self.generation)
        elif kind == "generation.cancelled" and self.generation and gen == self.generation.id:
            self.generation._finish("cancelled")
            self._audio = None
        elif kind == "generation.done" and self.generation and gen == self.generation.id:
            if "source_frames" in d and d["source_frames"] != (self._audio["received"] if self._audio else 0):
                raise YukkuriError("protocol_error", "Incomplete source audio stream.")
            self.generation._finish()
            self._emit("generation_done", self.generation)
        elif kind == "input_audio.transcript.final":
            if any(not isinstance(d.get(k), str) for k in ("text", "language", "turn_id")):
                raise YukkuriError("protocol_error", "Invalid transcript event.")
            self._emit("transcript", Transcript(d["text"], d["language"], d["turn_id"]))
        elif kind == "response.text.delta":
            if not isinstance(d.get("text"), str):
                raise YukkuriError("protocol_error", "Invalid text delta.")
            self._emit("text_delta", {"delta": d["text"], "generation_id": gen})
        elif kind == "response.text.done":
            self._emit("text_done", {"generation_id": gen})
        elif kind == "response.audio.chunk.started" and self.generation and gen == self.generation.id and self.generation.state != "cancelled":
            rate = d.get("sample_rate")
            if type(rate) is not int or not 1000 <= rate <= 192000 or d.get("channels") != 1 or d.get("bits_per_sample") != 16:
                raise YukkuriError("protocol_error", "Unsupported audio output format.")
            if not self._audio:
                self._audio = dict(id=gen, rate=rate, received=0, played=0, capacity=rate * 2, chunk=-1, packet=-1)
                await self._credit()
            elif self._audio["rate"] != rate:
                raise YukkuriError("protocol_error", "Source rate changed within generation.")
        elif kind == "response.audio.delta":
            if self.transcription_only:
                raise YukkuriError("protocol_error", "Transcription cannot produce audio.")
            self._pending = e
            def expired():
                self._terminal = YukkuriError("protocol_error", "Audio binary did not arrive.")
                self._emit("error", self._terminal)
                self._reader.cancel()
            self._pair_timer = asyncio.get_running_loop().call_later(self.client.connect_timeout, expired)
        elif kind.startswith("interruption."):
            self._emit("interruption", e)
        elif kind == "input_audio.backchannel":
            self._emit("backchannel", e)

    async def _binary(self, pcm):
        e, self._pending = self._pending, None
        if self._pair_timer:
            self._pair_timer.cancel()
        if not e:
            raise YukkuriError("protocol_error", "Unexpected binary without audio metadata.")
        d = e["data"]
        if type(d.get("source_frames")) is not int or d["source_frames"] <= 0 or len(pcm) != d.get("bytes") or len(pcm) != d["source_frames"] * 2 or d.get("channels") != 1 or d.get("bits_per_sample") != 16:
            raise YukkuriError("protocol_error", "PCM metadata length or format mismatch.")
        if not self.generation or e.get("generation_id") != self.generation.id or self.generation.state == "cancelled":
            return
        a = self._audio
        if not a or d.get("sample_rate") != a["rate"] or d.get("source_start_frame") != a["received"] or type(d.get("speech_sequence")) is not int or type(d.get("audio_sequence")) is not int:
            raise YukkuriError("protocol_error", "PCM source position mismatch.")
        if (d["audio_sequence"] != a["packet"] + 1 if d["speech_sequence"] == a["chunk"] else d["speech_sequence"] != a["chunk"] + 1 or d["audio_sequence"] != 0):
            raise YukkuriError("protocol_error", "PCM packet order mismatch.")
        a["received"] += d["source_frames"]
        a["chunk"], a["packet"] = d["speech_sequence"], d["audio_sequence"]
        if a["received"] - a["played"] > a["capacity"]:
            raise YukkuriError("resource_limit", "Audio exceeded the playback window.")
        await self._credit()
        self._emit("audio", AudioPacket(a["id"], pcm, AudioFormat("pcm_s16le", a["rate"], 1), d))

    async def send_event(self, kind: str, data=None, *, generation_id=None, event_id=None):
        if self.state != "active":
            raise YukkuriError("invalid_state", "Session is not active.")
        event_id = event_id or "sdk_" + uuid.uuid4().hex
        event = dict(type=kind, data=data or {}, event_id=event_id)
        if generation_id:
            event["generation_id"] = generation_id
        payload = json.dumps(event, ensure_ascii=False)
        if len(payload.encode()) > self.capabilities["limits"].get("json_bytes", 65536):
            raise YukkuriError("payload_too_large", "JSON event is too large.")
        try:
            async with self._send_lock, asyncio.timeout(self.client.connect_timeout):
                await self._ws.send(payload)
        except TimeoutError:
            raise YukkuriError("timeout", "WebSocket send timed out.") from None
        except ConnectionClosed:
            raise YukkuriError("connection_closed", "Session is closed.") from None
        return event_id

    async def _request(self, kind, data, reply, *, timeout=None):
        event_id = "sdk_" + uuid.uuid4().hex
        future = self._waiter(reply, event_id)
        try:
            async with asyncio.timeout(self.client.operation_timeout if timeout is None else timeout):
                await self.send_event(kind, data, event_id=event_id)
                return await future
        except TimeoutError:
            raise YukkuriError("timeout", "Operation timed out.") from None
        finally:
            if not future.done():
                future.cancel()

    async def send_audio(self, pcm: bytes, *, timeout=None):
        if not self._input:
            raise YukkuriError("invalid_state", "Start audio input first.")
        if len(pcm) % 2:
            raise YukkuriError("unsupported_format", "PCM16 requires whole samples.")
        maximum = min(self.capabilities["limits"].get("input_binary_bytes", 65536), 65536) & ~1
        try:
            async with asyncio.timeout(self.client.operation_timeout if timeout is None else timeout):
                for pos in range(0, len(pcm), maximum):
                    async with self._send_lock:
                        await self._ws.send(pcm[pos:pos + maximum])
                    await asyncio.sleep(0)
        except (TimeoutError, asyncio.CancelledError) as exc:
            await self.cancel_input()
            if isinstance(exc, asyncio.CancelledError):
                raise
            raise YukkuriError("timeout", "Audio input timed out.") from None
        except ConnectionClosed:
            raise YukkuriError("connection_closed", "Session is closed.") from None

    async def cancel_input(self):
        await self.send_event("input_audio.cancel")
        self._input = self._continuous

    async def stop_audio_input(self):
        await self.send_event("input_audio.stop")
        self._input = False
        self._continuous = False

    async def cancel_generation(self, generation_id=None):
        generation_id = generation_id or (self.generation.id if self.generation else None)
        if generation_id:
            await self.send_event("generation.cancel", generation_id=generation_id)

    def playback_snapshot(self):
        a = self._audio
        return dict(capacity_source_frames=a["capacity"], received_source_frames=a["received"], played_source_frames=a["played"], buffered_source_frames=a["received"] - a["played"]) if a else None

    async def ack_played(self, played_source_frames: int, generation_id=None):
        a = self._audio
        if not a or (generation_id and generation_id != a["id"]):
            return
        if type(played_source_frames) is not int or not 0 <= played_source_frames <= a["received"]:
            raise YukkuriError("invalid_request", "Playback position must be a received source-frame position.")
        if played_source_frames < a["played"]:
            return
        a["played"] = played_source_frames
        await self._credit()
        await self.send_event("playback.progress", {"played_source_frames": a["played"]}, generation_id=a["id"])

    async def _credit(self):
        if self._audio and self.state == "active":
            await self.send_event("playback.credit", self.playback_snapshot(), generation_id=self._audio["id"])

    def _finish_close(self):
        if self.state == "closed":
            return
        self.state = "closed"
        if self._pair_timer:
            self._pair_timer.cancel()
        err = self._terminal or YukkuriError("connection_closed", "Session closed.")
        for _, _, future in list(self._waiters):
            if not future.done():
                future.set_exception(err)
        if self.generation:
            self.generation._finish(error=err)
        self._audio = None
        self._emit("closed", {"cleanup_complete": self._cleanup})

    async def _shutdown(self):
        if self._reader and self._reader is not asyncio.current_task():
            self._reader.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await self._reader
        if self._ws:
            await self._ws.close()
        self._finish_close()

    async def close(self):
        if self.state == "closed":
            return
        if not self._close_task:
            async def closing():
                try:
                    if self.state == "active":
                        # Send before switching state; no new public work after this await.
                        future = self._waiter("session.closed")
                        await self.send_event("session.close")
                        self.state = "closing"
                        try:
                            async with asyncio.timeout(self.client.close_timeout):
                                await future
                        except TimeoutError:
                            raise YukkuriError("timeout", "Session close timed out.") from None
                finally:
                    await self._shutdown()
            self._close_task = asyncio.create_task(closing())
        await asyncio.shield(self._close_task)

    async def __aenter__(self):
        return self

    async def __aexit__(self, *args):
        await self.close()


class RealtimeSession(Session):
    async def send_text(self, text: str, *, output: Output = "text", timeout=None) -> Generation:
        self.client.require(self.capabilities, "conversation")
        if output == "audio":
            self.client.require(self.capabilities, "tts")
            self.client.require(self.capabilities, "audio_flow_control")
        try:
            event = await self._request("input_text.commit", {"text": text, "output": output}, "generation.created", timeout=timeout)
        except (asyncio.CancelledError, YukkuriError) as exc:
            if isinstance(exc, asyncio.CancelledError) or exc.code == "timeout":
                await self.close()
            raise
        if not self.generation or self.generation.id != event["generation_id"]:
            raise YukkuriError("invalid_state", "Generation was superseded.")
        return self.generation

    async def start_audio_input(self, *, mode="realtime"):
        self.client.require(self.capabilities, "transcription")
        if mode == "realtime":
            self.client.require(self.capabilities, "realtime_input")
        elif mode != "manual":
            raise YukkuriError("invalid_request", "Input mode must be realtime or manual.")
        data = dict(sample_rate=16000, channels=1, encoding="pcm_s16le")
        if mode == "realtime":
            data["mode"] = mode
        await self.send_event("input_audio.start", data)
        self._input = True
        self._continuous = mode == "realtime"

    async def commit_input(self):
        await self.send_event("input_audio.commit")
        self._input = False


class TranscriptionSession(Session):
    transcription_only = True

    def __init__(self, client):
        super().__init__(client)
        self._commit_task = None
        self._cancel_requested = False

    async def start(self, *, timeout=None):
        await self._request("input_audio.start", dict(sample_rate=16000, channels=1, encoding="pcm_s16le"), "input_audio.started", timeout=timeout)
        self._input = True

    async def commit(self, *, timeout=None) -> Transcript:
        if self._commit_task:
            raise YukkuriError("invalid_state", "Transcription is already in progress.")
        self._input = False
        self._cancel_requested = False
        self._commit_task = asyncio.current_task()
        try:
            e = await self._request("input_audio.commit", {}, "input_audio.transcript.final", timeout=timeout)
            d = e["data"]
            return Transcript(d["text"], d["language"], d["turn_id"])
        except (asyncio.CancelledError, YukkuriError) as exc:
            if (isinstance(exc, asyncio.CancelledError) or exc.code == "timeout") and self.state == "active" and not self._cancel_requested:
                await super().cancel_input()
            raise
        finally:
            self._commit_task = None
            self._cancel_requested = False

    async def cancel(self, *, timeout=None):
        if self._commit_task and self._commit_task is not asyncio.current_task():
            self._cancel_requested = True
            self._commit_task.cancel()
        self._input = False
        await self._request("input_audio.cancel", {}, "input_audio.cancelled", timeout=timeout)

    async def cancel_input(self):
        await self.cancel()
