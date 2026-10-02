# Retired workspace drafts / 停用旧工作区草稿

Desktop no longer offers the previous workspace-draft workflow. This applies
equally to empty drafts, settings-only drafts, unsent content and unfinished
draft submissions. There is no project badge, draft page, recovery notice,
automatic migration or replay. New Conversation and navigation use formal
sessions directly, without waiting for or updating the old draft store.

Existing `desktop/drafts-v1.sqlite` files are left in place and are not read by
the normal application lifecycle. This is a product retirement, not a storage
rewrite: historical compatibility implementations remain available for fixture
verification, but are disconnected from the current application flow. Downgrade
to an older binary can still expose that binary's own draft behavior.

Already-created formal sessions and their chat history are retained. Current
formal-session input lives in `desktop/session-ui-v1.sqlite` and still saves
across switches/restarts. The shell exit callbacks now belong directly to that
input owner, preserving attachment completion, save-error exit veto and resumed
editing after a cancelled exit.

| Contract | Old data | Current behavior | Previous-version behavior |
| --- | --- | --- | --- |
| Draft SQLite | File retained unchanged | No lifecycle reads, migration, reconciliation or UI entry | Existing old reader/writer format unchanged |
| Formal input SQLite | Existing inputs retained | Normal save, conflict handling and exit barrier | Format unchanged |
| Session / workspace registry | Formal sessions and history retained | Normal creation and navigation | Format unchanged |
| Shell exit hooks | Historical callback names retained | Flush only formal input | Shell/renderer callback contract unchanged |

Regression coverage checks formal creation and saved-tab restore against a
corrupt historical draft database, dropping unfinished draft presentation while
retaining formal history, and formal input's attachment/save exit barrier.
Retirement also applies when saving reconciled tab preferences fails: the
fallback must not resurrect draft operation ownership, and an unpersisted
identity repair must not start a runtime.
Workspace-identity conflicts retain formal sessions for repair, including empty
ones. Saved-tab restore no longer runs the retired empty-session cleanup or
loads its draft-store evidence.
The old badge and draft-composer override assertions were removed because those
UI contracts are retired. Historical draft runtime fixtures use the same
`draft-op-` operation namespace as the old production writer.

## 中文

旧工作区草稿全部停用，不再区分是否包含文字或设置。项目旁不再显示“旧版草稿”，
也没有独立草稿页面、恢复提示或自动迁移。旧的未发送内容和未完成草稿提交不再
恢复或重发。新建与切换会话直接走正式会话流程，不依赖旧草稿数据库是否可读。

旧数据库文件留在原处，当前应用生命周期不读取、不改写。兼容实现仍供历史夹具
验证使用，但已从当前产品流程断开。降级到旧客户端仍可能看到旧客户端自己的
草稿行为。已创建的正式会话及聊天记录不变；正式会话的新输入保存功能继续保留。

正常退出仍等待新版输入及附件保存，保存失败保留窗口；取消退出后继续编辑。
数据库、会话、工作区和 Shell 回调格式均未改变。

回归覆盖旧草稿库损坏时的新建和恢复、未完成草稿入口的移除、正式聊天记录保留，
以及新版输入与附件的退出保存。旧草稿徽标和编辑器覆盖逻辑的断言随功能停用移除；
历史运行时夹具使用与原生产代码一致的 `draft-op-` 操作标识。
标签页配置写回失败时也不恢复旧草稿操作；尚未保存成功的身份修复不得自动启动运行时。
工作区身份冲突时保留正式会话供修复，包括空会话；标签页恢复不再触发旧空会话清理，
也不再通过该清理流程读取旧草稿库。
