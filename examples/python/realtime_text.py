import asyncio
import os
from yukkuri_realtime import YukkuriClient


async def main():
    async with YukkuriClient(os.environ.get("YUKKURI_ENGINE_URL", "http://127.0.0.1:8765")) as client:
        async with await client.realtime.connect() as session:
            session.on("text_delta", lambda e: print(e["delta"], end="", flush=True))
            generation = await session.send_text("札幌について教えて")
            await generation.wait_done(timeout=130)
            print()


asyncio.run(main())
