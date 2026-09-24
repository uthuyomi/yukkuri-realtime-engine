import asyncio
import os
from pathlib import Path
from yukkuri_realtime import YukkuriClient


async def main():
    async with YukkuriClient(os.environ.get("YUKKURI_ENGINE_URL", "http://127.0.0.1:8765")) as client:
        result = await client.speak("ゆっくりしていってね")
        Path("hello.wav").write_bytes(result.audio)


asyncio.run(main())
