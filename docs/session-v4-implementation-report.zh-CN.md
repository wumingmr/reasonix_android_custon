# 统一会话 v4 落地实施报告

日期：2026-09-14  
基线：`main-v2` 的 `744c2e94ec77b577fbe3f1c93c769b1c3388bd21`

## 已交付能力

- `internal/session` 是唯一生产会话服务。新建和继续写入统一使用
  `reasonix.session.linear/v4` 与 `storageRevision: 1`；旧 checkpoint、DAG、
  preview、线性 v3/v3.1 和未发布 v4 草稿只作为迁移输入。
- 大字段在事件接受前发布到 SHA-256 内容存储。内容支持流式写入、范围读取、
  分块校验、同授权域去重，并随导出归档形成完整引用闭包。
- 逻辑事务由有界、独立校验的 Zstandard 帧和 begin/event/end 记录组成。
  accepted 与 durable 水位分离；不完整事务不可见；写入压力产生可取消背压，
  不再变成会话累计容量拒绝。
- schema-1/checkpoint 迁移会流式复制并计算源摘要、增量解析消息、在磁盘暂存、
  验证目标后原子发布；失败不修改源数据。
- durable 历史投影到可重建 SQLite 索引。Desktop、Serve、远端 transcript、
  搜索、大字段展开和 Goal 诊断统一使用有界分页或流式 writer，不再通过全历史
  RPC 值传递。
- Runtime 只保留当前业务状态、accepted 尾部和模型工作集，不常驻全部 durable
  正文。导入导出、分叉、恢复和诊断保持固定快照及执行授权边界。
- 保留已合入 Goal 生命周期的 CAS、armed/disarmed、唯一续跑预留、用户输入优先
  和副作用前 Flush。Goal 轮数没有隐藏产品上限。

## 兼容结论

| 格式 | 当前读取 | 当前写入 | 降级行为 | 结论 |
| --- | --- | --- | --- | --- |
| checkpoint / schema-1 | 流式迁移或只读发现 | 不再写入 | 保留源字节 | 兼容 |
| schema-2 DAG | 图专属迁移适配器 | 不再写入 | 保留源图 | 兼容 |
| preview、线性 v3/v3.1 | 显式迁移适配器 | 不再写入 | 保留源字节 | 兼容 |
| v4 revision 0 草稿 | 显式迁移适配器 | 不再写入 | 草稿保持不变 | 明确迁移边界 |
| v4 revision 1 | 原生读取 | 唯一生产格式 | 旧版不会写该目录 | 明确单向边界 |

最终 v4 使用独立目录，不覆盖旧存储。冷浏览、迁移、导入和分叉都不会恢复批准
或 Goal activation。

## 缓存契约

内容哈希、磁盘路径、分页游标和持久化水位不会进入 provider 请求。系统提示、
工具 schema、消息顺序、推理签名、provider 元数据、可见性及压缩触发规则保持
原行为。序列化字节守卫覆盖 OpenAI Chat Completions、Anthropic 和 OpenAI
Responses，并比较 v4 关闭重开前后的实际请求字节。

## 验证证据

- 根 Go：`go test -p 2 ./... -count=1 -timeout=15m`
- Desktop Go：`cd desktop && go test ./... -count=1 -timeout=15m`
- Race：`go test -race ./internal/session ./internal/sessioncontent ./internal/control -count=1 -timeout=15m`
- 原 128 MiB 故障：`134,308,416` 字节合成旧日志可迁移、打开、校验并继续写入。
- 大单条记录：超过 64 MiB 的旧单记录不再因原记录大小门槛迁移失败。
- Provider 字节：OpenAI、Anthropic、Responses 在 v4 持久化并重开后的序列化
  请求字节一致。
- 容量场景：100,001 个事件、1 GiB 逻辑历史、1 GiB 附件和 16 MiB 模型工作集
  完成；参考 macOS 主机的索引页 P95 为 597 毫秒，进程峰值 RSS 约 235 MiB，
  冷启动耗时 10.1 秒。首次索引构建耗时 115.1 秒，尚未达到参考机 60 秒的
  期望目标；构建过程可取消，并且不阻止会话重开及流式访问。这些是测量结果，
  不是运行时准入上限。
- 256 MiB 场景的索引页 P95 为 153 毫秒，峰值 RSS 约 230 MiB。
- 仓库静态 ratchet 仍启用。由于无版本包重命名改变了按路径记录的 finding，且
  新迁移/查询实现有意增加源码复杂度，因此已重新生成并记录基线。

前端类型/测试、正式前端构建和原生 transcript 布局证据记录在 PR 检查结果中。

## 明确未包含的证据

按任务负责人要求，本次移除 Windows 安装与打包应用验证。因此不声明 Windows
文件系统、安装器、签名或 WebView2 验证通过。反馈用户的原始日志也尚未提供；
上述精确尺寸回归使用合成样本，不声明已经恢复该用户的真实会话。
