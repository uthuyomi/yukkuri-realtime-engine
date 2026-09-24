import argparse
import asyncio
import json
import os
from pathlib import Path
import struct
import sys
from . import YukkuriClient, YukkuriError, wav_to_pcm


def parser():
    p = argparse.ArgumentParser(prog="yukkuri", description="Yukkuri Realtime Engine Public API v1 client")
    p.add_argument("--url", default=None, help="Engine URL (overrides YUKKURI_ENGINE_URL)")
    p.add_argument("--timeout", type=float, default=130, help="Operation timeout in seconds")
    sub = p.add_subparsers(dest="command", required=True)
    for name in ("health", "capabilities", "speak", "transcribe", "realtime"):
        cmd = sub.add_parser(name)
        # Accept URL before or after the command without overwriting the global value.
        cmd.add_argument("--url", default=argparse.SUPPRESS)
        if name == "speak":
            cmd.add_argument("text")
            cmd.add_argument("--output", required=True, type=Path)
            cmd.add_argument("--voice")
            cmd.add_argument("--speed", type=float)
        if name == "transcribe":
            cmd.add_argument("input", type=Path)
    return p


def _as_wav(result):
    if result.format.encoding == "wav":
        return result.audio
    f, pcm = result.format, result.audio
    if f.encoding != "pcm_s16le" or f.channels != 1 or f.sample_rate <= 0 or len(pcm) % 2:
        raise YukkuriError("unsupported_format", "Cannot save this response as PCM16 WAV.")
    return b"RIFF" + struct.pack("<I", 36 + len(pcm)) + b"WAVEfmt " + struct.pack("<IHHIIHH", 16, 1, 1, f.sample_rate, f.sample_rate * 2, 2, 16) + b"data" + struct.pack("<I", len(pcm)) + pcm


async def run(args):
    url = args.url or os.environ.get("YUKKURI_ENGINE_URL") or "http://127.0.0.1:8765"
    async with YukkuriClient(url, http_timeout=args.timeout, operation_timeout=args.timeout) as client:
        if args.command == "health":
            print(json.dumps(await client.health(), ensure_ascii=False))
        elif args.command == "capabilities":
            print(json.dumps(await client.capabilities(refresh=True), ensure_ascii=False, indent=2))
        elif args.command == "speak":
            result = await client.speak(args.text, voice=args.voice, speed=args.speed)
            args.output.write_bytes(_as_wav(result))
            print(f"Saved {len(result.audio)} audio bytes to {args.output}")
        elif args.command == "transcribe":
            # Refuse oversized files before allocating; WAV metadata allowance is 1 MiB.
            if args.input.stat().st_size > 3840000 + 1048576:
                raise YukkuriError("payload_too_large", "Input WAV exceeds the manual turn budget.")
            pcm = wav_to_pcm(args.input.read_bytes())
            if len(pcm) > 3840000:
                raise YukkuriError("payload_too_large", "Input PCM exceeds 120 seconds.")
            print((await client.transcribe(pcm)).text)
        else:
            async with await client.realtime.connect() as session:
                session.on("text_delta", lambda e: print(e["delta"], end="", flush=True))
                while True:
                    try:
                        text = await asyncio.to_thread(input, "> " if sys.stdin.isatty() else "")
                    except EOFError:
                        break
                    if not text.strip():
                        continue
                    generation = await session.send_text(text)
                    await generation.wait_done(timeout=args.timeout)
                    print()


def main(argv=None):
    args = parser().parse_args(argv)
    try:
        asyncio.run(run(args))
        return 0
    except YukkuriError as e:
        ids = " ".join(f"{k}={v}" for k, v in {"request_id": e.request_id, "event_id": e.event_id, "generation_id": e.generation_id}.items() if v)
        print(f"{e.code}: {e.message}" + (f" ({ids})" if ids else ""), file=sys.stderr)
        return 1
    except (OSError, ValueError):
        print("local_error: Check the input file, output path and URL.", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
