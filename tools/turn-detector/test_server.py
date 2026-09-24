import threading
import unittest
from http.server import ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from server import make_handler


class FakeDetector:
    def __init__(self):
        self.lock = threading.Lock()

    def predict(self, pcm):
        return {"probability": 0.8, "complete": True}


class ProtocolTest(unittest.TestCase):
    def setUp(self):
        self.detector = FakeDetector()
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), make_handler(self.detector))
        self.thread = threading.Thread(target=self.server.serve_forever)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}"

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def post(self, data):
        return urlopen(Request(self.url + "/predict", data=data,
                       headers={"Content-Type": "application/octet-stream"}), timeout=2)

    def test_success(self):
        with self.post(b"\0\0") as response:
            self.assertIn(b'"complete": true', response.read())

    def test_invalid_pcm(self):
        for data in (b"", b"1", b"0" * 256002):
            with self.assertRaises(HTTPError) as error:
                self.post(data)
            self.assertEqual(error.exception.code, 400)
            error.exception.close()

    def test_busy(self):
        with self.detector.lock:
            with self.assertRaises(HTTPError) as error:
                self.post(b"\0\0")
        self.assertEqual(error.exception.code, 503)
        error.exception.close()


if __name__ == "__main__":
    unittest.main()
