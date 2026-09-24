import asyncio
import os
from pathlib import Path
import sys
from yukkuri_realtime import YukkuriClient, wav_to_pcm


async def main():
    pcm = wav_to_pcm(Path(sys.argv[1] if len(sys.argv) > 1 else "input.wav").read_bytes())
    async with YukkuriClient(os.environ.get("YUKKURI_ENGINE_URL", "http://127.0.0.1:8765")) as client:
        print((await client.transcribe(pcm)).text)


asyncio.run(main())
