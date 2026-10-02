import type {
  HistoryContentRef,
  HistoryEntry,
  HistoryMessage,
  HistorySlice,
  HistorySliceRequest,
} from "./types";

export function mockHistorySlice(
  tabID: string,
  messages: HistoryMessage[],
  req: HistorySliceRequest,
  benchMock: boolean,
): HistorySlice {
  const turnsOf: number[] = [];
  let turn = 0;
  for (const message of messages) {
    if (message.role === "user") turn += 1;
    turnsOf.push(turn);
  }
  let before = messages.length;
  if (req.cursor) {
    try {
      const decoded = JSON.parse(atob(req.cursor)) as { before?: number };
      if (typeof decoded.before === "number" && decoded.before >= 0 && decoded.before < before) before = decoded.before;
    } catch { /* unknown cursor: serve the latest page */ }
  }
  const empty: HistorySlice = { entries: [], nextCursor: "", hasOlder: false, totalTurns: turn, startTurn: 0, endTurn: 0, stale: false, revision: 0 };
  if (before <= 0 || messages.length === 0) return empty;
  const windowedTranscriptContract = messages.length === 2_000
    && messages.some((message) => message.content?.includes("Windowed turn 1000"));
  const turns = windowedTranscriptContract
    ? Math.max(120, Math.floor(req.turns || 0))
    : Math.max(1, Math.floor(req.turns || 12));
  const newestTurn = turnsOf[before - 1];
  const oldestTurn = newestTurn > 0 ? Math.max(newestTurn - turns + 1, 1) : 0;
  let lo = 0;
  if (oldestTurn > 1) {
    lo = before;
    for (let index = 0; index < before; index += 1) {
      if (turnsOf[index] >= oldestTurn) {
        lo = index;
        break;
      }
    }
  }
  // The geometry-contract fixture is deliberately a single completed turn.
  // Serve all of it in the first slice so its first-visit traversal measures
  // row geometry, not an unrelated history-prepend transaction. Prepend is
  // covered by the dedicated history pagination scenario below.
  const geometryContract = messages.some((message) => message.content?.includes("Geometry contract fixture complete."));
  // The browser selection contract spans 20+ turns in the 3.2k-message
  // tool-dense fixture. A production request is turn-budgeted, but the dev
  // mock's generic 120-entry fallback would expose barely two such turns and
  // make the test depend on a long chain of incidental prepend timings.
  const toolDenseSelectionContract = messages.length > 1_500
    && messages.some((message) => message.content?.startsWith("bench turn 38:"));
  const maxEntries = geometryContract
    ? messages.length
    : toolDenseSelectionContract
      ? Math.max(1_000, Math.floor(req.entries || 0))
      : windowedTranscriptContract
        ? Math.max(240, Math.floor(req.entries || 0))
        : Math.max(1, Math.floor(req.entries || 120));
  if (before - lo > maxEntries) lo = before - maxEntries;
  const entries = messages.slice(lo, before).map((message, index) => {
    const entryId = `smock-${tabID}:r0:m${lo + index}:o0`;
    const content = message.content ?? "";
    const reasoning = message.reasoning ?? "";
    const lazyContent = benchMock && content.includes("ASYNC LAYOUT EXPANSION COMPLETE");
    const stormContent = benchMock && content.includes("BENCH STORM HYDRATION RESOLVED");
    const stormReasoning = benchMock && reasoning.includes("BENCH STORM HYDRATION RESOLVED");
    const refs: HistoryEntry["refs"] = [];
    if (lazyContent || stormContent) {
      refs.push({ entryId, field: "content", size: content.length, chunks: 1, revision: 0, revKnown: false, digest: "" });
    }
    if (stormReasoning) {
      refs.push({ entryId, field: "reasoning", size: reasoning.length, chunks: 1, revision: 0, revKnown: false, digest: "" });
    }
    return {
      entryId,
      turn: turnsOf[lo + index],
      order: lo + index,
      message: refs.length > 0 ? {
        ...message,
        ...(lazyContent || stormContent ? { content: content.slice(0, 4 * 1024) } : {}),
        ...(stormReasoning ? { reasoning: reasoning.slice(0, 4 * 1024) } : {}),
      } : message,
      refs,
    };
  });
  const visibleTurns = entries.map((entry) => entry.turn).filter((value) => value > 0);
  return {
    entries,
    nextCursor: lo > 0 ? btoa(JSON.stringify({ v: 1, before: lo })) : "",
    hasOlder: lo > 0,
    totalTurns: turn,
    startTurn: visibleTurns.length > 0 ? Math.min(...visibleTurns) : 0,
    endTurn: visibleTurns.length > 0 ? Math.max(...visibleTurns) : 0,
    stale: false,
    revision: 0,
  };
}

