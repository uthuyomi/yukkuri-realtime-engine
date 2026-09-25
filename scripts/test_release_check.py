import unittest
from release_check import forbidden, check_content


class ReleaseSafety(unittest.TestCase):
    def test_restricted_assets_and_secrets_even_outside_vendor_directories(self):
        for path in ["internal/providers/tts/aquestalk/aqk2k_win/readme.txt", "copy/AqKanji2Koe.lib",
                     "copy/aq_dic/CREDITS", "runtime/whisper/models/ggml-small.bin", ".env",
                     ".env.local", ".venv-release/pyvenv.cfg", "sdk/typescript/node_modules/x.js",
                     "keys/client.pem", "../outside.md"]:
            with self.subTest(path=path):
                self.assertTrue(forbidden(path))

    def test_source_and_example_are_allowed(self):
        for path in [".env.example", "internal/providers/tts/aquestalk/provider_windows.go",
                     "tools/turn-detector/SMART-TURN-LICENSE", "docs/providers.ja.md"]:
            self.assertFalse(forbidden(path))

    def test_secret_value_never_appears_in_diagnostic(self):
        secret = b"sk-" + b"a" * 40
        errors = check_content("config.txt", secret)
        self.assertTrue(errors)
        self.assertNotIn(secret.decode(), str(errors))

    def test_binary_disguised_as_source_is_rejected(self):
        self.assertTrue(check_content("assets/example.txt", b"MZ\0binary"))


if __name__ == "__main__":
    unittest.main()
