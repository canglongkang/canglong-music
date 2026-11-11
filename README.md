# canglong-music 引擎发布仓

苍龙音乐管理播放器的安卓下载引擎（libmusdl.so）自动构建与发布仓。

- 上游项目：`guohuiyuan/go-music-dl`（第三方，接口常变，必须随新版更新）
- 本仓差异：在 `cmd/music-dl/` 注入 `compat_server.go` 兼容层
  （为苍龙 App 提供与桌面 music-dl.exe 一致的 search/download HTTP 契约，
  并注入公共 DNS 解析——安卓没有 /etc/resolv.conf）
- 构建参数：`CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -trimpath -ldflags="-w -s"`
  （**必须 GOOS=android**：只有 android 构建会加载 /system/etc/security/cacerts，
  GOOS=linux 构建在安卓上 HTTPS 全挂）

## 产物

每天自动检查上游 release；有新版本则构建并发 GitHub Release：

- `libmusdl_arm64-v8a.so` —— 引擎本体（放回 App 的 `jniLibs/arm64-v8a/libmusdl.so`）
- `manifest.json` —— 版本 / sha256 / size，供 App 校验

## 使用

1. GitHub Release 页下载 `manifest.json` 与 `.so`
2. App 内「外观 → 下载引擎」检查更新（自动比对 manifest 的 version 与 sha256）