export function mockHistoryContentField(message: HistoryMessage, ref: HistoryContentRef): string {
  switch (ref.field) {
    case "content": return message.content ?? "";
    case "reasoning": return message.reasoning ?? "";
    case "submitText": return message.submitText ?? "";
    case "detail": return message.detail ?? "";
    case "code": return message.code ?? "";
    case "summary": return message.summary ?? "";
    case "archive": return message.archive ?? "";
    case "toolResultError": return message.toolResultError ?? "";
    case "toolArguments": return (message.toolCalls ?? []).find((toolCall) => toolCall.id === ref.toolCallId)?.arguments ?? "";
    case "toolSubject": return (message.toolCalls ?? []).find((toolCall) => toolCall.id === ref.toolCallId)?.subject ?? "";
    case "toolSummary": return (message.toolCalls ?? []).find((toolCall) => toolCall.id === ref.toolCallId)?.summary ?? "";
    case "toolDiff": return (message.toolCalls ?? []).find((toolCall) => toolCall.id === ref.toolCallId)?.diff ?? "";
    default: return "";
  }
}

// The dev mock's topic history: one fixture per mock topic, addressed the way the
// transcript addresses it. The epoch is a parameter so a session's timestamps stay
// stable across every read of the same mock app.
function mockLongTranscriptHistory(t0: number): HistoryMessage[] {
  const out: HistoryMessage[] = [];
  for (let i = 1; i <= 18; i++) {
    out.push({
      role: "user",
      content: `第 ${i} 轮：检查聊天滚动定位，切换会话后应该自动停在最新消息底部。`,
      createdAt: t0 - (19 - i) * 15 * 60_000,
    });
    if (i === 4) {
      out.push({ role: "phase", content: "复现切换会话后的滚动位置" });
    }
    if (i === 8) {
      const toolID = "mock-scroll-layout-check";
      out.push({
        role: "assistant",
        content: "我会先读取滚动容器尺寸，再确认是否存在动态高度变化导致的底部偏移。",
        reasoning: "旧实现只重置 stick 标志，没有主动等待布局稳定；AskCard、Approval、Todo 这类卡片可能在下一帧改变高度。",
        toolCalls: [{ id: toolID, name: "bash", arguments: JSON.stringify({ command: "npm run check:css && pnpm typecheck" }) }],
      });
      out.push({
        role: "tool",
        toolCallId: toolID,
        toolName: "bash",
        content: "CSS syntax check passed\nz-index token check passed\ntsc --noEmit passed\n",
      });
      continue;
    }
    if (i === 13) {
      out.push({ role: "notice", level: "info", content: "模拟提示：用户向上查看历史后，右下角应出现跳到底部按钮。" });
    }
    out.push({
      role: "assistant",
      content: [
        `第 ${i} 轮结果：当前滚动契约会在切换会话或 reveal 信号到达后执行强制贴底。`,
        "它会先立即设置 scrollTop 到 scrollHeight，再连续几个 animation frame 复查，避免动态内容把底部再次推走。",
        "如果用户主动向上滚动，普通 streaming 不会强行拉回；只有点击跳到底部按钮或显式切换会话才会重新贴底。",
      ].join("\n\n"),
    });
  }
  out.push({
    role: "compaction",
    content: "",
    trigger: "manual",
    messages: 36,
    summary: "Mock 长会话用于验证桌面端 Transcript 自动贴底、多帧布局修正和跳到底部按钮。",
    archive: "mock-scroll-preview",
  });
  out.push({
    role: "assistant",
    content: "最终状态：这条消息应该位于真实底部。向上滚动后，右下角会显示跳到底部按钮；点击按钮后应回到这里。",
  });
  return out;
}

