[English](README.md) | 简体中文

# AndroidLibBoxLite

AndroidLibBoxLite 为 Asterisk 系列应用提供经过审核的 Android `libbox.aar`。仓库跟踪 [SagerNet/sing-box](https://github.com/SagerNet/sing-box) 官方 SemVer tag，为每个版本冻结全部构建输入，在 Linux 上只构建一次，完成产物验证后发布不可变 GitHub Release。

首个基线为 sing-box `v1.14.0-alpha.47`，对应 commit `37b4386bddb143e0780435c467cd2c5f1250a4ff`。仓库不会补发更早的历史版本。

## 发布资产

每个成功 release 固定包含：

- `libbox.aar`
- `libbox-sources.jar`
- `build-manifest.json`
- `SHA256SUMS`

release tag 与上游 sing-box tag 完全相同。alpha、beta 和 rc 作为 GitHub prerelease 发布，stable 作为普通 release 发布。

## 更新策略

每日发现工作流会查找基线及之后的全部规范上游新 tag。首次发现时解析当时最新的稳定工具链，先提交 `locks/<tag>.json`，再派发发布构建。失败重试只读取已经提交的锁文件，不会再次解析 `latest`。

Android API 23 是 libbox 构建契约中的固定值。Go、SagerNet gomobile/gobind、Eclipse Temurin JDK、Android command-line tools、build-tools 和 NDK 分别按上游 tag 冻结。

## 验证边界

发布流程会验证源码 commit 与归档哈希、全部工具链归档、四个固定 Android ABI、ELF machine、必要 Java 类与源码、Go build settings、注入的 sing-box 版本、AAR 确定性规范化和 release 校验和。

## 开发

Python 工具要求 Python 3.12 或更高版本，不包含第三方运行时依赖。

```bash
python -m unittest discover -s tests -v
python scripts/discover_upstream.py --help
python scripts/resolve_toolchain.py --help
python scripts/build_libbox.py --help
```

真实提供端构建只支持 Linux。下载缓存在 `.toolchains/downloads`：已经通过大小与哈希验证的文件会跨构建复用；截断或哈希不匹配的缓存会被丢弃并重新下载。

## 许可

[LGPL-3.0](LICENSE)

## 致谢

- [SagerNet/sing-box](https://github.com/SagerNet/sing-box)
- [SagerNet/gomobile](https://github.com/SagerNet/gomobile)
