# v0.1.0 release preparation / Step 10 audit

Audit date: 2026-09-25. Target is an **unreleased early v0.1.0**, Public API v1. This report records local preparation, not a published release or independent reproduction of the microphone demo.

## Scope and source of truth

Reviewed the tracked-file inventory, source interfaces and lifecycle paths under cmd/internal, public HTTP routes and WebSocket validator/schema/catalog, environment parsing, provider availability, SDK/CLI entry points, examples, build/setup scripts, dependency declarations and existing test inventory. All 154 remaining initially tracked working files were scanned for binary/large content and common credential signatures after asset removal. Focused source review covered admission/cancellation, metadata/PCM pairing, source-frame ACK, speculative promotion, final transcript ownership, error sanitation and subprocess containment. This is a repository audit, not a formal verification or exhaustive external penetration test.

Public routes match `internal/transport/http/server.go`: GET /health, GET /v1/capabilities, POST /v1/audio/speech, WS /v1/realtime and WS /v1/transcription. Public protocol is string major version "1". Both SDKs negotiate credit-v1 and keep received/played positions separate. CLI reuses Python SDK and does not own audio devices. Final-only STT, lack of reconnect restoration, and provider availability versus readiness are documented explicitly.

Dependency baseline: Go 1.27.1; coder/websocket 1.8.15; godotenv 1.5.1; TypeScript ~5.9.3 (lockfile), Node >=22; Python >=3.11, httpx >=0.28,<1, websockets >=15,<18, build setuptools >=77. Sidecar pins numpy 2.4.3, onnxruntime 1.24.4, transformers 4.57.6, tested here in Python 3.13.5. Browser CDN pins vad-web 0.0.31 and onnxruntime-web 1.22.0. Whisper setup pins d09f61a708f3487afa956ff578e60eae5e7a233c. Dependency pinning does not itself guarantee absence of vulnerabilities.

## Proprietary assets: original Step 10 audit (before Step 10-C)

Seventeen AqKanji2Koe SDK files under `internal/providers/tts/aquestalk/aqk2k_win/` were tracked, including a 9,847,992-byte `aq_dic/aqdic.bin`, user dictionary, CREDITS, manual PDF, headers, import libraries and vendor samples. With explicit owner authorization, `git rm --cached` removed all 17 from the index. Every local file remained present and its SHA-256 matched the pre-removal value. No local SDK/dictionary/runtime assets were deleted or edited.

`git ls-files` for both aqk2k_win and aqtk1_win is empty after the change. No tracked AquesTalk1 assets were found in its designated directory. The remaining tracked-file extension/content/signature scan found no DLL/lib/exe/model binaries, .env, virtualenv, node_modules or recognized private-key/OpenAI/GitHub token signatures. This is a limited signature-based conclusion, not proof of absence of every secret or licensing restriction.

**The original Step 10 HEAD contained those assets** because no cleanup commit had been authorized or created at that stage. Staged deletions describe the next commit, not an altered HEAD. Existing history includes commit `2502c8e` (Implement realtime PCM playback and playback timeline) containing AqKanji2Koe assets. No history rewriting occurred during Step 10/B; the authorized local rewrite is recorded under Step 10-C below. A clean source candidate does not clean Git history. Before publishing the repository with history, the owner must resolve history cleanup or choose another reviewed publication approach. Do not push/force-push/tag/release the current historical repository as if it were clean.

Existing ignore rules for both AQUEST directories were retained. Added runtime/virtualenv/output/model exclusions and `.gitattributes` export exclusions. `scripts/release_check.py` rejects restricted paths even if force-added, binary/large source files and common secret signatures; it checks local Markdown paths and can audit a committed tree. It creates a source candidate from tracked working files plus nonignored new files, never recursively copying ignored assets or .git. Future committed source archives require the guard on the exact chosen commit; the original HEAD failed the guard; the rewritten local HEAD now passes.

## Changes