export function mockTopicHistory(t0: number, topicId: string): HistoryMessage[] {
  switch (topicId) {
    case "topic_product":
      return [
        {
          role: "user",
          content: [
            "[[reasonix-im]]",
            "provider=lark",
            "label=Feishu / Lark",
            "sender=ou_mock_user_001",
            "chat=p2p 会话",
            "[[/reasonix-im]]",
            "你可以做什么",
          ].join("\n"),
        },
        {
          role: "assistant",
          content: "这是 Global 范围下的 IM 会话。我可以先处理不依赖项目文件的问答、计划和信息整理；需要进入项目时，再由桌面端显式绑定或迁移到项目话题。",
        },
      ];
    case "topic_ai":
      return [
        {
          role: "user",
          content: [
            "[[reasonix-im]]",
            "provider=weixin",
            "label=微信",
            "sender=wxid_mock_user_001",
            "chat=单聊",
            "[[/reasonix-im]]",
            "帮我整理一下今天要做的事",
          ].join("\n"),
        },
        {
          role: "assistant",
          content: "可以。我会先在 Global 范围里整理任务清单；如果某条任务需要读取项目文件，再切到你授权的项目话题处理。",
        },
      ];
    case "topic_dev_standard":
      return mockLongTranscriptHistory(t0);
    case "topic_p3b_pd":
      return [
        { role: "user", content: "把 p3b P&D 的范围和风险重新整理成可执行计划。" },
        { role: "phase", content: "分析需求范围" },
      ];
    case "topic_p3a_pd":
      return [
        { role: "user", content: "复盘 p3a 的技术方案，先不要写文件，先说明你的判断。" },
      ];
    case "topic_hotfix":
      return [
        { role: "user", content: "检查 post-p3-hotfix 的回归风险，重点看最近的 shell 输出和 git 改动。" },
        { role: "assistant", content: "", reasoning: "我先定位最近一次 hotfix 的上下文，然后用只读命令检查状态；左侧保持“思考中”，工具细节在这里展开。" },
      ];
    case "topic_sys_coord":
      return [
        { role: "user", content: "准备执行 joyquant-sys 的同步脚本，但需要我确认后再运行。" },
        { role: "assistant", content: "", reasoning: "这个动作会运行脚本并可能刷新本地缓存，所以需要先等用户确认。" },
      ];
    case "topic_sys_standard":
      return [
        { role: "user", content: "继续制定 SYS 项目开发规范，先停在当前检查点。" },
        { role: "assistant", content: "已暂停在规范整理阶段。当前保留了目录约定、分支策略和待确认的发布检查项；继续时可以从这里恢复。" },
        { role: "notice", level: "info", content: "会话已暂停：未继续执行命令，等待用户恢复或切换任务。" },
      ];
    case "topic_sys_exception":
      return [
        { role: "user", content: "演练异常处理流程，看看失败时界面怎么提示。" },
        { role: "assistant", content: "我尝试校验恢复脚本时遇到异常，已停止继续执行。" },
        { role: "notice", level: "warn", content: "运行异常：恢复脚本缺少必要环境变量 JOYQUANT_SYS_TOKEN。请补齐配置后重试。" },
      ];
    default:
      return [];
  }
}
