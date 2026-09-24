import asyncio
import contextlib
import io
import json
import os
from pathlib import Path
import socket
import struct
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.request
from yukkuri_realtime import YukkuriClient, YukkuriError, wav_to_pcm

ROOT = Path(__file__).resolve().parents[3]


class Integration(unittest.IsolatedAsyncioTestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory(prefix="yukkuri-sdk-")
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        binary = str(Path(cls.temp.name) / ("fixture.exe" if os.name == "nt" else "fixture"))
        subprocess.run(["go", "build", "-o", binary, "./internal/sdktest"], cwd=ROOT, check=True, capture_output=True)
        cls.process = subprocess.Popen([binary, f"127.0.0.1:{port}"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        cls.url = f"http://127.0.0.1:{port}"
        for _ in range(100):
            try:
                with urllib.request.urlopen(cls.url + "/health", timeout=1):
                    return
            except OSError:
                time.sleep(.025)
        raise RuntimeError("fixture did not start")

    @classmethod
    def tearDownClass(cls):
        cls.process.terminate()
        cls.process.wait(timeout=10)
        cls.temp.cleanup()

    async def asyncSetUp(self):
        self.client = YukkuriClient(self.url, operation_timeout=3)

    async def asyncTearDown(self):
        await self.client.close()

    async def test_health_capabilities_speak(self):
        self.assertEqual((await self.client.health())["status"], "ok")
        first = await self.client.capabilities()
        self.assertIs(first, await self.client.capabilities())
        self.assertIsNot(first, await self.client.capabilities(refresh=True))
        result = await self.client.speak("hello")
        self.assertTrue(result.request_id)
        self.assertEqual(result.format.sample_rate, 16000)
        self.assertEqual(len(wav_to_pcm(result.audio)), 3200)

    async def test_structured_error_timeout_cancel(self):
        with self.assertRaises(YukkuriError) as e:
            await self.client.speak("fail")
        self.assertEqual(e.exception.code, "generation_failed")
        self.assertTrue(e.exception.request_id)
        self.assertNotIn("private", e.exception.message)
        with self.assertRaises(YukkuriError) as e:
            await self.client.speak("slow", timeout=.03)
        self.assertEqual(e.exception.code, "timeout")
        task = asyncio.create_task(self.client.speak("slow"))
        await asyncio.sleep(.02)
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task

        async with await self.client.realtime.connect() as session:
            pending = asyncio.get_running_loop().create_future()
            session.on("error", pending.set_result)
            related = await session.send_event("input_text.commit", {"text": ""})
            error = await asyncio.wait_for(pending, 2)
            self.assertIsInstance(error, YukkuriError)
            self.assertEqual(error.related_event_id, related)
            self.assertEqual(error.request_id, session.request_id)
            self.assertTrue(error.event_id)

    async def test_transcription(self):
        self.assertEqual((await self.client.transcribe(bytes(320))).text, "fixture transcript")
        async with await self.client.transcription.connect() as session:
            await session.start()
            await session.send_audio(bytes(70000))
            result = await session.commit()
            self.assertEqual(result.language, "en")
            await session.start()
            await session.send_audio(bytes([127, 0]))
            task = asyncio.create_task(session.commit())
            await asyncio.sleep(.02)
            await session.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
        self.assertEqual(session.state, "closed")

    async def test_realtime_text_order_generation(self):
        async with await self.client.realtime.connect() as session:
            text, events = [], []
            session.on("text_delta", lambda e: text.append(e["delta"]))
            session.on("event", lambda e: events.append(e["type"]))
            gen = await session.send_text("hello")
            self.assertEqual(await gen.wait_done(timeout=3), "done")
            self.assertEqual("".join(text), "Hello. SDK fixture.")
            self.assertLess(events.index("generation.created"), events.index("response.text.delta"))
            await session.cancel_generation("stale")
            next_gen = await session.send_text("again")
            self.assertNotEqual(gen.id, next_gen.id)
            await next_gen.wait_done(timeout=3)

    async def test_audio_pairing_received_not_played(self):
        async with await self.client.realtime.connect() as session:
            packets = []
            session.on("audio", packets.append)
            gen = await session.send_text("hello", output="audio")
            await gen.wait_done(timeout=3)
            self.assertTrue(packets)
            self.assertEqual(len(packets[0].pcm), packets[0].metadata["bytes"])
            snapshot = session.playback_snapshot()
            self.assertGreater(snapshot["received_source_frames"], 0)
            self.assertEqual(snapshot["played_source_frames"], 0)
            await session.ack_played(snapshot["received_source_frames"], gen.id)
            self.assertEqual(session.playback_snapshot()["buffered_source_frames"], 0)
            with self.assertRaises(YukkuriError):
                await session.ack_played(snapshot["received_source_frames"] + 1)

    async def test_transcription_timeout(self):
        async with await self.client.transcription.connect() as session:
            await session.start()
            await session.send_audio(bytes([127, 0]))
            with self.assertRaises(YukkuriError) as e:
                await session.commit(timeout=.02)
            self.assertEqual(e.exception.code, "timeout")

    async def test_active_generation_cancel(self):
        async with await self.client.realtime.connect() as session:
            generation = await session.send_text("slow-generation")
            await generation.cancel()
            self.assertEqual(await generation.wait_done(timeout=3), "cancelled")
            self.assertEqual(session.state, "active")

    def cli(self, *args, env=None, input=None):
        return subprocess.run([sys.executable, "-m", "yukkuri_realtime", *args], input=input, text=True,
                              capture_output=True, timeout=15, env=env)

    def test_cli_help_health_caps_url_env(self):
        self.assertEqual(self.cli("--help").returncode, 0)
        health = self.cli("--url", self.url, "health")
        self.assertEqual(json.loads(health.stdout)["status"], "ok", health.stderr)
        caps = self.cli("capabilities", "--url", self.url)
        self.assertEqual(json.loads(caps.stdout)["protocol_version"], "1", caps.stderr)
        env = dict(os.environ, YUKKURI_ENGINE_URL=self.url)
        self.assertEqual(self.cli("health", env=env).returncode, 0)
        env["YUKKURI_ENGINE_URL"] = "http://127.0.0.1:1"
        self.assertEqual(self.cli("--url", self.url, "health", env=env).returncode, 0)

    def test_cli_speak_transcribe_invalid_errors(self):
        path = Path(self.temp.name) / "sample.wav"
        speak = self.cli("--url", self.url, "speak", "hello", "--output", str(path))
        self.assertEqual(speak.returncode, 0, speak.stderr)
        self.assertEqual(len(wav_to_pcm(path.read_bytes())), 3200)
        transcript = self.cli("--url", self.url, "transcribe", str(path))
        self.assertEqual(transcript.stdout.strip(), "fixture transcript", transcript.stderr)
        path.write_bytes(b"invalid")
        self.assertEqual(self.cli("--url", self.url, "transcribe", str(path)).returncode, 1)
        unavailable = self.cli("--url", "http://127.0.0.1:1", "health")
        self.assertEqual(unavailable.returncode, 1)
        self.assertIn("connection_error", unavailable.stderr)
        failure = self.cli("--url", self.url, "speak", "fail", "--output", str(path))
        self.assertEqual(failure.returncode, 1)
        self.assertIn("generation_failed", failure.stderr)
        self.assertNotIn("private", failure.stderr)
        rt = self.cli("--url", self.url, "realtime", input="hello\n")
        self.assertIn("Hello. SDK fixture.", rt.stdout)
        self.assertEqual(rt.returncode, 0, rt.stderr)


if __name__ == "__main__":
    unittest.main()
