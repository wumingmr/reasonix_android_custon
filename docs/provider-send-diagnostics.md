# Provider send protection (1.x)

Each provider HTTP attempt has a 300-second send-stage budget. It covers
connection acquisition and request writing, including a new stage after a
redirect or Transport retry. A successful `WroteRequest` hands protection to
the existing response-header timeout. An early final response also ends the
send budget. Response-body streaming retains its existing idle protection;
there is no 300-second limit on a healthy stream.

The timer cancels only the attempt context. It does not set socket deadlines
or close a multiplexed HTTP/2 connection. A send timeout means the body never
finished uploading, so the provider cannot have answered it: the request is
resent once, under the same single-resend allowance as a stale idle pooled
connection, after idle connections are closed. A `request_send_timeout` on the
resend fails the turn. Explicit user cancellation and the outer task deadline retain
priority and are never resent. Worst case before the turn fails is two send
budgets (600 seconds).

## Automatic protocol compatibility

For the built-in Chat Completions, Responses and Anthropic HTTP clients, a
typed HTTP/2 `PROTOCOL_ERROR` or `HTTP_1_1_REQUIRED` records a temporary
compatibility hint. `INTERNAL_ERROR` is also what servers and gateways send for
a generic upstream failure, so it records a hint only when a request that
started after the first one fails with it again; concurrent streams reset by
the same event count once. The failed request still returns its original
error. It is **not replayed**, because the peer may already have processed it,
and these three codes never consume the send-timeout/stale-connection resend
allowance or become stream retries. Other HTTP/2 codes keep their existing
handling. No tool execution or agent turn is restarted by this mechanism.

Only a subsequent new request tries HTTP/1.1. Hints are isolated by configured
provider identity, adapter, exact request endpoint and the actual resolved proxy
route. One concurrent request probes an unproven route; after response headers
arrive, subsequent requests may use HTTP/1.1. A transport failure on that route
disables compatibility until the hint expires, including failures while reading
the response body. The failure itself is reported and recovered by its real
cause: an EOF, reset or send timeout on HTTP/1.1 is owed the same resend or
stream recovery as on HTTP/2, which then uses normal protocol negotiation.
User cancellation does not mark the route incompatible. HTTP status errors
retain their existing handling.

Hints expire 10 minutes after the HTTP/2 failure that confirmed them and are
capped at 256 entries. Success does not extend them, so a route whose HTTP/2
path stays broken fails one request after each expiry before switching again. They survive provider reconstruction in the same process, but not an
application restart. Expiry returns to normal protocol negotiation; it does not
start a background request. Custom injected clients and credential tunnels keep
their existing transport contracts. TLS verification, proxy routing, request
bodies, model selection and prompt-cache inputs are unchanged. Other HTTP/2
streams continue on their original connection.

Request diagnostics expose the actual `httpProtocol` and the allowlisted
`protocolFallbackReason` when compatibility was selected. The latter is a hint
from an earlier request, not the current error code. Optional
`diagnostic/provider` events use schema version 2; required recovery records and
session schema versions are unchanged. Old observations simply omit the field,
and older session readers skip the optional event. No URL, proxy credential or
raw error text is added by compatibility tracking.

This is bounded compatibility handling, not proof that HTTP/2 or DeepSeek was
the original cause. It cannot guarantee that an upstream or network fault will
disappear over HTTP/1.1.

## Reading diagnostics

`FailureDiagnostic` retains its existing field structure. New safe kinds are
`request_send_timeout`, `transport_eof`, `transport_reset`, `transport_timeout`
and `transport_dns`. HTTP/2 errors retain `transport_protocol` and
`transportCode`. Unknown causes retain the generic presentation.

Optional `diagnostic/provider` schema version 2 adds `errorCode`, `errorPhase`,
`timeoutSource`, `sendTimeoutMs`, and `sendElapsedMs` to request observations.
Older readers skip this optional event; older records without the fields remain
valid. The send elapsed value describes the latest send stage, not total model
runtime. `requestBytes` is declared body length, **not bytes actually uploaded**.
Missing `writtenAt` means successful write completion was not observed, not
that no bytes were sent. Normal response-body EOF is not a transport failure.
Error prose, bodies, credentials, and URL query parameters are not diagnostic
fields. Records still pass through JSON-aware secret redaction.

Run `bash scripts/check-provider-diagnostic-compatibility.sh` to exercise both
directions of real framed-store compatibility with the v1.39.8/v1.39.9 source
readers, continued writes, and reopening. No installed user application or
session data is modified.

## Isolated network comparison

Build `go build -o provider-network-probe ./cmd/provider-network-probe`.
Set `REASONIX_PROBE_API_KEY` securely in the environment; do not put a key in a
command argument or record it in an exported shell transcript. The program
makes at most one application request per invocation, follows no redirects,
does not replay a request body, invokes no tools, and uses `max_tokens: 1`.
Each invocation can still incur input-token charges, especially the large case.
Run only the agreed matrix; the program does not loop or run it automatically.

Example for **one** case:

