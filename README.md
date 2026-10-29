# Codex CLI Headless Clipboard Bridge

Windows 当前登录用户运行的 Go 程序。按 Alt+V 后，自动识别 Windows Terminal 当前焦点窗格的 Codex TUI，读取本机图片、通过现有 SSH 连接写入 Debian Xvfb 的 CLIPBOARD，验证 MIME 与 SHA-256，等物理按键释放后向原来的窗格重放 Alt+V。默认不要求设置任何窗口或标签页标题。没有 Linux 守护进程、HTTP 服务、SCP、图片临时文件或 AutoHotkey。

当前提供控制台诊断版。真实物理快捷键验收完成前，不把无控制台版本标为稳定版。

## 下载编译文件

从本仓库 [Releases](https://github.com/fuzqing/codex-cli-headless-clipboard-bridge/releases) 下载 `clipbridge-windows-amd64.zip`，解压后将 `config.example.json` 复制为 `config.json` 并填写 SSH 配置，然后运行：

```powershell
.\clipbridge.exe -config .\config.json -metrics .\windows-live.jsonl
```

ZIP 包含 EXE、配置示例、说明、验收报告和第三方许可；Release 同时提供单独的 EXE 与 `SHA256SUMS.txt`。源码中的实际配置、私钥、日志和测试图片不会进入自动发布包。下面使用 `dist/clipbridge.exe` 的命令适用于源码构建目录。

## 开始使用

1. 将 `config.example.json` 复制为 `config.json`。填写你的 SSH 主机、用户名、私钥与 known_hosts 路径（示例使用占位值）。程序只读私钥和 known_hosts，不修改它们。
2. 在 Windows Terminal 内通过 SSH 运行 Codex，Codex 的环境为 `DISPLAY=:99`。保持焦点在 Codex 的输入区，使用默认 `window.mode: codex_tui`；无需设置标题或专用 Terminal profile。
3. 运行：

```powershell
.\dist\clipbridge.exe -config .\config.json -metrics .\windows-live.jsonl
```

4. 用原有 Alt+A 截图工具将图片放入剪贴板，切回目标 Codex 标签页，按下并释放 Alt+V。失败时提示音加日志，成功日志为 `paste replayed after PNG verification`；该日志表示重放按键，最终 `[Image #1]` 要在 Codex 中观察。
5. 控制台 Ctrl+C 退出。不要同时运行旧 AHK Alt+V 绑定或另一份 bridge。

已有 known_hosts 必须包含经过独立确认的服务器公钥；未知或变化的公钥会被拒绝。不要使用 `StrictHostKeyChecking=no` 或自动接受扫描所得公钥。普通用户运行即可，Terminal 和 bridge 应处在相同权限级别。

`ssh -Y` 不属于 bridge 的连接要求。本程序直接使用 Debian 的 `:99`，不进行 X11 forwarding。Debian 上启动 Codex 的 shell 同样要使用 `DISPLAY=:99`，而不是 SSH 自动设置的 `localhost:10.0`。Xvfb 必须已运行；如启用 Xauthority，SSH 登录用户应已有访问权限。本程序不修改 X11 安全设置。

## 无标题的 Codex TUI 识别

默认配置：

```json
{
  "mode": "codex_tui",
  "process": "WindowsTerminal.exe",
  "class": "CASCADIA_HOSTING_WINDOW_CLASS"
}
```

以上是 `window` 对象中的字段。前台进程和窗口类通过后，工作线程通过系统 UI Automation 读取**当前焦点窗格的可见文本**，不读取历史 document range。仅检测底部相邻的 `›` 输入提示符与 Codex 快捷键/上下文页脚，支持下方另有一行状态栏；启动横幅、标题或正文出现 Codex 字样都不足以匹配。

使用 UIA Runtime ID 绑定焦点窗格，并确认其祖先 HWND 属于原前台窗口。发送前重新识别，必须仍是同一窗格且具有 Codex 输入界面。标题在此模式下不参与匹配或取消，Codex 自动更改标题不影响粘贴。

UIA 使用 Windows 自带 COM 接口，运行在独立 MTA 工作线程，Hook 回调不调用 UIA。没有轮询剪贴板或持续读取屏幕；只在 Alt+V 事务内进行初次识别和发送前核对。不保存、上传或记录终端文本，诊断只记录识别特征布尔值与耗时 `tui_detect_ms`。

在其他 Windows 应用中 Alt+V 直接放行。在 Windows Terminal 的非 Codex 窗格中，会等待快捷键释放、复核窗格，然后重放原生 Alt+V，不读取 Windows 剪贴板，不上传图片；此路径会增加识别与重放延迟。UIA 超时/错误或途中切换焦点则取消，不重放。

前台切换、焦点变化、鼠标点击/滚轮或额外物理键盘输入都会取消正在传输的操作，切走再切回也不能恢复旧操作。多标签页/窗格的 UIA 身份检查已实现，真实切换仍需验收。

这是 UI 特征识别，不是远端进程身份认证。通过 SSH 无法在 Windows 直接取得当前 TTY 的前台进程名；足够相似的程序/输出可模仿这些特征。自定义/本地化页脚、窄窗口隐藏提示、审批/菜单覆盖输入区等情况可能不能识别。所有识别出的 Codex 都使用配置的 SSH 主机与 DISPLAY，不能据此自动推断远端主机。

旧 `title_regex` 配置仍可使用，默认解释为 `mode: title`，也可显式选择 title 模式；该兼容模式仍要求唯一标题。自动模式中移除 `title_regex`，防止两种策略混用。现场旧配置和 EXE 备份为 `config.title-backup.json` 与 `dist/clipbridge-title.exe`。

参考：[Windows Terminal 的可见文本与 UIA Provider](https://github.com/microsoft/terminal/blob/main/src/types/ScreenInfoUiaProviderBase.cpp)。

## 为什么没有首选 RegisterHotKey

已先评估动态 RegisterHotKey + 前台事件：它仍注册系统级热键。从前台切换到注销完成之间存在窗口，会吞掉其他应用 Alt+V；注销期间又有漏接窗口。检查 WM_HOTKEY 时已经无法撤销原始输入。本实现使用允许的备选 `WH_KEYBOARD_LL`，专用消息线程内按键时检查前台 Windows Terminal；是否上传由工作线程的 TUI 识别决定。网络、剪贴板与 UIA 不在 Hook 回调内运行。

Hook 使用按下/释放状态抑制自动重复，busy 状态不排队第二次上传，忽略注入键防止 SendInput 重入。另一个低级鼠标 Hook 只用于取消传输，不吞鼠标事件。退出时解除全部 Hook；进程异常退出时系统也会清除它们。

参考：[RegisterHotKey](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-registerhotkey)、[LowLevelKeyboardProc](https://learn.microsoft.com/en-us/windows/win32/winmsg/lowlevelkeyboardproc)、[SendInput](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput)。

## 图片和所有权

- 仅收到合格的物理 Alt+V 才读取 Windows 剪贴板。没有剪贴板观察器或自动上传。
- 优先读取浏览器常用的 `PNG` 格式，再尝试 CF_DIBV5 / CF_DIB。支持未压缩 1/4/8/16/24/32-bit DIB、BI_BITFIELDS、显式 alpha mask。PNG 与带显式 alpha 的 DIB 保留透明通道；BI_RGB 中未定义的 alpha 字节按不透明处理。
- RLE、嵌入色彩配置、CF_BITMAP-only、网页 URL、HTML-only、文件列表不支持。多数截图工具和浏览器会提供 PNG 或 DIB；实际工具仍需验收。色彩配置不作颜色管理。
- 解码前限制像素数，剪贴板内存复制和最终 PNG 分别有限额。剪贴板忙时在 600ms 内重试；不清空、不覆盖本机剪贴板。
- `xclip -quiet -loops 0 -selection clipboard -target image/png` 前台拥有 Selection。无默认后台 fork，也不使用有限读取次数：TARGETS 和验证读取本身就会消耗请求。
- 每次上传在复用的 SSH 连接上新建 Session（SSH Session 不能重复 exec）。同一时刻只保留当前 owner Session；替换前关闭旧 stdin，等待远端 trap kill+wait 回收。
- stdin 协议为 N 字节 PNG，随后每 3 秒一行 keepalive。Bash coprocess 为 xclip 提供 EOF 结束图片输入，同时 SSH stdin 留作租约。15 秒无续租、EOF、HUP/TERM 均回收 owner；上传 head 也受有界 timeout 保护，避免传到一半卡死。
- `UPLOADED` 仅表明字节已交给远端 pipe。程序另外读取 TARGETS 并用 `xclip -out -target image/png | sha256sum` 校验，采用 pipefail、远端读取超时及总截止时间内的 20ms 重试。
- 按键释放后再次校验，最终在消息线程重查窗口/焦点与按键状态，再批量 SendInput。失败不重放，也不自动重试过期请求。连接恢复后必须重新按快捷键。

## 配置

完整示例见 `config.example.json`。额外可设 `ssh.passphrase_env` 为已加密私钥口令所在的环境变量名；配置和日志里不放口令。支持 `~/` 和环境变量展开，SSH 相对路径按配置目录解析。默认超时 12 秒，最大 PNG 20 MiB、1600 万像素。日志级别 debug/info/warn/error；`-log-file bridge.log` 输出 JSON 日志。

没有 ssh-agent、硬件密钥、交互式口令提示、ProxyJump 或自动读取 ssh_config。支持普通文件式 SSH 私钥和 known_hosts（包括其主机名/端口匹配规则）。

```powershell
# 只读依赖诊断，不读取 Windows 剪贴板
.\dist\clipbridge.exe -config config.json -probe

# 内存生成 100 张不同图；替换远端剪贴板，不发送按键
.\dist\clipbridge.exe -config config.json -selftest 100 -metrics remote-100.jsonl
```

## 构建与测试

Windows amd64，Go 1.25+。仅两个直接依赖 x/crypto 和 x/sys，均使用 go.sum 校验。

```powershell
.\build.ps1
go vet ./...
go test -v ./...
go test -run '^$' -fuzz FuzzDIB -fuzztime 10s

# 明确 opt-in 的真实 Debian 租约/断线/错误哈希测试，会替换远端剪贴板
$env:CLIPBRIDGE_REMOTE_CONFIG = (Resolve-Path config.json).Path
go test -v -run '^TestLiveRemoteLifecycle$'
```

脚本将 Go 缓存放在仓库 `.cache/`，避免受限环境写用户缓存失败。手工运行 go 命令时可沿用该设置。Linux 交叉编译：`GOOS=windows GOARCH=amd64 go build -o clipbridge.exe .`；Windows 原生测试仍应在实机执行。

原生剪贴板测试使用子进程内私有、非交互 Window Station，不接触用户剪贴板；环境不允许创建时明确 SKIP。测试覆盖虚拟 SSH 服务器上的真实握手、连接复用、替换、超时、错误哈希和主机公钥错误。真实 Debian 测试另外检查心跳保持超过租约、停止续租自动回收及断线清理。

稳定验收后可用 `build.ps1 -Background` 生成无控制台版，并配 `-log-file`。当前不安装服务、自启项或计划任务。

## GitHub Actions 与发布

- `Windows build`：`main` 推送、Pull Request 或手动运行时，在 Windows runner 上检查格式、执行 `go vet` 和测试，构建 Windows amd64 控制台 EXE，再上传名为 `clipbridge-windows-amd64` 的 Actions artifact（保留 14 天）。
- `Release`：推送 `v*` 版本标签时重新测试和构建，并发布 EXE、ZIP 与 SHA-256 校验文件。标签包含预发布后缀（如 `v0.1.0-alpha.1`）时自动标为 prerelease。

```powershell
git tag v0.1.0-alpha.1
git push origin v0.1.0-alpha.1
```

同一版本标签只发布一次；如果该 Release 已存在，任务会失败，避免覆盖已发布文件。修复版本使用新的标签。初始版本仍属诊断/预发布阶段，不代表剩余实机验收已完成。

工作流只使用 GitHub 内置 `GITHUB_TOKEN`，不需要上传 SSH 私钥或配置 Secrets。构建任务为只读仓库权限，Release 任务额外使用 `contents: write`；Actions 固定到已核验的提交 SHA。CI 无法代替真实 Windows Terminal → SSH → Codex 物理快捷键验收。

本地生成相同的发布格式：

```powershell
.\scripts\package.ps1
```

输出位于 `dist/release/`，不会覆盖正在运行的 `dist/clipbridge.exe`。

## 安全边界和已知限制

1. X11 剪贴板对有权访问该 DISPLAY 的其他客户端可见。此程序把图片留在远端内存直至替换、程序退出或租约过期；不写图片文件，但不能阻止 OS swap、截图工具、X11 客户端或 Codex 自己的持久化行为。
2. 自动模式用 HWND/PID 加 UIA Runtime ID 区分焦点窗格，但不能认证远端进程、主机或工作目录。输入期间出现用户交互按保守策略取消；无可观察事件的程序状态变化仍有竞争窗口。旧 title 模式仍需唯一标题和单窗格。
3. SendInput 与前台切换、远端读剪贴板均不是跨系统原子操作。末次检查后仍有极短竞争窗口；同一 DISPLAY 不应有并行剪贴板写入者。无法在不修改 Terminal/Codex、没有接收确认协议的条件下证明 Codex 最终消费的是哪张图片。
4. 低级 Hook 若被系统因超时移除，Windows 不提供移除通知。回调只做小量 Win32 查询和非阻塞投递，仍需实机长期验收；应用没有自动降级到全局热键。
5. 已捕获一次 Alt+V 后，对应 V 释放/重复也会被吞掉，直到该次按键结束，即使用户在按住 V 时切换应用。其他应用的新 Alt+V 不拦截。AltGr/Ctrl/Shift/Win 组合不作为上传快捷键。
6. 发送前按键需全部释放；其间额外输入取消操作。远端 xclip 可读不等于 Codex 必定显示 `[Image #1]`，还取决于 Terminal 原生 Alt+V 转发及 Codex 状态。注入的按键不会触发上传。
7. 普通网络断开/进程结束会触发 EOF/HUP 或 15 秒租约回收；若远端 shell 被 SIGKILL 或整个机器冻结，trap 无法执行，这超出用户态租约保证。

## 验收和延迟

实测记录及待验收项见 `TEST_REPORT.md`。JSONL 记录剪贴板读取、PNG 编码、SSH 连接/传输、xclip pipe acknowledgment、哈希验证、按键释放、SendInput 与总耗时。不记录图片、私钥、口令或剪贴板文本。`xclip_write_ms` 是传输结束到远端确认的客户端时间，包含网络/调度，并非 X11 内部纯耗时。

真正端到端延迟以物理触发到 SendInput 返回计；Codex 渲染延迟需额外人工观察。`-selftest` 的远端链路数字不能作为完整端到端结果。使用 `summarize.ps1 -Path windows-live.jsonl` 汇总实际快捷键日志。

旧标题模式的现场验收已由用户确认连续 20 张不同图片全部正确，保存的 7 条成功计时 p50 354 ms，范围 250–513 ms。此后按用户要求改为默认无标题 TUI 识别；旧模式通过的验收不自动等同于新模式已通过。新模式的实机结果及剩余验收见 `TEST_REPORT.md`。成功没有提示音。

当前诊断进程的 PID 存在 `bridge.pid`。如需停止本次隐藏启动的诊断进程，可先确认该 PID 的进程名为 clipbridge，再结束它；Windows 会卸载其 Hook，远端 owner 通过连接断开/租约回收。常规控制台启动时优先 Ctrl+C 正常退出。不要仅因进程名相同而批量停止其他实例。