New public documentation: README.ja.md; EN/JA architecture, quickstart, configuration, providers, performance, troubleshooting; Japanese protocol counterpart. English README rewritten as the landing page. Existing detailed protocol/API/SDK/runtime documents remain available; historical CPU/CUDA availability statements are scoped to their original measurements.

Added CHANGELOG.md, CONTRIBUTING.md, SECURITY.md, .env.example, source export attributes, CI, release guard and its tests, repeated-session lifecycle regression and environment-log privacy regression. Browser Voice files from the preceding task were preserved in the candidate and included in tests.

Implementation changes: startup .env error logging now uses a fixed safe diagnostic instead of printing godotenv's raw parser error, which can contain a credential-bearing input line. Tests exercise a malformed secret-bearing file. A full-suite rerun also exposed a pre-existing STT shutdown race: queued inference could acquire the slot before its asynchronous root-cancellation callback ran, then reload/invoke a worker. The existing fake-worker test panicked with `close of closed channel` at `runtime_test.go:173`. The runtime now checks closed/root-cancelled state synchronously after acquiring the slot. The test records load/inference counts instead of panicking, asserts no queued work starts, and a new cancelled-root test verifies zero inference calls. Both focused tests passed 100 repetitions. This is a justified lifecycle correctness fix, not an architecture/protocol redesign. No breaking changes.

Documentation correction: AquesTalk speed is a ratio (`1.0` normal), not `100`; only examples/explanation changed. TTS paths and keys are Go config, not invented environment variables. Missing timing values remain unavailable, and notification TTFA is not physical audible latency.

## Reliability coverage

| Concern | Evidence / limitation |
| --- | --- |
| Long sessions | Worklet ten-minute streaming test, fixed buffers and exact final frames; conversation group/byte eviction tests. No multiday hardware soak performed. |
| Connect/disconnect/reconnect | New 80-session sequential real-WebSocket regression (over the 64-slot admission bound), alternating realtime/transcription and graceful/abrupt closes; fresh IDs, released slots. No restoration feature is claimed. |
| Cancellation/rapid interruption | Existing input/audio/interruption tests cover stale generation tokens, concurrent finalization, retained playback and disconnect. |
| Speculative cancellation | Existing revision, timeout, busy admission, promotion, fallback and disconnect tests. |
| Provider failure/timeout | Fake STT worker crash/cancel/reap/reload, CUDA-evidence rejection, transport error privacy and noncompliant-provider bounded cleanup tests. |
| Slow clients / credit exhaustion | Credit window, duplicate snapshot, timeout/cancel, atomic writer, backpressure/control tests. |
| Malformed/oversized protocol | Validator/schema, HTTP/WS size/format/origin tests, SDK binary-pairing/timeout tests. |
| Resource cleanup / saturation | Input queue overflow, microphone startup cancellation, admission capacity and worker cleanup tests; no claim that native DLL internals are race-safe. |
| Turn detector failure | Fake HTTP contract, invalid PCM, busy, cancellation and endpoint deadline/stale-result tests. Real model smoke is opt-in. |

## Quality results

Local environment: Windows amd64, Go 1.27.1, Node 22.21.1, Python 3.13.5; Go CGO_ENABLED=0, no gcc on PATH.

