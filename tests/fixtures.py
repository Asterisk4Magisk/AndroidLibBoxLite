from __future__ import annotations


def archive(name: str, sha1: str | None = None) -> dict[str, object]:
    return {
        "url": f"https://go.dev/dl/{name}",
        "size": 123,
        "sha256": "a" * 64,
        "sha1": sha1,
    }


def release_lock_dict() -> dict[str, object]:
    return {
        "schema": 1,
        "source": {
            "repository": "SagerNet/sing-box",
            "tag": "v1.14.0-alpha.47",
            "commit": "37b4386bddb143e0780435c467cd2c5f1250a4ff",
            "commitTime": 1784505600,
            "archive": archive("sing-box.zip"),
        },
        "toolchain": {
            "go": {
                "version": "go1.26.5",
                "archive": archive("go.tar.gz"),
            },
            "gomobile": {
                "module": "github.com/sagernet/gomobile",
                "version": "v0.1.13",
                "sum": "h1:foTOGKJetah9VwaJl1XJx5TswIAVg8NfYmHOhrOc95I=",
            },
            "jdk": {
                "vendor": "Eclipse Temurin",
                "version": "25.0.3+9",
                "archive": archive("jdk.tar.gz"),
            },
            "android": {
                "repository": "https://dl.google.com/android/repository/repository2-3.xml",
                "commandLineTools": {
                    "package": "cmdline-tools;22.0",
                    "archive": archive("commandlinetools.zip", "b" * 40),
                },
                "platform": {
                    "package": "platforms;android-23",
                    "archive": archive("platform.zip", "c" * 40),
                },
                "buildTools": {
                    "package": "build-tools;37.0.0",
                    "archive": archive("build-tools.zip", "d" * 40),
                },
                "ndk": {
                    "package": "ndk;29.0.14206865",
                    "archive": archive("ndk.zip", "e" * 40),
                },
            },
        },
        "libbox": {
            "androidApi": 23,
            "abis": ["arm64-v8a", "armeabi-v7a", "x86", "x86_64"],
            "tags": [
                "with_gvisor",
                "with_quic",
                "with_dhcp",
                "with_wireguard",
                "with_utls",
                "with_clash_api",
                "with_tailscale",
                "with_naive_outbound",
                "with_openvpn",
                "with_openconnect",
                "badlinkname",
                "tfogo_checklinkname0",
                "ts_omit_logtail",
                "ts_omit_ssh",
                "ts_omit_drive",
                "ts_omit_taildrop",
                "ts_omit_webclient",
                "ts_omit_doctor",
                "ts_omit_capture",
                "ts_omit_kube",
                "ts_omit_aws",
                "ts_omit_synology",
                "ts_omit_bird",
            ],
            "ldflags": "-X github.com/sagernet/sing-box/constant.Version=1.14.0-alpha.47 -X internal/godebug.defaultGODEBUG=multipathtcp=0 -checklinkname=0 -s -w -buildid=",
        },
        "workflowCommit": "f" * 40,
    }
