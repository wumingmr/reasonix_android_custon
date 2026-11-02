# Discover Reasonix Studio from Desktop 1.x

Stable Desktop 1.x introduces Studio once after startup, when setup is complete
and the app has no foreground/background work, pending prompt, active decision,
or competing page/dialog. It waits for an idle interval; keyboard or pointer
interaction defers the automatic introduction to a later launch. Web/serve,
development, prerelease and Studio builds do not show it automatically.

**Settings → About → Explore Reasonix Studio** always opens the same introduction.
**Download Studio** opens <https://reasonix.io/?download=studio#start> in the
system browser. Users select the current platform installer there. Nothing is
downloaded or installed automatically, and the 1.x updater is unchanged.

The local frontend key `reasonix.studio-introduction.v1` records that this
campaign has been shown (including manual opening). It is independent of app
version, so later patch releases do not repeat it. If storage cannot be read or
written, automatic display is suppressed and the About entry remains available.
Clearing application site data also clears this preference. No telemetry is added.

Studio can be installed alongside 1.x, but separate installation does **not**
imply isolated data or guaranteed rollback. Configuration/history may share
storage. Back up Reasonix data before trying Studio and avoid concurrent edits
to the same project in both applications. Some 1.x project directories are not
currently discoverable by Studio; see [configuration paths](CONFIG_PATHS.md).
No migration or session/configuration format change is part of this feature.

## 中文

正式版 1.x 会在启动完成、模型配置就绪且没有运行任务、待处理输入、审批或其他
页面/弹窗时，自动介绍一次 Studio。启动期间已有键盘或鼠标操作时，本次不再打断
用户，留待后续启动。网页版、开发版、预发布版和 Studio 不自动展示。

随时可以通过 **设置 → 关于 → 探索 Reasonix Studio** 再次打开介绍。
**下载 Studio** 进入官网 Studio 下载区，由用户选择系统安装包；不自动安装，
也不改变 1.x 的更新通道。关闭、Esc 和 **继续使用 1.x** 均返回原界面。

展示状态按这次推广记录，不随每次 1.x 小版本升级重置。清除应用站点数据会清除
此状态；存储不可用时只保留手动入口。不新增遥测，也不修改配置或历史数据格式。

Studio 与 1.x 可并存安装，但配置、历史可能共用存储，不保证数据隔离或安全回退。
体验前请备份数据，避免两个应用同时修改同一项目。部分因路径名称冲突而分配了独立
目录的 1.x 项目暂不能在 Studio 中显示，详见[配置路径](CONFIG_PATHS.zh-CN.md)。
