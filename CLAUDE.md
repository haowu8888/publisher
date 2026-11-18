# Publisher 一键分发

把做好的视频、文章自动发布到多个平台的桌面应用。基于 [MyGo](https://mygo.egoist.dev)（Go + 系统 WebView），发布靠 go-rod 驱动一个**独立配置目录的可见 Chrome**：用户在里面扫码登录一次，之后复用登录态自动填表上传。

## 结构

- `main.go`：单实例锁（再次启动只显示已有窗口），打开数据存储、浏览器和任务队列，绑定服务，开主窗口；关窗口只隐藏到托盘（`assets/tray.png`），左键点托盘图标打开窗口，右键菜单「退出」才结束应用，定时发布在后台继续
- `alerts.go`：任务失败而用户没在看窗口时（多数时候应用在托盘里），发系统通知（`mygo.NewNotification`，Windows 上是托盘气泡；系统关了通知就不显示），并把托盘图标换成带红点的 `assets/tray-alert.png`、提示里写失败个数；窗口再获得焦点时恢复
- `services.go`：前端可调用的服务，`mygo generate` 会生成 `src/mygo.ts`
  - `Library`：素材导入（对话框、拖拽 `ImportPaths`）与编辑；导入时同名图片或 `x_cover` 图片自动作为封面，`.md` 开头的 `# ` 标题会成为文章标题，导入的文章公众号默认发贴图（`WeixinType` 设为 `images`），旁边的同名文件夹里的图片按文件名自然排序作为 `Material.Images`（`cover.*` 作封面，没有就取第一张；`material.go` 的 `articleImages`），可在素材里重新选择（`PickImages`/`ClearImages`；原封面取自旧图片或已不存在时，跟着换成新图片的 cover 或第一张）；视频的创作声明默认「内容无需标注」。素材库的提交方式默认全自动。`ImportPaths` 跳过素材库里已有的文件（`store.HasFile`，全是已有的就报错）和视频的字幕 `.txt`（`material.go` 的 `isCaption`），否则会多出一条素材被定时再发一遍
    - 视频旁有 `x_caption.txt`（或 `x.txt`）时，正文作简介、只含 `#标签` 的行作标签，标题从正文推导（`material.go` 的 `captionTitle`：第一句，不足 10 字接下一句，超过 30 字按分句截短）。格式来自 ai-explainer 的 `out/`
    - 文章 `.md` 里单独一行、只含 `#标签` 的行（`#` 后紧跟文字，所以 `# 标题`、`## 小节` 不算；代码块里的行也不算）同样作标签，并从正文里去掉（`material.go` 的 `articleTags`/`isTagLine`），一般写在文末，如 `#AI #MCP #人工智能`。Markdown 编辑器会把行首的 `#` 存成 `\#`（防止变成标题），也按标签认。小红书把标签输成话题，公众号不用标签。已经导入的文章不会重新解析
    - 同步文件夹（`Folders`/`AddFolder`/`RemoveFolder`/`SyncFolders`，可添加多个，存在 `data.json` 的 `folders`）：逐个文件夹导入顶层（不含子目录）里没导入过的视频和 `.md`，每个文件夹内视频按文件名自然排序（ep42 在 ep100 前）、文章按文件创建时间（`createdAt`，Windows 外用修改时间）依次加入；读不了的文件夹跳过，列表里显示原因，定时发布就按这个顺序发。读不了的文件留到下次，只有一个都没导入时才报错。设置里打开 `autoSync` 后，定时发布到点时先同步一次（`main.go` 的 `autoSync` → `Library.sync(settleTime)`），5 分钟内改动过的文件（含视频的字幕、封面，文章的图片文件夹，`fresh`）留到下一次，免得发出还没写完的。`store` 的 `Imported` 记下导入过的每个文件，发布过的素材删掉后再同步也不会重新导入；没发布过的删掉时会从 `Imported` 移除，再同步会回来
    - 合集：`Platform.Collections` 为真的平台（抖音、小红书）发布时加入合集。素材的 `Collections` 优先，留空用设置里的默认，填「无」（`store.NoCollection`）不加入；由 `Settings.Collection` 解析后放进 `Job.Collection`，`Job.Original` 来自设置。合集名找不到时任务失败并列出现有合集（`pickByName`）
  - 标签全部保留，各平台发布时截取：快手前 4 个、抖音前 5 个、B站/小红书前 10 个、百家号视频在作品描述里放得下几个算几个；标题超出平台上限时用 `fitTitle` 在分句或词边界截断
  - 字数上限集中在 `platforms.Limits`（`Platform.Limits`，按素材类型，公众号贴图用 `images`），发布代码和界面共用：素材编辑页的 `showLimits` 实时显示字数并标出会被截断的平台；正文里要打进话题的平台用 `fitText` 给话题留出字数
  - `Accounts`：各平台登录与检测；账号页的「一键检测」逐个检测全部平台，「一键重新登录」同时打开未登录平台的登录页（都已登录时确认后全部重新登录）
  - 发布状态记在素材上（`Material.Published`，平台 → 首次成功时间），由 `store.UpdateTask` 在任务变成成功时写入，`PutMaterial` 不会覆盖；所以删除任务、「清除已结束」后素材仍显示已发布。旧数据在 `store.Open` 时从成功任务补上
  - `Library.Delete`：素材有排队中或进行中的任务时拒绝（`store.ErrMaterialBusy`），先取消任务
  - `Library.DeletePublished`：批量删除发过的每个平台都已发布、且没有进行中任务的素材（只删记录不删文件）
  - 删素材（`store.DeleteMaterials`）不删它的任务，由任务页「清除已结束」清理；任务入队时记下素材的 `Title`/`Kind`（旧任务在 `store.Open` 时补上），素材删了任务页仍能显示和分页签，但不能重试
  - `Publisher.Delete`：删除单条任务（排队中、进行中的要先取消）；`Publisher.ClearFinished` 保留「待确认」的任务，删了素材会算作没发过、被定时再发
  - `Publisher`：创建、取消、重试发布任务
  - `Scheduler`：定时发布的设置、待发布列表、立即运行
  - `Settings`：设置页。开机自启用 `mygo.App.SetOpenAtLogin`（写 HKCU Run，带登录参数启动时窗口 `Hidden` 直接进托盘），不存数据；其余存在 `data.json` 的 `settings`（零值即默认）：`theme`（空=跟随系统，用 `mygo.Theme.SetSource`，页面同时设 `color-scheme`）、`quitOnClose`（关窗口直接退出）、`keepBrowser`（全自动任务后不关浏览器）、`autoSync`（定时发布前自动同步文件夹）、`noUpdateCheck`（不自动检查更新）、`ui`（外观：`accent` 主题色、`style` 风格 ""/`glass` 毛玻璃/`soft` 柔和、`corner` 圆角、`zoom` 缩放百分比、`background` 背景图片、`veil` 图片透明度、`panel` 面板透明度（只在有背景图时生效）、`blur` 模糊）、`collections`（各平台默认合集名，平台 id → 名称）、`original`（小红书声明原创）
  - `Updates`（`updater.go`）：设置页的「检查更新」。Flow 流水线 `publisher` 每次构建把 `Publisher-<版本>-setup.exe` 和 `Publisher-<版本>-portable.zip` 传到 Cloudreve `/builds/publisher`（只留最新一版，公开分享 `19I6`），版本号 = `mygo.config.ts` 的主次版本 + 运行号（0.1.<运行号>，流水线构建时改写，仓库里保持 0.1.0）；应用匿名调 Cloudreve v4 API 列出分享里的文件、按文件名比版本。安装版（exe 旁有 `Uninstall.exe`）下载 setup 后静默 `/S` 安装，免安装版下载 zip 解压后覆盖文件夹（含 `extension/`），都由 `updater_windows.go` 的 PowerShell 脚本等应用退出后执行并重新启动（日志在 `%TEMP%\publisher-update-*\update.log`）。有排队中或进行中的任务时拒绝更新。启动 1 分钟后和之后每 6 小时自动检查（设置 `noUpdateCheck` 关掉），有新版时侧栏「设置」显示「新」。本地构建的 `build/windows-amd64` 没有 `Uninstall.exe`，会被当成免安装版；它的版本号是 `x.y.0`（Flow 不会给出），算作本地构建（`UpdateState.Local`）：不自动检查，更新前提示网盘版本不一定比本地代码新。有「待确认」任务时更新前也会提示（退出会关掉浏览器里填好的页面）
  - 外观（设置 → 外观）：前端 `applyLook` 把主题色写成 `--accent`/`--accent-ink`（按亮度选黑白字），风格、圆角写成 `<html data-style/data-corner>`，`src/style.css` 末尾按这些属性改变量和样式；缩放用 `Page.SetZoomFactor`（`applyZoom`，保存时和开窗口时调用），不用 CSS zoom。背景图片由 `Settings.PickBackground` 复制到数据目录的 `backgrounds/`（每次新文件名，换掉的删除），`main.go` 注册的 `bg` 协议提供给页面；WebView2 把自定义协议映射成 `http://bg.localhost/`，所以地址由 Go 给出（`SettingsInfo.BackgroundURL`），不要在前端拼，应用的 UA 是 Mac 的，判断不了系统。`#backdrop` 是固定在最底层的背景层：图片 + `--blur` 模糊 + `--veil` 透明度的底色遮罩（界面上叫「图片透明度」）；面板透明度设 `--panel-a` 和 `data-clear`，把 `--panel`/`--panel-2` 混成半透明。`Save` 不接受前端改 `background`
  - 事件 `taskUpdated`、`accountUpdated`、`cookiesSynced`、`scheduleRan`、`updateChanged`
- `internal/store`：JSON 存储，位置是 `%AppData%\Publisher\data.json`。每次正常打开时另存一份 `data.json.bak`；`data.json` 读不了时改名为 `.corrupt`、换用备份，并关掉定时发布（备份之后发布过的素材会显示成未发布），`main.go` 弹框说明（`Store.Restored`）。任务只改了步骤文字（`Step`）时不写文件，上传时每 2 秒一次进度没必要落盘，所以 `data.json` 和 `.dev/tasks.sh` 里的步骤可能是旧的，状态是准的
- `internal/cookiesync` + `resources/extension/`：从用户日常 Chrome 同步登录。原因是 Chrome 136 起不再允许用调试端口接管默认配置目录，所以改用配套扩展（MV3）：扩展（改了要升 `manifest.json` 的版本号，用户在 `chrome://extensions` 点重新加载才会生效）从 GET `/platforms`（`platforms.SyncPlatforms`：各平台 id、名称、Cookie 域名，不能同步的不给域名、带原因）列出平台和本浏览器里的 Cookie 数，可「一键同步全部」或勾选后「同步所选」（没勾的记在扩展的 `localStorage`），读取所选平台的 Cookie，POST 到 `127.0.0.1:17891/import`，应用再用 `Storage.setCookies` 写进自动化 Chrome，之后检测这些平台的登录状态。GET `/domains` 留给 0.3 之前只能全部同步的旧扩展，新扩展连旧应用时也退回用它
  - 安全规则：POST 必须带 `chrome-extension://` 来源且是 JSON；GET `/platforms`、`/domains` 扩展请求不带 Origin，所以放行（网页来源除外）
  - 平台的 Cookie 域名在 `Platform.CookieDomains` 里，新增域名时要同步改扩展 `manifest.json` 的 `host_permissions`
  - 抖音不参与同步（`LoginNote`）：它的写请求用存在页面存储里的密钥签名，只复制 Cookie 时显示已登录，但提交会提示「登录失效」。必须在应用里扫码；「登录」会先清掉该平台旧 Cookie；检测登录看首页有没有「扫码登录」（`LoginText`），不看 Cookie。服务端注销会话后 Cookie 还在，页面原地显示登录框，所以 `loggedOut` 也看页面上有没有「扫码登录」；任务因未登录失败时会把账号标成未登录（`queue.New` 的 `onLoggedOut` → `Accounts.record`，同时发 `accountUpdated`）
  - `resources/` 的内容会被复制到 exe 旁边，所以扩展目录在 `build/windows-amd64/extension`
- `internal/browser`：启动 Chrome/Edge，配置目录是 `%AppData%\Publisher\chrome-profile`，并能接管上次崩溃后遗留的实例（读 `DevToolsActivePort`）
- `internal/platforms`：平台注册表 `All`，目前有 B站（视频）、快手、抖音和小红书（视频 + 文章）、视频号（视频）、公众号（文章）、百家号和头条号（视频 + 文章）。每个平台记录支持的素材类型 `Kinds`、创作中心 URL、登录判定和 `Publish` 函数。登录判定用登录 Cookie，或看页面 URL：含 `LoggedInMark` 算已登录，含 `LoginMark` 算未登录
  - `page.go`：共用工具
    - `openFresh`：给会从 `localStorage` 恢复上次表单的编辑器用（百家号、头条微头条），先在同域的轻量页面（`robots.txt`）删掉匹配的草稿键再进编辑器，否则新任务会带上上一个任务的内容
    - `openForm`、`waitElement`：打开发布页，等待元素出现，同时识别是否跳到了登录页；页面显示「加载失败，请刷新」（快手偶尔这样）时自动刷新一次。`openForm` 在任务期间自动应答页面的原生弹窗（alert、「离开此网站？」），否则页面会卡住，所有 CDP 调用都不返回
    - 页面里的循环检查都用 `page.Timeout(evalTimeout)`；队列每个任务最多一小时，防止一个卡住的页面挡住后面的任务
    - `startUpload`：等上传组件就绪后再给文件，没开始就重传一次。页面刚加载完就给文件会被忽略
    - `waitUploaded`：用「重新上传」出现、「取消上传」消失判断上传完成；`uploadWatch` 发现页面新出现「上传失败」之类的字样就立即失败（第一次看到时已有的算页面固有文字），B站的上传等待也用它
    - `finish`：全自动发布成功后报告最后一步并关掉标签页，否则浏览器一直开着时标签越积越多；失败的留着给用户看
    - `find`/`findR`：带超时查找元素，但返回的元素不继承超时（`waitElement` 返回的元素也换回默认的 Sleeper，否则之后 `Focus` 里等元素可见会立即报 cannot find element）。直接用 `page.Timeout().Element()` 拿到的元素，后续操作会因为超时而失败
    - `fill`、`typeInto`、`pasteHTML`：写入输入框或富文本编辑器。文章先把 Markdown 用 goldmark 转成 HTML，再模拟粘贴
    - `jsClick`、`clickText`：用脚本点击，绕开引导浮层
    - `setCover`：有封面的视频在上传完成后设置封面（抖音、快手、小红书各自的 `xxxCover`）；设不上不算失败，按 Esc 关掉可能留下的编辑框，在最后一步里注明用了平台默认封面
    - `dismissGuides`、`waitPublished`：关闭引导弹窗；提交后等待成功跳转或提示，页面新出现「登录失效」时立即失败；超时时用 `pageNotes` 把页面上的提示/弹窗文字写进报错（只取看得见的，隐藏对话框里的文字不算）
  - 各平台要点（选择器截至 2026-11，用 `.dev/inspect.sh` 在真实登录态下核对过）
    - `bilibili.go`：标签要先删掉推荐标签（点 svg）再逐个填；封面编辑器点「完成」后可能弹出「确认同步」，确认后要再点一次「完成」；「创作声明」必填，取素材的 `Statement`；`Platform.Check` 在加入队列时就拒绝没选声明的全自动任务
    - `douyin.go`、`kuaishou.go`：快手没有标题框，标题并入描述；抖音的话题目前是纯文本 `#tag`，没有从候选里选；抖音合集点 `[class*=select-collection]` 再按 `.collection-option` 里的标题选。抖音的文章发成图文（`upload?default-tab=3`）：`noteImages` 的图片（封面在前，最多 35 张）一次给 `input[accept^=image]`，等「已添加N张图片」且没有「上传中」；标题限 20 字（`douyinImageLimits`），描述是 `paragraphs` 的正文（换行会变成分行），话题同视频；封面就是第一张图；合集是另一个下拉（`[class*=mix-sel-wrap]` 里不是 `select-mix-type` 的那个，选项 `.semi-select-option`）；没有任何图片的文章由 `checkPictures` 拒绝（定时发布的 `Targets` 会跳过 `Check` 不通过的平台）。「发布文章」（长文）要求另传封面，没用。封面：抖音设竖封面（`[class*=cover-tip]` 含「竖封面」的那一格 → 编辑框里 `.semi-upload[class*=upload-] input` 上传 →「完成」→ 弹出「设置横封面」时点「暂不设置」，抖音会用竖封面生成横封面）；快手点 `[class*=_cover-full-editor_]` → `.ant-modal` 的「上传封面」页签 → 上传 → 等 `canvas[class*=_cutter-raw_]` 画出图片（「清空上传」出现时还没画好，这时确认封面是黑的）→「确认」。快手上传页有「还有上次未发布的视频/图集」草稿条时先点「放弃」（`kuaishouNoDraft`，否则给的文件会加进草稿里）。快手的文章也发成图文：「上传图文」页签，图片输入框要先用脚本点一下「上传图片」按钮才认后面给的文件（脚本点击不会弹出文件选择框），最多 31 张，等「N/31」；描述框是 `div[contenteditable][class*=_description_]`，标题 + `paragraphs` 正文 + 前 4 个话题，同视频；封面是第一张图。抖音和快手没有图片的文章都由 `checkPictures` 拒绝
    - `xiaohongshu.go`：「发布」按钮在 `xhs-publish-btn` 的封闭 Shadow DOM 里，网页脚本和按文字查找都找不到，用 rod 的 `ShadowRoot()` 穿进去点；视频走视频笔记；有图片的文章走图文笔记（`target=image`，先给 `input.upload-input` 第一张，再给表单里的 `input[multiple]` 其余的，等 `.title-area .status` 显示 `n/18` 且没有 `aria-busy` 的图片；发布成功后 URL 带 `published=true`；正文按段落粘贴），没图片的走「写长文」→「一键排版」→「下一步」→笔记表单。图片顺序是封面在前（`noteImages`）。话题输入 `#tag` 后按回车选中第一个候选。合集：`.collection-plugin-button` → `.collection-plugin-popover .item`。原创声明：`.custom-switch-card`（含「原创声明」）的开关 → 弹窗 `.originalContainer` 勾选同意 →「声明原创」；视频短于 10 秒时开关禁用，跳过并在步骤里注明；要等视频上传完再点。视频封面：`.cover-plugin-preview .cover-edit-entry` → `.cover-modal input.upload-input` → 等 `.uploaded-thumbnail` →「完成」，按钮变成转圈后要几十秒编辑框才关；还显示「完成」说明没点上，再点
    - `weixinmp.go`：从首页跳转后的 URL 里取 token。`Material.WeixinType` 为空发文章：正文里单独一行的图片文件名（如 `3.jpg`，文件存在才算）由 `articleBody` 换成图片，相对路径先找 md 旁边、再找同名文件夹（`articleDirs`）；正文没引用任何图片时，`spreadImages` 把除封面外的 `Images`（`bodyImages`）均匀插到顶层段落之间；正文里的本地图片用 `inlineImages` 转成 data URL 粘贴，编辑器会自动转存到 mmbiz，等没有 `data:` 图片再继续；封面走「从图片库选择」（`.js_imagedialog`）→ 对话框里上传 → 自动选中 →「下一步」→「确认」（`clickVisible` 只点可见对话框的按钮，隐藏对话框里有同名按钮）。为 `images` 时发贴图（`createType=8`）：逐张给 `input[accept^="image/bmp"]`，等列表项的背景图变成 mmbiz 地址（传完）再给下一张，没传完就保存草稿会存不上；上一张没处理完给的文件会被忽略，所以间隔 2 秒并重试；标题限 20 字，描述按段落粘贴。全自动模式只到「保存为草稿」；表单刚填完时编辑器偶尔不理会这次点击，所以每 20 秒没看到「已保存」就再点一次（最多 4 次，重复保存还是同一份草稿）
    - `channels.go`（视频号，`weixin-channels`）：页面用 wujie 微前端，表单在 `wujie-app` 的（开放）Shadow DOM 里，`document.querySelector`、rod 的 `Element`、`document.body.innerText` 都够不着，所以用 `chRoot`/`chFind`/`chWait`/`chClick`/`chText` 在里面查找；发表成功看 URL 变成 `post/list`；`page.go` 的 `bodyText`、`pageNotes` 也读这个 Shadow DOM，所以「登录失效」和超时时的页面提示对视频号同样有效。先传视频（`取消上传` 出现即开始，「发表」不再 disabled 且没有「取消上传」算传完），同时填描述（`.input-editor`，标题不在描述里时放第一行，`#tag ` 加空格变成话题，最多 10 个）。`Limits` 没有标题上限（和快手一样标题并入描述，界面按此计字数），短标题由 `shortTitle` 生成：6–16 字（`shortTitleMax`），符号只留书名号、引号、冒号、加号、问号、百分号、摄氏度，逗号等换成空格，不足 6 字不填。合集：`.post-album-display` → `.post-album-wrap .option-item .name`，选中后显示框文字变成合集名。封面只设个人主页卡片 3:4（`.vertical-cover-wrap .edit-btn` → `.single-cover-uploader-wrap input[type=file]` → 等里面出现 `img` →「确认」，编辑框几秒后才关），分享卡片 4:3 保留视频画面。位置在视频传完后设成「不显示位置」（`chNoLocation`：点 `.position-display-wrap` 打开，再点 `.location-item` 里的「不显示位置」，平台默认会填浏览器定位到的城市）。Cookie 在 `channels.weixin.qq.com`（`sessionid`、`wxuin`），可从 Chrome 同步；公众号的 Cookie 都在 `mp.weixin.qq.com`，所以两者的 `CookieDomains` 分开写，免得一个重新登录清掉另一个
    - `baijiahao.go`（百家号）：`bjhOpen` 用 `openFresh` 删掉编辑器存在 `localStorage` 里的本地草稿（`edit-<id>-news`/`-videoV2`/`-localReload`），否则新表单会带上次的内容（上一篇的封面，或者在这次的视频旁边多出上一个视频）。视频（`type=videoV2`）：传视频后才出表单；没有标题和简介，只有 50 字的「作品描述」（Lexical 编辑器，界面上显示成标题），所以放标题，`Limits` 只有 `Title: 50`；话题输入 ` #tag` 后点 `[class*=topicItem]` 里同名的候选（没有就第一个），变成 `#tag#`，在描述里有空位才加；创作声明点 `input[placeholder="请选择创作声明"]` 打开弹窗选，素材的声明用 B站的说法，由 `bjhStatements` 对应（「内容无需标注」→「无需声明」）；上传进度是缩略图上 `[class*=tabCoverOverlay]` 里的百分比，消失且封面出现算传完（「发布」按钮上传中也能点）；封面有横（16:9）竖（3:4）两格，竖图设竖封面、横图设横封面（`wide`），点格子 → 弹窗最后一个文件输入框（本地上传）→ 自动选中 →「确定」。文章（`type=news`）：标题是 Lexical（`[data-testid=news-title-input]`），正文是 UEditor iframe（`#ueditor_0`），用 `UE_V2.instants` 的 `setContent` 写入，data URL 图片编辑器会自动转存成 `bjh/picproxy`；封面必填，点「选择封面」框（`-default > -content`，点外框没反应）打开同样的弹窗，素材没封面就用弹窗默认选中的正文第一张图，正文也没图时全自动直接失败；文章没有标签。Cookie 是 `.baidu.com` 的 `BDUSS`。全自动的发布成功判定（跳到 `rc/content` 或出现「发布成功」）还没实测过
    - `toutiao.go`（头条号，`mp.toutiao.com`）：Cookie 是 `.toutiao.com` 的 `sessionid`。视频（`profile_v4/xigua/upload-video`）：横版视频有标题（30）、话题（`.form-item-hash_tag input`，输入后等候选加载再回车，最多 10 个，不占简介字数）、简介（`textarea.abstract`，400）、合集；竖版视频只有标题、封面、「视频生成图文」和作品声明，所以话题、简介是有才填。「上传成功」算传完，100% 后还要等服务器处理一阵。封面必填：点 `.form-item-poster` 打开 `.m-poster-upgrade`，有素材封面走「本地上传」页签，没有就在「封面截取」点「下一步」用第一帧；编辑器里比例不对时要先点「完成裁剪」（`.clip-btn`），再点 `.btn-sure`，再在「完成后无法继续编辑」确认框（`.m-dialog-edit`）点「确定」。编辑器画好图片前会忽略「确定」且没有就绪标志，所以每 2 秒再点一次直到确认框出现；确认框是 fixed 定位，`offsetParent` 为 null，要用 `getClientRects()` 判断可见。全自动下封面没设上就不提交（头条封面必填）。作品声明是勾选框（`.form-item-source label`），`ttStatements` 只对应「AI生成」「虚构演绎，故事经历」。「视频生成图文」平台默认勾上并与视频同时发布，没动它。文章发成微头条（`profile_v4/weitoutiao/publish`，`openFresh` 清 `wtt-mp(form|editor)draft`）：没有标题，标题放正文第一行（`Limits` 只有 `Text: 2000`）；图片点工具栏「图片」→ `.upload-image-panel input` 一次给全（最多 18）→ 等「已上传 N 张图片」→ 抽屉里「确定」→ 表单显示「共 N 张」；正文按段落粘贴到 ProseMirror；话题点工具栏「话题」→ `section.mention-tooltip-modal` 的「搜索话题」→ 点 `.forum-list-item` 里同名的（没有就第一个），直接输入 `#` 不会变成话题。头条号还没有合集，合集没做。全自动的发布成功判定（URL 含 `/manage`/`/content` 或出现「发布成功」）没实测
  - 八个平台都会自动设置封面（小红书图文的封面是第一张图）
- `internal/schedule`：定时发布。视频和文章各有自己的时间、每次条数和平台（`Schedule.Times`/`PerRun`/`Platforms` 都按素材类型，`Ran` 记各类型上次运行）；每 15 秒检查一次，某类型到了设定时间（晚 10 分钟内仍会补跑）就取该类型待发布的前 N 个，按设置的平台以全自动模式入队；「立即发布下一批」两类都发。旧数据的单一 `times`/`perRun` 在 `store.Open` 时复制给两类
  - 待发布：从没被定时排过（`Material.ScheduledAt` 为空）、没在任何平台发布过（`Published` 为空），且没有待确认或进行中的任务；按导入顺序先旧后新。每个素材只会被定时排一次，失败的由用户在任务页重试
  - B站要创作声明：素材没选时用设置里的默认声明（会写回素材，发百家号时也这样）；默认也没设就跳过 B站
  - 保存设置时把各类型的 `Ran` 设成现在，避免刚设的、已过去的时间立即触发；设置存在 `data.json` 的 `schedule`
- `internal/queue`：任务按顺序执行（共用一个浏览器）；开始前检查素材的视频、文章、封面和图片文件都还在，缺了直接报出文件名；`review` 模式只填表，由用户在浏览器里点提交，`auto` 模式会自动提交。一轮任务跑完后，如果浏览器是这轮才打开的、且全是 `auto` 任务（如定时发布），就把浏览器关掉；本来就开着、有确认任务、有任务失败（留着页面给用户看）或设置了 `keepBrowser` 时保留
- `src/main.ts` + `src/style.css`：原生 TS 界面（确认框用 `ask()`，即 `index.html` 里的 `<dialog id="ask">`，别再用浏览器的 `confirm()`/`alert()`；设置页的勾选框画成开关），分素材库、账号、任务、定时发布、设置五页；素材库和定时发布的待发布列表、任务页都按视频、文章分两个页签，按导入顺序先旧后新列出（和定时发布的顺序一致）；素材编辑框里没保存的修改，在切换素材、页签或页面前自动保存（`keepEdits`）；任务页的「重试失败的」把当前页签里失败、且之后没在该平台发成功的任务重新排队

## 新增一个平台的发布

在 `internal/platforms/` 写 `publishXxx(ctx, b, job)`，并赋给 `All` 中对应平台的 `Publish`。写法参考 `bilibili.go`：
1. 用 `b.Page(url)` 打开发布页，等待出现上传框或跳转到登录页（未登录时返回 `ErrNotLoggedIn`）
2. `SetFiles` 传入本地文件路径
3. 用 `fill` 或 `InsertText` 填写文字
4. 用 `job.Report` 报告进度
5. `ModeReview` 时到提交按钮前就返回

选择器可以参考开源扩展 leaperone/MultiPost-Extension 的 `src/sync/{video,article}/*.ts`。

## 开发与构建（在 Windows 宿主机上）

- Go 1.27+（宿主机装在 `%LOCALAPPDATA%\Programs\go`）、Bun、WebView2
- `bun install`，然后用 `bun run dev`（热重载）或 `bun run build -- -platform windows/amd64` 构建，产物在 `build/windows-amd64/`（exe 和 NSIS 安装包）
- 容器里的 bun 比宿主机新，生成的 `bun.lock` 宿主机读不了：依赖只在宿主机安装
- 容器内检查：`go vet ./...`；交叉编译用 `CGO_ENABLED=0 GOOS=windows go build -o /dev/null .`

## 调试工具 `.dev/`

- `launch.cmd`：带 WebView2 调试端口 9223 启动已构建的 exe。从容器里启动要通过交互式计划任务 `PublisherDev`，因为 SSH 会话（session 0）看不到桌面
- `drive.sh '<js>' [截图名]`：在宿主机上通过 `go run ./.dev/drive` 往应用窗口执行 JS，可以选择截图到 `.dev/`。模拟鼠标点击对 WebView2 无效，所以用这个办法
- `inspect.sh '<url 或标签页 URL 片段>' '<js 表达式>' [截图名]`：连接应用的自动化 Chrome（读 `DevToolsActivePort`），在指定标签页执行 JS；脚本写成 `upload:<选择器>|<文件>` 是给文件输入框传文件（选择器写成 `宿主 >>> 选择器` 穿进 Shadow DOM，如 `wujie-app >>> input[type=file]`），`shadow:<选择器>` 是打印元素的 Shadow DOM（封闭的也行），`close` 关闭该标签页。30 秒无响应就退出（页面多半被原生弹窗卡住）。`dump.js` 会列出页面上的表单控件
- `drive.sh` 和 `inspect.sh` 的 JS 要写成表达式，函数要立即调用，如 `(async () => ...)()`。应用窗口里可以直接用 `mygo.call("Publisher.Publish", id, [平台], "review")` 调后端
- `tasks.sh [n]`：打印最新的 n 个任务；`stop.ps1`：结束应用和它的自动化 Chrome（只杀应用配置目录的那个）
- `toast.ps1`：连拍屏幕右下角（系统通知出现的位置）到 `.dev/toast1..3.png`，同样要通过交互式计划任务运行
- `dialog/`：应答卡住标签页的原生弹窗；`screen.ps1`、`windows.ps1`：截取指定窗口（被遮挡也能截）、列出桌面窗口标题，要通过交互式计划任务 `PublisherShot` 运行
- `exttest/`：在容器里用 Playwright 的 Chromium 加载扩展，配合假 Cookie 端到端测试「一键同步全部」。运行 `go run ./.dev/exttest ~/.cache/ms-playwright/chromium-1140/chrome-linux/chrome`
- `sync-test.ps1`：在宿主机上模拟扩展，往运行中的应用 POST 一条测试 Cookie
- `data.json` 和测试音视频：可复制到 `%AppData%\Publisher\` 作为种子数据；`测试视频_cover.jpg` 是和视频画面明显不同的 9:16 竖版封面，用来看封面有没有真的设上；`测试图文.md` + `测试图文/` 是带封面和正文图片的测试文章
