English | [简体中文](README_zh_CN.md)

# AndroidLibBoxLite

AndroidLibBoxLite provides reviewed Android `libbox.aar` releases for the Asterisk app family. It tracks official [SagerNet/sing-box](https://github.com/SagerNet/sing-box) SemVer tags, freezes every build input in a per-release lock, builds once on Linux, verifies the result, and publishes immutable GitHub Release assets.

The baseline is sing-box `v1.14.0-alpha.47` at commit `37b4386bddb143e0780435c467cd2c5f1250a4ff`. Older releases are intentionally not backfilled.

## Release assets

Every successful release contains exactly:

- `libbox.aar`
- `libbox-sources.jar`
- `build-manifest.json`
- `SHA256SUMS`

The release tag is identical to the upstream sing-box tag. Alpha, beta, and RC tags are GitHub prereleases; stable tags are normal releases.

## Update policy

The daily discovery workflow finds every new canonical upstream tag at or after the baseline. It resolves the latest stable toolchain available at discovery time, commits `locks/<tag>.json`, and dispatches the release build. Retries consume the committed lock and never resolve `latest` again.

Android API 23 is fixed by the libbox contract. Go, SagerNet gomobile/gobind, Eclipse Temurin JDK, Android command-line tools, build-tools, and NDK are pinned independently for each upstream tag.

## Verification boundary

The release pipeline verifies the source commit and archive hash, every toolchain archive, the exact four Android ABIs, ELF machine values, required Java classes and sources, Go build settings, injected sing-box version, deterministic AAR normalization, and release checksums.

## Development

Python tooling requires Python 3.12 or later and has no third-party runtime dependencies.

```bash
python -m unittest discover -s tests -v
python scripts/discover_upstream.py --help
python scripts/resolve_toolchain.py --help
python scripts/build_libbox.py --help
```

Real provider builds require Linux. Downloads are stored in `.toolchains/downloads`; verified entries are reused across runs, while truncated or hash-mismatched entries are discarded and downloaded again.

## License

[LGPL-3.0](LICENSE)

## Credits

- [SagerNet/sing-box](https://github.com/SagerNet/sing-box)
- [SagerNet/gomobile](https://github.com/SagerNet/gomobile)
