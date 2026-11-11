# MyGo 原生播放进度补丁

基于官方 MyGo Music 0.1.22，基线提交 `1af0d8efd77fdf630c27cfc635f12e9cb089ce10`；配套构建的源码提交 `59c76a17aae40fb20e41bd6ac2e25c77c90f553a`。

该构建是本项目的配套版本，不是 MyGo 官方发行版。增加通用、可选的 playbackStart/playbackProgress 接口，用于原生播放器云端续播及真实位置上报；未实现这些导出的插件不增加周期任务。

preview.2 还修复“关注的节目”首次加载后永久保留旧列表的问题。可选 `libraryRefreshSeconds` 启用前台周期更新和重回窗口刷新，未启用的来源不增加轮询。新增行为测试覆盖动态关注/取消、后台不轮询、重回前台、并发合并与旧回包隔离。

完整修改见 [native-playback-progress.patch](native-playback-progress.patch)。对应完整源码为 Releases 中的 `mygo-music-native-progress-source.zip`。宿主及补丁遵循原仓库 [AGPL-3.0](LICENSE)，包含原许可证；其他项目文件的许可不在此另行变更。

## 构建与检查

安装 Go 1.27.3，在官方基线检出后执行：

```sh
git apply /path/to/native-playback-progress.patch
go test ./internal/...
go tool mygo build -skip-installer
```

如用于继续开发，遵循源码中的 AGENTS.md。新增回归覆盖云端位置替代过期本机位置、加载途中手动定位优先、迟到进度不覆盖倒退。

实际账号验收为显式 opt-in，不在普通测试中自动执行：设置 `MYGO_APPLE_PLUGIN` 为本插件目录、`MYGO_APPLE_DATA` 为插件已有私有数据目录，然后运行 `go test ./internal/player -tags applepodcasts_live -run TestApplePodcastsNativeLive -count=1 -v`。它会以音量 0 播放选定的公开测试单集，修改、读回并恢复该集测试前的位置；请仅在自己允许读写的账号上执行。

播放器在播放中每 15 秒、暂停、定位、切集、结束及退出时投递快照；同步在后台执行，较新的同单集状态替代旧的等待项。辅助程序先保存再上传，失败保留待确认操作。完整行为见 docs/TECH_SPEC.md。