| Command / check | Result |
| --- | --- |
| `go test ./...` / final JSON run | Exit 0; 118 top-level tests passed, 3 opt-in tests skipped. Separate opt-in executions below passed all three. Subtests are not double-counted. |
| `go vet ./...` | Exit 1: `internal/providers/tts/aquestalk/provider_windows.go:324:11: possible misuse of unsafe.Pointer` (pre-existing native return pointer). |
| Split vet from CONTRIBUTING | Exit 0. Full vet for other packages; `-unsafeptr=false` only for AquesTalk and its importing cmd/engine. Full vet on cmd/engine alone also traverses the adapter and reproduces the warning. |
| `go test -race ./internal/...` locally | Could not execute: `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`. Environment limitation; no local C compiler. |
| `npm ci` / `npm test` in SDK | Install succeeded; 20 tests passed (build includes strict tsc). npm reported 0 vulnerabilities for its two-package install, not a whole-project security verdict. |
| `npm run test:package` in SDK | Passed installed tarball core/browser/Worklet exports. No publication. |
| `node --test examples/browser/*.test.cjs examples/typescript/browser-voice/history.test.mjs` | 30 passed, 0 failed/skipped. VM/unit tests, not a real-device browser run. |
| Initial global Python SDK test command | Failed to import two test modules: `ModuleNotFoundError: No module named 'yukkuri_realtime'`. Missing local installation, not regression. |
| Python SDK tests after isolated editable install | 13 passed, including CLI health/capabilities, file errors, speech/transcription and session tests. |
| Python wheel smoke | Passed isolated wheel import, CLI entry point and py.typed; no publication. |
| Smart Turn fake-detector tests | 3 passed in existing sidecar venv; no model inference. Transformers optional PyTorch/TensorFlow/Flax warning is expected for this ONNX/feature-extractor path. |
| Installed whisper opt-in tests | 2 passed: CLI cancellation and persistent worker cancellation/reaping. Used existing CUDA-directory executables with CPU mode (`-ng`) and local small model; this is not CUDA inference validation. |
| Smart Turn real model smoke | 1 passed on an owned temporary sidecar port 18766 using existing ONNX weights and synthetic silence; the sidecar was stopped afterwards. This is not microphone accuracy validation. |
| Release guard tests | 4 passed: restricted assets, safe source, redacted secret detection, disguised binary. |
| Source archive / clean build | Source candidate zip excludes .env, vendor directories, models/runtime and Git history. Extracted into a fresh temporary directory and built `./cmd/engine` successfully without proprietary assets. |

No standalone TypeScript/Python lint configuration exists; do not claim a lint pass. No actual external LLM calls, proprietary synthesis, CUDA inference, real-microphone run or long hardware soak was repeated. Earlier user-observed GTX 1660 numbers remain attributed observations. Browser launch was blocked during the preceding UI task; no visual/device pass is claimed here.

## Security review

Confirmed loopback bind, shared HTTP/WS Origin policy, request IDs, fixed public error messages, protocol/body/audio bounds, source-frame validation and atomic output framing, direct subprocess invocation and bounded worker ownership. Found/fixed .env parser diagnostic disclosure and the queued STT shutdown lifecycle bug. Local-only assumptions, same-origin/loopback exceptions, no authentication, no HTTP speech concurrency quota, no ordinary LLM total deadline, native cancellation limits and local path/traceback diagnostics are now documented in SECURITY.md. A read-only scan of 276 reachable historical blobs (7 binary blobs) found no recognized credential signatures in text. Binary blobs are not certified secret-free. Object deduplication means blob path listings are not complete per-tree path inventories. No claim of production hardening or complete vulnerability absence.

## CI and distribution

Prepared `.github/workflows/quality.yml`: Windows Go build/tests and narrow vet exception; TS/browser/package tests; Python SDK/CLI/wheel; Linux portable-core race; Linux fake-detector tests with pinned dependencies and no ONNX model. Jobs use no private API key, proprietary SDK, NVIDIA GPU or downloaded whisper model. Workflow execution on GitHub is not performed or claimed. Read-only contents permission, concurrency cancellation and job timeouts are set. No release/upload/publish job is added.

Use [CONTRIBUTING](../CONTRIBUTING.md) checks, then `python scripts/release_check.py --archive dist/source-candidate.zip`. Build Windows engine with `go build -trimpath -o dist/engine.exe ./cmd/engine`. npm tarball and Python wheel are separate SDK packages; never zip the whole working directory or runtime. Users supply AQUEST, whisper executables/models, Smart Turn model/dependencies and external credentials separately. A release must verify the actual artifact list/checksums and repository license before publication.

## Clean-room and bilingual review

