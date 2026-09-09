# 麒麟 V10 ARM64 离线运行包

GitHub Actions 的 `Build Linux ARM64 Kylin AppImage and DEB` 工作流生成
`uniterm-appimage-arm64` 附件。下载解压后，将 `.AppImage` 复制到内网机器。

要求：ARM64（aarch64）、glibc 2.31 或更新版本，以及正常登录的 Linux 桌面会话。
包内包含 GTK 3、WebKitGTK 4.0 / WebKit 2.38、libsoup 2、相关运行库、字体和软件渲染驱动。
无需安装系统 WebKit，也无需联网下载运行依赖。glibc、内核和桌面显示服务由系统提供。

```bash
chmod +x ./uniterm-*.AppImage
./uniterm-*.AppImage
```

如果系统缺少 FUSE，使用无需安装 FUSE 的启动方式：

```bash
./uniterm-*.AppImage --appimage-extract-and-run
```

也可以在一个空目录中解包一次，以后运行解包目录内的 AppRun：

```bash
./uniterm-*.AppImage --appimage-extract
./squashfs-root/AppRun
```

请以当前桌面用户运行，不要使用 sudo。初始化时可以选择“主密码加密”；此模式不要求
系统钥匙串可用。选择“系统钥匙串加密”仍需要桌面已配置并解锁 Secret Service 钥匙串。

启动日志：

```bash
./uniterm-*.AppImage --appimage-extract-and-run > uniterm-startup.log 2>&1
```

默认使用包内的软件渲染驱动。确认宿主显卡驱动兼容后，可设置
`UNITERM_HARDWARE_ACCELERATION=1` 尝试硬件加速。
启动脚本不会默认关闭 WebKit 沙箱。

CI 检查所有包内 ELF 的架构和 GLIBC 符号版本，并在未安装 GTK/WebKit 的 Ubuntu 20.04
ARM64 容器中，从含空格及中文的目录启动 AppImage，验证真实 Wails 网页到 Go 后端的
中文和 600 KB 请求往返。CI 中的 root 容器测试单独关闭沙箱；这不代表已覆盖所有麒麟
桌面、内核或显卡组合，最终仍需目标机器验收。

附件同时提供 AppImage-SHA256SUMS 和 runtime-manifest.json。
各依赖的版权及许可证说明位于包内 usr/share/licenses。
