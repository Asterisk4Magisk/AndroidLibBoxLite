[English](README.md) | 简体中文

# AndroidLibBoxLite

AndroidLibBoxLite 为 Asterisk 系列应用提供经过审核的 Android `libbox.aar`。仓库跟踪 [sing-box](https://github.com/reF1nd/sing-box) SemVer release tag，为每个版本冻结全部构建输入，在 Linux 上只构建一次，完成产物验证后发布不可变 GitHub Release。

当前基线为 sing-box `v1.14.0-beta.5-reF1nd`，对应 commit `b4de7f7013014b87cff5ae2c21952d9d9127c5d2`。仓库不会补发更早的历史版本。

## 发布资产

每个成功 release 固定包含：

- `libbox.aar`
- `libbox-sources.jar`
- `build-manifest.json`
- `SHA256SUMS`

release tag 与对应的 sing-box 源码 tag 完全相同。alpha、beta 和 rc 作为 GitHub prerelease 发布，stable 作为普通 release 发布。

## 更新策略

每日发现工作流会查找来源仓库中基线及之后的全部规范新 tag。首次发现时解析当时最新的稳定工具链，先提交 `locks/<tag>.json`，再派发发布构建。失败重试只读取已经提交的锁文件，不会再次解析 `latest`。

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

- [sing-box](https://github.com/reF1nd/sing-box)
- [SagerNet/gomobile](https://github.com/SagerNet/gomobile)