Start at either README, follow paired quickstart/configuration/provider/troubleshooting links. Commands use a generic clone root rather than a developer-specific absolute directory. The quickstart specifies prerequisites, separately obtained proprietary assets, explicit model download, sidecar startup, Engine configuration, SDK build and HTTP hosting. CPU-only and explicit CUDA policies are distinct. Both languages retain the same identifiers, defaults, protocol catalog, performance figures, units and licensing caveats. Local Markdown targets and accidental encoding loss are checked automatically; external link availability and translated prose still require human review.

## Remaining release gates

- Local reachable history is clean after Step 10-C. GitHub main still references contaminated history; remote replacement and any GitHub retention cleanup require separate owner authorization/review. The local origin configuration was removed to prevent automatic refetch. Step 10 working changes remain uncommitted; review and an authorized commit are still needed before publication.
- Full default Windows vet retains its documented native-pointer warning; the exception needs maintainer acceptance. Portable-core race job is prepared but not executed locally/GitHub in this task.
- Existing installed whisper CPU cancellation/reaping and Smart Turn model smoke were run successfully. Proprietary TTS, real microphone, CUDA inference and fresh external download/install reproduction were not rerun. External model/API availability is not guaranteed by source defaults.

All authorized repository preparation should be reviewed before any commit. No commit, push, force-push, GitHub Release, npm publish or PyPI publish has been performed.

## Completion gate accounting

| Gate | Status |
| --- | --- |
| Repository inventory/source, API/protocol/provider/configuration/SDK/CLI review | Completed with audit scope above; not formal verification |
| README EN/JA | Completed: MIT scope and root LICENSE links match in EN/JA |
| Architecture, quickstart, configuration, protocol, providers, performance, troubleshooting EN/JA | Written and local links/technical parity checked |
| CHANGELOG, CONTRIBUTING, SECURITY | Written |
| LICENSE | **Complete: standard MIT; Copyright (c) 2026 uthuyomi, explicitly designated by the owner** |
| .env.example and .gitignore | Verified against source environment names and actual ignored assets |
| Proprietary/sensitive tracked-file audit | Local reachable history cleaned in Step 10-C; local assets preserved; remote historical exposure remains |
| Go tests / vet | Tests passed; full vet executed with known native-pointer failure; documented split passed |
| TS build/tests/package / Python SDK/CLI/wheel / browser unit tests | Passed |
| Relevant reliability regression tests | Added and passed; STT shutdown fix exercised 100 repetitions |
| Security review | Completed within local-development threat model; two targeted fixes above |
| CI | Prepared, not executed on GitHub; no release/publish automation |
| Clean build / source candidate | Asset-free extracted source build verified; no restricted assets in source candidate |
| Clean-room navigation / EN/JA consistency | Commands, configuration identifiers/defaults, event fields, performance rows and local links checked |
| Environment-dependent validation | Three installed-provider opt-in smokes passed separately; race, CUDA, proprietary TTS and real-device browser exclusions documented |
| Public release readiness | **Blocked by remote historical-asset cleanup/publication authorization; remaining quality gates above still require review** |

Suggested commit message after review (not executed):

```text
chore(release): prepare v0.1.0 documentation, asset audits and quality gates
```

Include the .env diagnostic and STT shutdown correctness fixes in the commit description, or split them into dedicated fix commits during the owner's later review.

## Step 10-B: MIT license finalized

The root [LICENSE](../LICENSE) contains the standard MIT text and exactly `Copyright (c) 2026 uthuyomi`. The project owner explicitly designated `uthuyomi` as the public copyright holder in the follow-up instruction. This is the source of authority; the identity was not guessed from Git metadata. The LICENSE completion gate is complete.

README EN/JA, providers EN/JA, CONTRIBUTING, SECURITY and CHANGELOG now link the finalized license and contain no copyright-name confirmation gate. MIT applies to original project code and project-authored documentation unless otherwise noted. Third-party components retain their own licenses and terms, including whisper.cpp, Smart Turn, Silero VAD/vad-web, ONNX Runtime, coder/websocket and SDK/browser dependencies. AquesTalk/AqKanji2Koe remain proprietary AQUEST software; users obtain assets and applicable rights separately. No AQUEST usage or redistribution rights are granted by this project's MIT License. At Step 10-B, reviewed source candidates excluded restricted assets while HEAD/history still contained them. Step 10-C below supersedes the local-history status.

