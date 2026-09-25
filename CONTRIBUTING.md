# Contributing

Read [architecture](docs/architecture.md), [protocol v1](docs/realtime-protocol.md) and [quickstart](docs/quickstart.md) first. English is canonical; keep the paired Japanese documents technically equivalent. This is an early release, not a stable externally importable Go package API.

## Development and checks

Windows x64, Go 1.27.1, Node 22+, Python 3.11+ (3.13 for the pinned sidecar dependencies). Run from repository root. Ordinary tests use fake providers: no paid keys, proprietary DLLs, GPU or downloaded models are needed.

```powershell
go test ./...
go vet ./...
go build -trimpath -o dist/engine.exe ./cmd/engine
npm --prefix sdk/typescript ci
npm --prefix sdk/typescript test
npm --prefix sdk/typescript run test:package
node --test examples/browser/*.test.cjs examples/typescript/browser-voice/history.test.mjs
python -m venv .venv
./.venv/Scripts/python.exe -m pip install -e ./sdk/python
./.venv/Scripts/python.exe -m unittest discover -s sdk/python/tests -v
./.venv/Scripts/python.exe sdk/python/tests/package_smoke.py
python -m unittest discover -s scripts -p 'test_*.py' -v
python scripts/release_check.py
```

`go vet ./...` currently reports `possible misuse of unsafe.Pointer` at the AquesTalk DLL return-pointer conversion. Do not hide the diagnostic using pointer tricks. The CI exception is limited to that analyzer for the adapter and its importing engine entry point; all other analyzers/packages still run:

```powershell
$packages = @(go list ./... | Where-Object { $_ -notlike '*/internal/providers/tts/aquestalk' -and $_ -notlike '*/cmd/engine' })
go vet $packages
go vet -unsafeptr=false ./cmd/engine ./internal/providers/tts/aquestalk
```

Race tests need CGO and a supported C compiler. Linux CI runs `go test -race ./internal/... ./cmd/stt-bench`, excluding the Windows-only engine entry point/DLL adapter by package scope/build constraints. The Windows local CGO=0 environment cannot run race tests. This exclusion is not a claim that native DLL internals have been race-tested.

Sidecar tests use a fake detector but import its Python dependencies. In a Python 3.13 virtualenv, install `tools/turn-detector/requirements.txt`, then run `python -m unittest discover -s tools/turn-detector -v`. No ONNX model is downloaded by these tests. Installed STT/Smart Turn smoke tests are opt-in; see [configuration](docs/configuration.md). Do not turn hardware-dependent skips into fake passes.

## Changes and pull requests

- Format changed Go files with `gofmt -w <files>`; preserve nearby Python/TypeScript conventions. TypeScript build includes strict type checking. There is no configured standalone lint script.
- Keep provider interfaces, transport, input, interruption, speculation, conversation and playback responsibilities separate. Do not simplify away advanced runtime behavior to make a test pass.
- Preserve public v1 field semantics, atomic metadata/PCM pairing, source-frame accounting, ID correlation and the speculative commit barrier. Add focused regression tests for behavior changes. Structural schemas and SDK generated client types must remain synchronized.
- Explain the concrete problem, resulting behavior, validation and limitations in the PR. Update EN/JA defaults, commands and limitations together. Do not claim unmeasured latency or physical playback.
- Never commit/vendor AQUEST DLLs, dictionaries, SDK files, keys, .env, models, virtualenvs or node_modules. Keep local proprietary files intact; removal from Git is not removal from disk. Run the release guard even if .gitignore already excludes a path.
- Never include private conversation text, recorded audio or real credentials in fixtures or reports.

## Release preparation (no publication)

Run the candidate guard before packaging. `python scripts/release_check.py --archive dist/source-candidate.zip` creates a reviewed working-tree source snapshot, including nonignored new files, without Git history. It refuses restricted paths, binary/large source files, obvious credential signatures and broken local documentation paths. It does not prove absence of every possible secret.

Run `python scripts/release_check.py --ref HEAD` before `git archive`/tagging. Step 10-C removed AqKanji2Koe assets from local reachable history; the Step 10 changes, including LICENSE, remain uncommitted and are not yet in a HEAD archive. GitHub main still points to the old contaminated history. The local origin remote was removed to prevent automatic fetch from restoring old refs. Remote replacement requires separate owner authorization; do not push, release or publish during local preparation. See [release report](docs/release-quality.md).

Binary distribution should contain only the freshly built Windows engine and reviewed project docs/license; users supply AQUEST, whisper binaries/models, Smart Turn dependencies/model and API credentials separately. npm pack and pip wheel are local package verification, not permission to publish.

## Project licensing

Original project code and project-authored documentation are licensed under [MIT](LICENSE) unless otherwise noted. Third-party dependencies and assets retain their own licenses and terms. Obtain AquesTalk/AqKanji2Koe and applicable rights separately from AQUEST; MIT grants no rights to their binaries, dictionaries, SDK files or keys. Project license finalization is complete; local AQUEST history cleanup is complete, while remote cleanup/publication remains a separate unresolved release gate.
