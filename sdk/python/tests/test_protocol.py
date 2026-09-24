import asyncio
import json
import unittest
from websockets.asyncio.server import serve
from yukkuri_realtime import YukkuriClient, RealtimeSession, YukkuriError, wav_to_pcm

CAPS = dict(protocol_version="1", features={"audio_flow_control": dict(version="credit-v1", available=True)}, limits={})


class Protocol(unittest.IsolatedAsyncioTestCase):
    async def socket_session(self, scenario, version="1"):
        async def handler(ws):
            n = 0
            async def event(kind, data=None, related=None, gen=None):
                nonlocal n
                n += 1
                await ws.send(json.dumps(dict(type=kind, data=data or {}, event_id=f"e{n}", session_id="s", timestamp="2026-09-24T00:00:00Z", related_event_id=related, generation_id=gen)))
            await event("session.created", dict(protocol_version=version, capabilities=CAPS))
            if version != "1":
                await ws.wait_closed()
                return
            config = json.loads(await ws.recv())
            await event("session.configured", dict(protocol_version="1"), config["event_id"])
            await scenario(ws, event)
            async for payload in ws:
                e = json.loads(payload)
                if e["type"] == "session.close":
                    await event("session.closed", dict(cleanup_complete=True), e["event_id"])
                    return
        server = await serve(handler, "127.0.0.1", 0)
        self.addAsyncCleanup(server.wait_closed)
        self.addCleanup(server.close)
        port = server.sockets[0].getsockname()[1]
        client = YukkuriClient(f"http://127.0.0.1:{port}", connect_timeout=.3, close_timeout=.3)
        self.addAsyncCleanup(client.close)
        session = RealtimeSession(client)
        self.addAsyncCleanup(session.close)
        return session

    async def test_unknown_future_event(self):
        release = asyncio.Event()
        async def scenario(ws, event):
            await release.wait()
            await event("future.optional", dict(extension=True))
        session = await self.socket_session(scenario)
        await session.connect()
        seen = asyncio.get_running_loop().create_future()
        session.on("event", lambda e: seen.set_result(e) if e["type"] == "future.optional" else None)
        release.set()
        event = await asyncio.wait_for(seen, 1)
        self.assertEqual(event["type"], "future.optional")
        self.assertEqual(session.state, "active")
        with self.assertRaises(YukkuriError) as e:
            await session.connect()
        self.assertEqual(e.exception.code, "invalid_state")

    async def test_version_and_capability(self):
        async def scenario(*_):
            pass
        session = await self.socket_session(scenario, "2")
        with self.assertRaises(YukkuriError) as e:
            await session.connect()
        self.assertEqual(e.exception.code, "unsupported_protocol")
        session = await self.socket_session(scenario)
        await session.connect()
        with self.assertRaises(YukkuriError) as e:
            await session.send_text("unavailable")
        self.assertEqual(e.exception.code, "unsupported_capability")

    async def test_malformed_pairing_disconnect_and_timeout(self):
        for action in ("unexpected", "missing", "length", "disconnect", "timeout"):
            release = asyncio.Event()
            async def scenario(ws, event):
                await release.wait()
                if action == "unexpected":
                    await ws.send(b"\0\0")
                else:
                    await event("response.audio.delta", dict(bytes=2, source_frames=1, channels=1, bits_per_sample=16), gen="old")
                    if action == "missing":
                        await event("future.optional")
                    elif action == "length":
                        await ws.send(bytes(4))
                    elif action == "disconnect":
                        await ws.close()
                    else:
                        await asyncio.sleep(.5)
            session = await self.socket_session(scenario)
            await session.connect()
            seen = asyncio.get_running_loop().create_future()
            session.on("error", lambda e: seen.set_result(e) if not seen.done() else None)
            release.set()
            error = await asyncio.wait_for(seen, 2)
            self.assertEqual(error.code, "protocol_error", action)
            await asyncio.sleep(.02)
            self.assertEqual(session.state, "closed")

    def test_wav_validation(self):
        with self.assertRaises(YukkuriError):
            wav_to_pcm(b"bad")


if __name__ == "__main__":
    unittest.main()