Go module metadata has no license field. The TypeScript package and lockfile root and Python project metadata have no project license fields; they remain unchanged, as do all dependency license fields. Package JSON/TOML and name/version consistency were checked during Step 10-B.

Final validation: release guard unit tests passed (4); `python scripts/release_check.py` passed for 184 candidate files, including Markdown/local links and EN/JA contract checks; `git diff --check` passed (only existing LF-to-CRLF configuration warnings). LICENSE references in both READMEs, providers, CONTRIBUTING, SECURITY, CHANGELOG and this report resolve correctly. No stale license/name-pending wording was found. Package metadata parsing/consistency passed. Both AQUEST directories remain absent from the index; all 17 staged-removal files still exist locally. The status inventory below matches `git status --short`. The source archives existing at Step 10-B predated license finalization; Step 10-C regenerated and inspected a new candidate. No provider/hardware rerun is warranted by these documentation-only changes.

Step 10-B performed no local asset deletion, commit, tag, push, release, publish or history rewrite. Local-history cleanup was subsequently authorized and performed in Step 10-C below; remote cleanup remains deferred.

## Step 10-C: local history cleanup

### Backup and preservation

Verified backup directory: ../yukkuri-step10c-backup-20260925-122317/, relative to the checkout root (a sibling outside this repository). This private backup contains restricted assets and must not be published.

- before.bundle: all original refs/history; git bundle verify passed.
- git-metadata/: full original Git metadata, index and reflogs. live-original.git/ additionally retains the metadata moved out during installation.
- working.patch and index.patch: binary/full-index diffs. working-files/: lossless copies of all source candidate and local AQUEST files.
- manifest.json: SHA-256 for 250 files, including 66 local files under aqk2k_win/aqtk1_win. All backup copies matched, and all 250 originals matched again after metadata installation, before intentional documentation updates.
- refs.txt, remotes.txt, remote-before.txt, removal-paths.txt: original refs, remote evidence and exact removal set.
- history_audit.py and pre-audit/, post-audit/, final-active-audit/: reproducible all-ref/tree/blob inventories and signature results; values of candidate credentials were not emitted.
- rewritten.git/: filtered mirror. clean-validation/: fresh single-branch clone with the uncommitted candidate and fresh test dependencies.

No vendor assets were added to a new commit. The temporary mirror copied pre-existing history solely for the authorized rewrite. Local ignored development assets, .env, runtimes and environments were left intact.

### Original and rewritten refs

Original HEAD/main and origin/main resolved to 631962e75b03362f423e7c2f895977c0c472cc39. Original refs:

```text
e8c4261ea45a9bb0680c28bc2becdc22e598fdf8 refs/codex/turn-diffs/captures/1790306563883/16d37525-4650-4bbf-bb11-51faa58d32f3/base
e8c4261ea45a9bb0680c28bc2becdc22e598fdf8 refs/codex/turn-diffs/checkpoints/8d3a9129779734912abde18e9d128584/9fafe3b431c04b3eda284758eda7d59c/1790306213525/d5cf1f28-7b12-4eb0-8d2d-70ce43ce47b2
631962e75b03362f423e7c2f895977c0c472cc39 refs/heads/main
631962e75b03362f423e7c2f895977c0c472cc39 refs/remotes/origin/HEAD
631962e75b03362f423e7c2f895977c0c472cc39 refs/remotes/origin/main
```

Current HEAD/main is bab0602e695918b680c21756f42b907f828aa3cb, the sole active ref. The two tool snapshot refs pointed to tree e8c4261ea45a9bb0680c28bc2becdc22e598fdf8, not commits. Their complete trees were audited separately because rev-list alone skips tree-valued refs; they remain preserved in the backup/mirror but were not imported into the active publication clone.

