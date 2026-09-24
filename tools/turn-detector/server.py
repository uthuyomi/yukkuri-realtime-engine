"""Loopback-only Smart Turn CPU inference. No Agents framework or STT required."""
import argparse
import json
import logging
import math
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import numpy as np
import onnxruntime as ort
from transformers import WhisperFeatureExtractor


class Detector:
    def __init__(self, model, threshold):
        options = ort.SessionOptions()
        options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
        options.inter_op_num_threads = 1
        options.intra_op_num_threads = 1
        self.session = ort.InferenceSession(
            model, sess_options=options, providers=["CPUExecutionProvider"]
        )
        self.extractor = WhisperFeatureExtractor(chunk_length=8)
        self.threshold = threshold
        self.lock = threading.Lock()

    def predict(self, pcm):
        # Official Smart Turn preprocessing: last 8 s, left zero padding, then
        # normalized Whisper features. This extractor does not load Whisper STT.
        audio = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
        audio = audio[-128000:]
        audio = np.pad(audio, (128000 - len(audio), 0))
        features = self.extractor(
            audio, sampling_rate=16000, return_tensors="np",
            padding="max_length", max_length=128000, truncation=True,
            do_normalize=True,
        ).input_features.astype(np.float32)
        probability = float(self.session.run(None, {"input_features": features})[0].item())
        if not math.isfinite(probability) or not 0 <= probability <= 1:
            raise ValueError("Model returned invalid probability")
        return {"probability": probability, "complete": probability > self.threshold}


def make_handler(detector):
    class Handler(BaseHTTPRequestHandler):
        def setup(self):
            super().setup()
            self.connection.settimeout(3)

        def reply(self, status, body):
            payload = json.dumps(body, allow_nan=False).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            try:
                self.wfile.write(payload)
            except (BrokenPipeError, ConnectionResetError):
                pass  # the Go request was cancelled; native inference is bounded

        def do_GET(self):
            if self.path != "/health":
                self.reply(404, {"error": "not found"})
                return
            self.reply(200, {"model": "smart-turn-v3.2-cpu", "ready": True})

        def do_POST(self):
            if self.path != "/predict":
                self.reply(404, {"error": "not found"})
                return
            try:
                length = int(self.headers.get("Content-Length", "0"))
            except ValueError:
                length = 0
            if (length <= 0 or length > 256000 or length % 2
                    or self.headers.get("Content-Type") != "application/octet-stream"
                    or self.headers.get("Transfer-Encoding")):
                self.reply(400, {"error": "expected bounded PCM16 LE 16kHz mono"})
                return
            # Bound native work: no unbounded inference executor queue when clients
            # cancel or multiple sessions pause simultaneously. Busy is observable.
            if not detector.lock.acquire(blocking=False):
                self.reply(503, {"error": "detector busy"})
                return
            try:
                pcm = self.rfile.read(length)
                if len(pcm) != length:
                    self.reply(400, {"error": "truncated PCM"})
                    return
                self.reply(200, detector.predict(pcm))
            except Exception:
                logging.exception("turn inference failed")
                self.reply(500, {"error": "turn inference failed; see sidecar log"})
            finally:
                detector.lock.release()

    return Handler


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--model", required=True)
    parser.add_argument("--port", type=int, default=8766)
    parser.add_argument("--threshold", type=float, default=0.5)
    args = parser.parse_args()
    if not 0 < args.threshold < 1:
        parser.error("threshold must be between 0 and 1")
    detector = Detector(args.model, args.threshold)
    with ThreadingHTTPServer(("127.0.0.1", args.port), make_handler(detector)) as server:
        print(f"Smart Turn ready on http://127.0.0.1:{args.port}", flush=True)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass


if __name__ == "__main__":
    main()
