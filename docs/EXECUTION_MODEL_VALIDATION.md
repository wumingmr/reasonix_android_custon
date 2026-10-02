# 执行机制精简：改动与验证记录

日期：2026-09-12。配套设计与兼容说明见 [执行模型迁移](EXECUTION_MODEL_SIMPLIFICATION.md)。

## 审阅摘要

普通回合曾因风险推导、验证缺项或未完成待办被追加工作；现在由模型正常结束回合。Goal 通过结构化模型报告完成或阻塞，不再调用独立 evaluator。Plan 批准后采用普通执行语义，批准前的写入限制保持有效。

结果面板分别展示实际命令结果、修改后的检查过期状态和模型声明。新记录使用 `assessmentKind: facts` 与兼容值 `verdict: unknown`；历史质量结论保留为历史评估。旧接口、检查点一次性消费和启动失败回滚保持兼容，恢复的 Goal 不自动激活。待办不会随 Goal 完成被批量改写。

执行核心和 Goal 驱动不再依赖质量验收实现。`taskcontract` 仅保留旧类型；控制器的旧 quality-floor 返回接口仍引用兼容类型，固定返回无策略。权限、Plan 写入限制、沙箱、租约、取消和预算仍有独立回归覆盖。单个工具的普通失败不会阻断同批后续独立调用。

## 自动验证

以下检查均通过：

- 根模块及桌面独立模块：`go test -p 2 ./... -timeout 300s`。
- 核心并发回归：`go test -race ./internal/control ./internal/agent ./internal/jobs ./internal/runtimepolicy -run 'Goal|TurnResult|ReadinessRecovery|Leas|MutationEvidence|Batch|Plan|Constraint' -timeout 240s`。包含用户输入、取消与 Goal 完成报告的确定性交错测试。
- 桌面并发回归：在桌面模块执行 `go test -race . -run 'Goal|ReadinessRecovery|SessionSwitch|QualityFloor|Lease' -timeout 240s`。
- 前端：`pnpm test:all`、workspace、stream、app-lifecycle 测试以及 `pnpm build`。构建包含类型、hooks、布局边界、CSS 和产物体积检查。
- `make lint`、`git diff --check`。
- 桌面 `go run . -emit-contract frontend/src/generated`；生成差异为可选事实标记及对应摘要，完整桌面测试覆盖生成契约。
- `bash scripts/cache-guard.sh`：通过；两个工具循环样例为 89%，触发低于 90% 的提示，其他样例及稳定扩展前缀检查通过。这不代表缓存命中率已改善。

## 交互验证

真实浏览器使用项目界面与 `desktop/frontend/bench/execution-facts.html`，验证菜单无交付模式、未运行检查的中性展示、失败退出码、修改后检查过期、模型声明与事实分离、历史质量评估，以及中英文和繁体中文文案。

本地 Electron 使用隔离的状态目录、临时项目及固定响应的本地 SSE provider，验证发送、停止、Plan 预览与批准执行、Goal 完成报告、新建与切换会话。Plan 和 Goal 结束后未完成待办保持原样，恢复会话没有自动启动 Goal。

旧远端兼容采用响应 fixtures、真实组件展示和按会话去重测试；没有连接部署中的旧版远端服务进行端到端升级测试。桌面测试不调用真实模型，未进行跨模型 A/B 实验，不能据此宣称完成率或缺陷率改善。测试临时服务已停止。

## English

The change removes host quality obligations, final quality gates, ordinary todo continuation, and the independent Goal evaluator. Model declarations and observed execution results remain separate. Plan preapproval permissions, execution safety, persistence, cancellation, and explicit budgets remain enforced.

Root and desktop Go suites, focused core race tests, frontend suites and production build, lint, generated contracts, and cache guards passed. Two deterministic cache cases reported 89%, below the 90% advisory threshold. Browser and native Electron interactions were exercised with isolated local fixtures; legacy remote behavior was checked through fixtures rather than a deployed old server. No model quality improvement is claimed. No release, deployment, or merge was performed.