### Inventory and exact removal set

Scanned every tree in all 12 original commits and every ref-root tree: 314 unique blobs including snapshot-only files. Seven binary blobs were all confirmed vendor files. The removal set contains 17 paths / 15 distinct blobs. No historical aqtk1_win asset, relocated AQUEST asset, unrelated binary/model or actual .env was found. The .env.example match was a public template. Recognized credential signatures: zero. Assignment candidates were local test placeholders. Other AQUEST mentions were project-authored integration, docs, tests or packaging configuration and were retained. This bounded audit is not proof of absence of every possible secret.

Exact removed paths:

```text
internal/providers/tts/aquestalk/aqk2k_win/aq_dic/CREDITS
internal/providers/tts/aquestalk/aqk2k_win/aq_dic/aq_user.dic
internal/providers/tts/aquestalk/aqk2k_win/aq_dic/aqdic.bin
internal/providers/tts/aquestalk/aqk2k_win/aqk2k_win_man.pdf
internal/providers/tts/aquestalk/aqk2k_win/lib/AqKanji2Koe.h
internal/providers/tts/aquestalk/aqk2k_win/lib/AqKanji2Koe.lib
internal/providers/tts/aquestalk/aqk2k_win/lib/AqUsrDic.h
internal/providers/tts/aquestalk/aqk2k_win/lib/AqUsrDic.lib
internal/providers/tts/aquestalk/aqk2k_win/lib64/AqKanji2Koe.h
internal/providers/tts/aquestalk/aqk2k_win/lib64/AqKanji2Koe.lib
internal/providers/tts/aquestalk/aqk2k_win/lib64/AqUsrDic.h
internal/providers/tts/aquestalk/aqk2k_win/lib64/AqUsrDic.lib
internal/providers/tts/aquestalk/aqk2k_win/readme.txt
internal/providers/tts/aquestalk/aqk2k_win/samples/Kanji2KoeCmd/Kanji2KoeCmd.cpp
internal/providers/tts/aquestalk/aqk2k_win/samples/Kanji2KoeCmd/Kanji2KoeCmd.vcxproj
internal/providers/tts/aquestalk/aqk2k_win/samples/Kanji2KoeCmd/testdata/sample.koe
internal/providers/tts/aquestalk/aqk2k_win/samples/Kanji2KoeCmd/testdata/sample.txt
```

### Rewrite and independent verification

Used git-filter-repo 2.47.0, revision a40bce548d2c, in a fresh git clone --mirror --no-local, with --invert-paths --paths-from-file removal-paths.txt. No --force bypass, filter-branch or new project commit was used. The dirty development working tree was not the filter target. Clean single-branch clone metadata was installed while leaving working files intact. Staged vendor deletions disappeared because the new base no longer tracks those paths.

Filter-repo warned that tool refs point to trees and skipped rewriting them. Independent full-tree inspection verified no vendor assets in those snapshots. A Windows hidden-directory move initially moved metadata contents but failed to remove the original container. The layout was inspected and clean metadata installed into the remaining directory; all 250 working-file hashes subsequently matched. No working data was lost.

For every old/new commit pair, full path/mode/blob trees were compared. After excluding exactly the 17 vendor paths, all 12 trees match. Project-authored provider_windows.go, kanji2koe_windows.go and all other nonvendor contents remain unchanged throughout history.

Commit mapping:

```text
old                                      new
0a893741120b175addd31be0d5fdfdc762bc6262 b49e15b9f60ede94a24618dde761b562f0e8952e
2502c8ef4dbeda79dd87f1f67358e8ad52bbae4f 78f18537232cb7404069720795b98f600358d38d
3ec858c061911cef304eb7b8a5e9fa60f89184c0 bf4e746ad9ccce5ad17337f543cbc34a498bac02
631962e75b03362f423e7c2f895977c0c472cc39 bab0602e695918b680c21756f42b907f828aa3cb
72b426b1efee5e729bb504105e2d8011e246bc9b dcf25aaa6d54c78f1548e52e9b3819d36b5c9081
80db29e7961d574e60e1ccc3ccfaea14886d93b9 80db29e7961d574e60e1ccc3ccfaea14886d93b9
85f0c01566b74d617d6e83503404d40fb4b9cee1 7efa4a018270090d4888827f5c39685510f72bc0
c4dae7da2126457921e6b85865a6fbce03cb63c4 c4dae7da2126457921e6b85865a6fbce03cb63c4
ccd164d11d1d75151d44c742361a461520fc27c6 42bb99db1f82e896ea297b36aa4a4219685c11c4
ddc31456c383ab59558ba093f1d2595fd960e714 95f1d9a90b9f1ac6177f5dc288905b0e5a65a1ad
e149a585ccc174e05f426d74afc96fe36367d419 cc5be67be8259fe2a78cf8943e340d067a5e0854
e2a2008f955126a092da12b92300a6c29c247a9a e2a2008f955126a092da12b92300a6c29c247a9a
```

Filtered mirror scan: 299 blobs including clean snapshots. Final active all-ref scan: 12 commits / 255 blobs, zero restricted paths, binary/large candidates or credential signatures. All 15 known vendor blobs are unreachable, including aqdic.bin 141d456ba078062f112bf65609ac864ec36f1851. Path inventory, blob-set comparison and exact tree comparison agree.

Reachability is distinct from physical absence: after restoring origin, an external/background fetch recreated the old origin/main. New main remained clean. Origin was removed locally to prevent repeated refetch, removing that contaminated tracking ref, and the all-ref audit passed again. Old objects, including the dictionary, remain physically present but unreachable from intended publish refs. git fsck reports the original tip as dangling. No additional garbage collection was performed or required. Backups intentionally preserve old history.

### Remote comparison and future operations

Original remote origin used https://github.com/uthuyomi/yukkuri-realtime-engine.git for fetch/push. Read-only ls-remote returned:

```text
631962e75b03362f423e7c2f895977c0c472cc39 HEAD
631962e75b03362f423e7c2f895977c0c472cc39 refs/heads/main
```

Remote main exactly matches the contaminated original tip; the assets have already been pushed. No other advertised branch/tag was returned. Hidden refs/caches are not certified absent. Among advertised refs, only main requires replacement; no branch/tag deletion is indicated. The active checkout has no configured remote to avoid automatic reinfection. GitHub was not mutated.

After owner review, an authorized commit of all prepared work, and explicit remote-mutation authorization, these narrowly scoped operations WOULD be needed (not executed):

```powershell
git remote add origin https://github.com/uthuyomi/yukkuri-realtime-engine.git
git ls-remote origin
git push --force-with-lease=refs/heads/main:631962e75b03362f423e7c2f895977c0c472cc39 origin main:refs/heads/main
```

The explicit lease must fail if remote main changes; review new work rather than bypassing it. Do not use mirror/all-ref push. Replacing main alone does not certify removal from GitHub caches, hidden refs or other clones; review provider-side retention and collaborators' old clones before declaring remote cleanup complete. These are next-task steps, not authorization.

### Clean clone and quality results

Cloned only rewritten main with --no-local --single-branch into clean-validation. Since no new commit is authorized, rewritten HEAD alone does not yet include LICENSE/README.ja.md or other new Step 10 files. The preserved 184-file candidate was reapplied as uncommitted work. Validation covers this combined candidate, not a claimed committed release. No developer .env, vendor directories, models or runtime environments were copied. Test dependencies came from npm ci and a fresh Python .venv.