```sh
./provider-network-probe -endpoint https://api.deepseek.com/chat/completions \
  -model deepseek-v4-flash -protocol h2 -body-bytes 3248572 -timeout 360s \
  > probe-current-h2-large.json
```

Keep endpoint, model, timeout, and synthetic request size fixed when comparing:

| Axis | Cases |
| --- | --- |
| Network | Current path / alternative path, on the affected machine |
| Protocol | `-protocol h1` / `-protocol h2` |
| Declared JSON length | `-body-bytes 1024` / `-body-bytes 3248572` |
| Expect behavior | Default / `-expect-continue` as an independent experiment |

Start with the 8 network/protocol/size cases. Add the 8 Expect cases only if
needed and agreed. Repeated comparisons require another explicit call budget.
The synthetic padding is deterministic for the same model and size, but is not
a reproduction of the user's private conversation. The program exports only
safe observation fields, actual negotiated protocol when observed, status,
typed failure and a validated provider trace ID. If HTTP/2 cannot negotiate,
record that failure; do not silently treat an HTTP/1.1 result as HTTP/2 evidence.
Proxy settings follow Go's standard environment handling. Label the network
route externally without including proxy credentials.

An isolated local regression proves bounded client behavior. Only stable
comparisons on the affected machine can attribute the original failure to a
network, proxy, protocol, or service mechanism. Neither a large request nor an
`INTERNAL_ERROR` alone establishes a request-size limit or an Expect defect.

Release wording: “修复模型请求在发送阶段可能长时间等待的问题，并增强传输失败诊断。”

## 中文说明

每次模型 HTTP 请求的发送阶段最多等待 300 秒，覆盖等待连接、建立连接与
写入请求。重定向或 Transport 内部重发会重新启用保护。写入成功后由原有
响应头超时接管；提前收到最终响应也会解除发送计时器。正常 SSE 长流继续
使用原有空闲保护，不受发送预算限制。保护只取消本次请求，不关闭共享的
HTTP/2 连接。发送超时说明请求正文未上传完，服务端不可能已作答，因此与“空闲连接已失效”共用同一次
重发额度：关闭空闲连接后重发一次，重发仍超时才判定本轮失败（最坏 600 秒）。用户停止
和外层任务预算优先，且不会触发重发。

内置模型客户端遇到明确的 HTTP/2 `PROTOCOL_ERROR` 或 `HTTP_1_1_REQUIRED`
时，当前请求仍然失败，不重放请求、不重新执行工具，也不进入发送超时、空闲连接
重发或流重试。`INTERNAL_ERROR` 也是服务端和网关表示通用上游故障的错误码，
因此只有在第一次之后才开始的另一个请求再次遇到它，才记录兼容提示；同一事件
重置的并发流只算一次。其他 HTTP/2 错误码保持原有处理。只有后续新请求才尝试
HTTP/1.1；选择按供应商、协议适配器、请求端点和实际代理路径隔离，从确认时起
保留 10 分钟，成功不会续期。兼容请求发生传输失败时，暂停该路径的兼容尝试直到
过期，但该失败仍按真实原因（EOF、连接重置、发送超时）处理，照常享有原有的
重发或流恢复，重发走正常协议协商。用户取消不算协议失败。
正常共享 HTTP/2 流不受影响。诊断中的 `protocolFallbackReason` 表示此前触发
兼容选择的原因，`httpProtocol` 表示实际响应协议；这不等于已经确认现场根因。

必需恢复记录的字段结构不变。新增原因与阶段证据存入可选的
`diagnostic/provider` 第 2 版事件，旧客户端可以跳过。`requestBytes` 是声明的
正文长度，不是实际上传量；缺少 `writtenAt` 不能解释为完全没有上传。
诊断只记录白名单原因码，不保存完整错误文本、正文、密钥或 URL 查询参数。
运行 `bash scripts/check-provider-diagnostic-compatibility.sh` 可用 v1.39.8 和
v1.39.9 的实际源码验证双向读取、继续写入和重新打开。

现场工具通过 `REASONIX_PROBE_API_KEY` 环境变量读取密钥，每次启动最多发起
一次调用，禁用工具、重定向和正文重放，输出上限为 1 token。大请求仍可能
产生输入费用。先比较当前/替代网络、HTTP/1.1/HTTP/2、小/大合成请求共 8 组；
必要时在约定调用预算内独立比较 `-expect-continue`，不自动循环。每组固定
端点、模型、正文大小和超时，实际协商协议以输出为准。合成正文不包含用户
会话，也不能证明已复现现场故障。只有用户机器上的稳定对照结果才可用于
归因；请求大小和 HTTP/2 错误本身不能证明服务端大小限制或 Expect 缺陷。

打包后可运行 `node desktop/packaging/provider-diagnostic-smoke.mjs /path/Reasonix.app`
验证停止操作、历史超时详情、实时 EOF、重启与诊断导出。该测试只访问本地
合成服务，并使用隔离数据目录；发送保护本身由缩短预算的真实 HTTP/1.1、
HTTP/2 单元回归覆盖。设置 `REASONIX_SMOKE_ARTIFACTS` 可指定截图与诊断输出目录。
