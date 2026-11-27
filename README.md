# Publisher

Publisher 是一个 Windows 桌面发布工具。视频和 Markdown 图文统一入库，选好创作平台后，可批量发布或分别为视频和图文安排定时发布。

## 下载

在 [Windows 下载页](https://cloudreve.haowu.fun/s/19I6) 获取 x64 安装包 `Publisher-*-setup.exe` 或免安装版 `Publisher-*-portable.zip`。免安装版解压后运行 `Publisher.exe`。应用需要 WebView2 Runtime 和 Chrome 或 Edge。

## 能做什么

- **整理素材** 拖入视频或 `.md` 文件，也可从指定文件夹同步。可配置标题、简介、标签、封面和配图的读取规则，并在素材库内调整。
- **管理平台账号** 在「账号」页登录或检查登录状态。应用用独立的浏览器配置打开创作后台，后续任务沿用登录状态。
- **检查后发布** 先由应用填写表单，再在浏览器里核对内容并提交。也可选择全自动提交，由任务页提示需要手动确认的步骤。
- **定时分发** 分别设定视频和图文的时段、批次大小和目标平台，可拖动待发布素材调整顺序。开启自动同步后，到点先读取同步文件夹的新素材，仍在修改的文件留到下次。窗口关闭到托盘后，定时任务仍可运行。
- **追踪任务** 查看各平台的发布记录、待确认事项和失败截图，并在任务页重试失败任务。

## 发布平台

| 素材类型 | 已接入发布的平台 |
| --- | --- |
| 视频和图文 | 哔哩哔哩、抖音、快手、小红书、视频号、百家号、微博、大鱼号、企鹅号、搜狐视频、搜狐号、知乎、网易号、一点号、CSDN、AcFun |
| 仅视频 | 芒果 TV、爱奇艺号、YouTube |
| 仅图文 | 公众号、头条号、掘金 |

图文的最终形式取决于平台，例如哔哩哔哩专栏、抖音图文和微头条。公众号的全自动模式会保存到草稿箱，群发需要在公众号后台扫码确认。发布到 YouTube 前，请从 Publisher 的「账号」页打开 YouTube Studio 并登录。

## 第一次发布

1. 在「账号」页选择平台，在应用打开的 Chrome 或 Edge 中登录。
2. 把视频或 Markdown 文件拖进「素材库」。编辑标题、简介和封面，然后选择平台。
3. 选择人工确认，等应用填好表单，在浏览器检查后手动提交。
4. 提交后回到「任务」页，点「我已提交」记录结果。需要定期更新时，可到定时发布页配置批次和时段。

例如导入 `demo.mp4` 时，Publisher 可以从同目录的 `demo_caption.txt` 或 `demo.txt` 读取简介和标签。图文的同名子文件夹可放配图，其中 `cover.*` 用作封面。

## 从源码运行

在 Windows 上准备 Go 1.27.3 或更高版本、Bun、WebView2 Runtime 和 Chrome 或 Edge。生成 Windows 安装包还需要 NSIS。

```bash
git clone https://github.com/haowu8888/publisher.git
cd publisher
bun install
bun run dev
```

## 检查与构建

```bash
bun run typecheck
go test -timeout=60s ./...
bun run build -- -platform windows/amd64
```

Windows x64 构建产物在 `build/windows-amd64/`。素材记录、任务和设置保存在 `%APPDATA%\Publisher\data.json`，独立的浏览器配置在 `%APPDATA%\Publisher\chrome-profile`。

前端入口在 [src/](src/)，各平台的发布适配在 [internal/platforms/](internal/platforms/)，排队和定时逻辑分别在 [internal/queue/](internal/queue/) 和 [internal/schedule/](internal/schedule/)。实现细节见 [开发笔记](CLAUDE.md)。