| Clean-candidate check | Result |
| --- | --- |
| go test ./... | Passed; opt-in hardware tests not enabled |
| Documented split vet | Passed; existing narrow native-pointer analyzer exception retained |
| npm ci; npm test | Passed strict build and 20 tests |
| npm run test:package | Passed installed tarball exports; no publication |
| Browser/turn-history node tests | 30 passed |
| Fresh .venv editable install; Python SDK/CLI unittest | 13 passed; informational asyncio slow-task diagnostics |
| Release guard unittest | 4 passed |
| release_check.py --ref HEAD | Passed candidate links/contracts and rewritten committed-tree scan |
| git diff --check | Passed; existing LF/CRLF configuration warnings only |

No unrestricted Windows vet pass is claimed. GitHub CI, race, native synthesis, CUDA, real microphone and installed-model/provider smokes were not rerun in Step 10-C; earlier results remain scoped to their original runs.

Final source candidate: dist/source-candidate-step10c.zip, regenerated after LICENSE and cleanup. Verification checks LICENSE and both READMEs, forbidden members, exact candidate bytes, and an extracted Windows engine build. SHA-256 is stored separately in dist/source-candidate-step10c.sha256 to avoid a self-referential archive hash. This source candidate contains uncommitted preparation, not a published release.

### Remaining gates and next step

Local reachable-history cleanup is complete; remote main remains contaminated. All 66 local vendor files retain their original SHA-256 and remain ignored/untracked; git ls-files for both directories is empty. Step 10 work and LICENSE remain uncommitted. No new project commit, push, force-push, tag, release or package publication occurred.

Next: owner review of the prepared changes and existing quality limitations, followed by separately authorized commits and remote cleanup. Before v0.1.0, resolve remote history/retention, the documented vet exception and CI/race acceptance, and verify artifacts from the actual reviewed commit.

Final local log (12 commits, requested maximum 15):

```text
bab0602 (HEAD -> main) feat: add persistent STT runtime and device selection
95f1d9a feat: add TypeScript Python SDKs and CLI
7efa4a0 feat: stabilize public API v1
b49e15b feat: finish realtime audio runtime
cc5be67 feat: add multi-turn conversation runtime
bf4e746 feat: add interruption recovery and speculative generation
dcf25aa auto-commit
42bb99d feat: complete initial realtime voice pipeline
78f1853 Implement realtime PCM playback and playback timeline
c4dae7d feat: add realtime audio streaming over WebSocket
80db29e feat: add HTTP speech API and AquesTalk voice registry
e2a2008 feat: add engine core and AquesTalk TTS provider
```

## Working-tree inventory at handoff

After Step 10-C, vendor staged deletions are gone because the rewritten base does not track them; local files remain intact.

```text
 M .gitignore
 M README.md
 M cmd/engine/main.go
 M docs/api.md
 M docs/cli.md
 M docs/realtime-protocol.md
 M docs/stt-performance.md
 M docs/stt-runtime-finishing.md
 M docs/stt-runtime.md
 M docs/typescript-sdk.md
 M examples/typescript/browser-voice/index.html
 M examples/typescript/browser-voice/main.js
 M internal/providers/stt/whispercpp/runtime.go
 M internal/providers/stt/whispercpp/runtime_test.go
?? .env.example
?? .gitattributes
?? .github/
?? CHANGELOG.md
?? CONTRIBUTING.md
?? LICENSE
?? README.ja.md
?? SECURITY.md
?? cmd/engine/main_test.go
?? docs/architecture.ja.md
?? docs/architecture.md
?? docs/configuration.ja.md
?? docs/configuration.md
?? docs/performance.ja.md
?? docs/performance.md
?? docs/providers.ja.md
?? docs/providers.md
?? docs/quickstart.ja.md
?? docs/quickstart.md
?? docs/realtime-protocol.ja.md
?? docs/release-quality.md
?? docs/troubleshooting.ja.md
?? docs/troubleshooting.md
?? examples/typescript/browser-voice/README.md
?? examples/typescript/browser-voice/history.mjs
?? examples/typescript/browser-voice/history.test.mjs
?? examples/typescript/browser-voice/style.css
?? internal/transport/http/lifecycle_test.go
?? scripts/release_check.py
?? scripts/test_release_check.py
```
