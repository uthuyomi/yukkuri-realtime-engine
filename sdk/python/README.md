# yukkuri-realtime

Async Python 3.11+ SDK and `yukkuri` CLI for Public API v1. From a repository checkout:

```sh
python -m pip install -e ./sdk/python
yukkuri health
yukkuri speak "こんにちは" --output hello.wav
```

See [Python SDK](../../docs/python-sdk.md) and [CLI](../../docs/cli.md). No ML dependency, automatic reconnect, implicit speaker playback or package publication.
