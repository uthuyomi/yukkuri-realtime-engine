# Security policy

## Reporting

Do not post exploit details, credentials, audio recordings or proprietary files in a public issue. If this repository's GitHub Security tab offers **Report a vulnerability**, use that private reporting route. Availability has not been verified; no private security email or guaranteed response SLA is established. If private reporting is unavailable, open a minimal issue asking the maintainer to establish a private contact channel, without sensitive details. Do not assume disclosure is authorized by a lack of response.

v0.1.0 is an early release target; there is no published long-term support policy. Reproduce against the current source and state the affected commit and safe diagnostic codes.

## Trust and deployment assumptions

The supplied engine binds 127.0.0.1:8765 and provides **no authentication or TLS termination**. Smart Turn binds loopback too. Do not expose either directly to untrusted networks. Native clients without Origin are allowed; same-origin and HTTP(S) loopback browser origins are allowed by default. Additional exact origins are configured explicitly. Origin/CORS is a browser policy, not authentication, DNS-rebinding protection or protection against malicious local processes. A remote deployment needs a separately designed authenticated gateway and network policy.

The quickstart's Python HTTP server is development-only and serves the checkout, including local .env and runtime assets to clients that can reach it. Bind it to loopback; do not use it as production hosting. Browser microphone permission is separate from server access. Browser VAD loads pinned CDN resources unless explicitly self-hosted.

Input text, transcript and playback history are application data. A configured external LLM receives conversation content. Review provider terms and data handling for your use. Public errors sanitize raw provider bodies; ordinary server telemetry records identifiers/counts rather than conversation content. Local TTS startup errors may expose filesystem paths; sidecar exceptions may produce local tracebacks. Redact diagnostics before sharing. The .env load diagnostic deliberately omits parser details because they can quote a secret-bearing line.

## Limits and residual risks

HTTP/WS payload, turn duration, transcript, conversation, audio credit, queue and worker limits are enforced in source; see [API](docs/api.md). Metadata/binary pairing and source frames are validated. Shutdown/cancellation propagates contexts, with bounded waits and explicit incomplete-cleanup reporting. STT children are executed directly (not through a shell); Windows Job Objects constrain worker lifetime. Only trusted locally installed provider executables/DLLs should be used.

Bounds are not per-user quotas. The service has no application request-rate limiter; HTTP speech has no global concurrency admission like the WebSocket session limit. Ordinary LLM streams lack a fixed server-wide completion deadline. Synchronous native TTS cannot be forcibly interrupted, and native library correctness/concurrency is outside Go race analysis. This release is intended for trusted local development, not hostile multi-tenant use.

Current Windows `go vet` flags the native return-pointer boundary. Its narrowly scoped CI exception is recorded in [CONTRIBUTING](CONTRIBUTING.md); it is not proof of native memory safety.

## Asset and secret hygiene

Never distribute AQUEST binaries, dictionaries, SDK assets or license keys from this project. Never package .env, downloaded models, private recordings, runtime directories or virtualenvs. The release guard checks source candidates and optional committed trees; it reports secret matches by filename without printing values. Signature scans are incomplete by nature.

Step 10-C removed AqKanji2Koe assets from all local reachable refs, preserving local files. Step 10-D replaced advertised GitHub main and verified a fresh clone has clean reachable history. This does not certify backend/cache deletion or cleanup of other clones. Restricted objects also remain in the private backup and may remain unreachable in the local object database; neither is a publication source. See [audit report](docs/release-quality.md).

The project's [MIT License](LICENSE) covers original project code and project-authored documentation unless otherwise noted, not third-party dependencies or assets. It does not grant AQUEST usage or redistribution rights; obtain software and applicable rights separately from AQUEST. Release/publication and any provider-side retention follow-up remain separate decisions; see the [release report](docs/release-quality.md).
