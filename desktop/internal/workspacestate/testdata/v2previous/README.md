# Previous workspace v2 implementation

Frozen, unmodified `store.go`, `lifecycle.go`, and `lifecycle_json.go` from
`esengine/DeepSeek-Reasonix` commit `1f598bc9d616248feb6f916e936a9ff6dd61ec82`.
Only compatibility tests import this package. Tests require neither Git nor network.
The previous writer is intentionally not concurrency-safe for purge/restore; this
fixture proves format readability and unrelated-field round trips, not safe mixed-version deletion.

# 上一版 workspace v2 实现

上述文件原样固定于所列提交，仅用于兼容测试，不依赖 Git 或网络。
验证旧读取器与无关字段写回，不表示旧程序的删除并发缺陷已修复。共享目录写入进程应统一升级。
