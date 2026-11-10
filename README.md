# Apple Podcasts for MyGo Music

在 [MyGo Music](https://github.com/egoist/mygo-music) 中登录苹果播客，浏览关注节目和已存单集，并遥控苹果官方播放器。

**当前为 `0.1.0-preview.1` 开发预览。** 已实测登录恢复、资料库读取、关注/保存/完成状态写回，以及免费单集的播放、定位和云端进度读回。MyGo 图形界面、手机与电脑交替续播、会员专属音频仍需验收，详见[验证记录](docs/VALIDATION.md)。

## 安装

目前目标为 **Windows x64、MyGo Music 0.1.22+、已安装 Google Chrome**。无需安装 Node.js 或 Go。

1. 从 [Releases](https://github.com/AceYKN/mygo-music-apple-podcasts/releases) 下载 `apple-podcasts-windows-amd64.zip`，在 MyGo 的“设置 → 插件 → ＋”选择本地 ZIP 安装。
2. 在插件设置中允许启动辅助程序，再选择 Apple Podcasts 来源。
3. 首次点击登录，在苹果官网完成登录与验证码。已有专用会话时直接复用，更新插件不会主动删除登录。

本预览**默认静音**。需要收听时，在“静音验证”设置中填写 `off`；留空则继续静音。改变此设置会重启辅助程序并保留登录。加载或控制播放时，Chrome 窗口会短暂展开，操作完成后自动最小化。

二进制只在平台 ZIP 中，仓库源码包不包含它；预览阶段请用本地 ZIP 安装。如果无法自动找到 Chrome，可在插件设置中填写其程序完整路径。

## 使用

- “关注的节目”“已存单集”“继续播放”“最新单集”读取苹果账号资料。MyGo 中的关注和保存操作写回苹果，并读回确认。
- 点击单集默认从苹果端已确认位置继续，MyGo 遥控播放、暂停、定位和音量。
- “播放与同步”可查看当前单集状态、标记已播放/未播放、从头播放，或展开官方窗口。
- 断网时保留带时间标记的展示快照。未确认操作保存在本机，可明确重试或放弃，不在后台反复提交旧操作。

## 当前限制

- 会员专属单集仍是正式版需求；尚无真实已授权样本完成验收，不能据此认定支持会员播放。
- “继续播放”使用平台实际数据，不宣称是全部历史。频道与订阅权益列表的界面仍待完善。
- MyGo 没有独立的单集完成状态字段，当前状态与标记入口暂放在“播放与同步”。
- 单次队列最多 200 集。完整播完、连续切集、正常关闭 MyGo 的最终进度及真实多设备冲突处理仍需验收。
- 缓存和待确认操作按授权会话隔离。令牌轮换后不会自动继承旧操作；旧记录保留在本机，同账号身份恢复与迁移流程尚未完成。
- 自然过期、多账号真实切换、长时间稳定性及 macOS/Linux 尚未验收。

以上缺口不改变[正式首版的需求](docs/PRD.md)，当前预览不能称为“完整同步版”。

## 本机数据

Windows 下通常保存在 `%APPDATA%\MyGo Music\plugin-data\apple-podcasts`。请保留其中的 `browser-profile`，删除它可能需要重新登录。退出账号会调用官方退出，并清除当前会话的同步快照和待确认操作。

辅助程序只监听 `127.0.0.1` 随机端口，使用每次启动的随机凭证。苹果令牌不写入源码、命令行或普通日志，账号认证头只发往苹果播客 API。项目没有公网后端，不分发 Chrome。

## 开发

技术栈为 TypeScript、esbuild、Go、chromedp 与正式 Chrome。MyGo 使用 `remote` 模式，具体规则见[技术规格](docs/TECH_SPEC.md)。需要 Node.js 24+、Go 1.25+：

```sh
npm ci --ignore-scripts
npm run check
npm test
npm run build
cd helper
go test ./...
go vet ./...
cd ../tests/runtime
go test ./...
cd ../..
npm run helper:build
npm run package
```

Windows 本机开发安装可运行 `node scripts/install-dev.mjs`，会保留登录资料。`bin/`、`dist/`、浏览器资料和真实账号测试记录均不提交 Git。

项目与 Apple、Google、MyGo Music 均无官方关联。
