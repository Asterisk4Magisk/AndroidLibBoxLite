# AndroidLibBoxLite

Android `libbox.aar` built from [reF1nd/sing-box](https://github.com/reF1nd/sing-box), with gomobile Java bindings and a CLI sharing one Go native library.

## Contents

Each Android ABI (`armeabi-v7a`, `arm64-v8a`, `x86`, `x86_64`) contains:

- `libbox.so`: sing-box, the Go runtime, JNI bindings, and the CLI entry point.
- `libsing-box.so`: a small executable launcher that loads the sibling `libbox.so`.

The Java package remains `io.nekohasekai.libbox`. The upstream libbox API and SagerNet gomobile binding are retained. `libbox-sources.jar` contains the generated Java sources.

## Build

Install Go (version in `go.mod`), Python 3.10+, a JDK, Android SDK, and NDK. Set `ANDROID_HOME`, `ANDROID_NDK_HOME`, and `JAVA_HOME`.

```sh
python3 scripts/build_shared.py
# Optional: build one ABI and retain intermediate files and logs.
python3 scripts/build_shared.py --arch arm64 --work-dir work --output libbox.aar
```

On Windows, use `python` instead of `python3`. Build tags are maintained locally in `build_tags.txt`. Core updates do not change this list. Shared linker flags follow the pinned upstream `release/LDFLAGS`. JNI and CLI use the same tag set. The minimum Android API remains 23. Sources are copied to a temporary workspace, preserving upstream dependency replacements. The checkout and Go module cache are not patched. No Gradle project is needed.

Tailscale remains enabled with the same 11 `ts_omit_*` tags as the previous libbox build. These exclusions now apply to both JNI and CLI, including Tailscale SSH, Taildrop, and Drive. They do not disable the separate SSH outbound.

The 22 build tags retain QUIC, DHCP DNS, WireGuard, uTLS, Tailscale, OpenVPN, OpenConnect, Naive outbound, and eBPF, together with `badlinkname`, `tfogo_checklinkname0`, and the 11 Tailscale exclusions. The build omits `with_gvisor`, ACME, Clash HTTP API, CCM, OCM, Cloudflare Tunnel, and USB/IP. The upstream Command API and API service remain available. No deprecated TUN stack parameter is introduced. Custom configurations requiring omitted components are unsupported.

## CLI

Execute `libsing-box.so` directly from the application's native library directory, preserving upstream arguments:

```sh
/path/to/nativeLibraryDir/libsing-box.so version
/path/to/nativeLibraryDir/libsing-box.so run -D /path/to/work -c config.json
```

The launcher resolves its sibling through `/proc/self/exe`. It does not need copying into the working directory. `ANDROID_BOX_LIBRARY` optionally specifies an absolute path to `libbox.so`; the exported ABI is `AndroidBoxCLI_v1` and is for standalone processes only. JNI callers continue to use the generated Java API. Native extraction must be enabled by the consuming app so both files exist on disk.

CLI and JNI share the native file, not a running process. The launcher imports the native environment and restores standard output/error before running the upstream command. `GOGC`, `GOMEMLIMIT`, and `GOMAXPROCS` are restored explicitly for the shared Go runtime. Custom standalone CLI support remains the consuming application's responsibility.

## Updates and releases

- `go.mod` pins the reF1nd source tag and SagerNet gomobile version; `go.sum` verifies downloads.
- `tidy.yml` checks upstream daily and on demand. It selects the highest semantic version with the `-reF1nd` suffix, including stable and preview tags. Alpha/beta/RC ordering is semantic; stable wins over previews of the same version. Synced original sing-box tags are excluded.
- An update commits the module pins, creates a matching tag, and explicitly dispatches `main.yml` at that tag. Unpublished tags are retried; versions never move backwards.
- `main.yml` builds all four ABIs on pushes, pull requests, and manual runs. Without `release_tag` it uploads workflow artifacts. With a matching tag (optionally suffixed `-android.N`), it creates/verifies the tag after a successful build and publishes the AAR and sources. Preview cores are marked as prereleases. Already published assets are preserved.

To publish this rewrite against an already released core tag, use a new tag such as `v1.15.0-alpha.6-reF1nd-android.1` rather than replacing existing release assets.

## Credits and license

- [sing-box / reF1nd](https://github.com/reF1nd/sing-box)
- [SagerNet/gomobile](https://github.com/SagerNet/gomobile)

Original contributions in this repository are licensed under [LGPL-3.0](LICENSE). Upstream and third-party code retain their respective licenses.
