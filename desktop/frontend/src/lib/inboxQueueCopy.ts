const en = {
  currentRunning: "Current task is running", stop: "Stop current task", restore: "Append to composer", editingRow: "Editing",
  title: "Queued messages", after: "Runs in order after the current task", pause: "Pause queue", resume: "Resume queue", paused: "Queue paused · current task continues",
  edit: "Edit", editing: "Editing queued message", cancel: "Cancel", save: "Save", saved: "Saved in its original position", remove: "Delete", guide: "Guide current turn", retry: "Confirm retry", queueMore: "Queue actions",
  up: "Move up", down: "Move down", first: "Move to top", last: "Move to bottom", drag: "Reorder message", more: "More actions", expand: "Show all", collapse: "Collapse",
  preserve: "Updates this message in place; does not resend it", mainDraft: "Your original composer draft is preserved", shortcut: "Ctrl / ⌘ + Enter to save", inFlight: "Waiting for current turn to receive",
  recover: "Keep as new message draft", recovered: "Saved drafts", copy: "Copy draft", copied: "Draft copied", reopen: "Open draft", retained: "Your edits have been kept", reload: "Load latest version", loading: "Loading full message…",
  item_not_pending: "This message has started processing. Your changes were not saved.", item_missing: "This message is no longer in the queue. Your changes were kept.", content_changed: "This message was edited elsewhere. Your changes were kept.", order_changed: "The queue changed. Please adjust its order again.", anchor_missing: "The target message has left the queue. Please reorder again.", session_changed: "The session changed. Your edits were kept.", unsupported: "Update the service to edit or reorder this queue.", read_only: "This queue is read-only.", structured_input: "This message contains a structured invocation. Plain-text editing is unavailable.", unconfirmed: "Save result unconfirmed. Your edits were kept; no request was resent.", error: "The operation failed. Your edits were kept.", blocked: "Needs attention", uncertain: "Delivery unconfirmed · confirm before retrying",
};
const zh: typeof en = {
  currentRunning: "当前任务正在运行", stop: "停止当前任务", restore: "追加到输入框", editingRow: "正在编辑",
  title: "待处理队列", after: "当前任务结束后，按顺序执行", pause: "暂停队列", resume: "继续队列", paused: "队列已暂停 · 当前任务继续运行",
  edit: "编辑", editing: "正在编辑队列消息", cancel: "取消", save: "保存", saved: "已保存，原队列位置不变", remove: "删除", guide: "引导当前轮", retry: "确认重试", queueMore: "队列操作",
  up: "上移", down: "下移", first: "移到队首", last: "移到队尾", drag: "调整消息顺序", more: "更多操作", expand: "展开全部", collapse: "收起",
  preserve: "原位更新此消息，不会重新发送", mainDraft: "原输入草稿已保留", shortcut: "Ctrl / ⌘ + Enter 保存", inFlight: "等待当前轮接收",
  recover: "保留为新消息草稿", recovered: "已保留的草稿", copy: "复制草稿", copied: "草稿已复制", reopen: "打开草稿", retained: "修改内容已保留", reload: "读取最新版本", loading: "正在读取完整正文…",
  item_not_pending: "该消息已开始处理，本次修改未保存。", item_missing: "该消息已离开队列，修改内容已保留。", content_changed: "内容已在其他位置更新，你的修改已保留。", order_changed: "队列已变化，请重新调整顺序。", anchor_missing: "目标消息已离开队列，请重新调整。", session_changed: "会话已变化，修改内容已保留。", unsupported: "请更新服务以编辑和排序此队列。", read_only: "此队列为只读状态。", structured_input: "此消息包含结构化指令，暂不支持纯文本编辑。", unconfirmed: "保存结果尚未确认，修改内容已保留，未重复发送。", error: "操作失败，修改内容已保留。", blocked: "需要处理", uncertain: "执行结果未确认 · 重试前需确认",
};
const zhTW: typeof en = {
  currentRunning: "目前任務正在執行", stop: "停止目前任務", restore: "附加到輸入框", editingRow: "正在編輯",
  title: "待處理佇列", after: "目前任務結束後，依序執行", pause: "暫停佇列", resume: "繼續佇列", paused: "佇列已暫停 · 目前任務繼續執行",
  edit: "編輯", editing: "正在編輯佇列訊息", cancel: "取消", save: "儲存", saved: "已儲存，原佇列位置不變", remove: "刪除", guide: "引導目前回合", retry: "確認重試", queueMore: "佇列操作",
  up: "上移", down: "下移", first: "移到佇列開頭", last: "移到佇列結尾", drag: "調整訊息順序", more: "更多操作", expand: "展開全部", collapse: "收合",
  preserve: "原位更新此訊息，不會重新傳送", mainDraft: "原輸入草稿已保留", shortcut: "Ctrl / ⌘ + Enter 儲存", inFlight: "等待目前回合接收",
  recover: "保留為新訊息草稿", recovered: "已保留的草稿", copy: "複製草稿", copied: "草稿已複製", reopen: "開啟草稿", retained: "修改內容已保留", reload: "讀取最新版本", loading: "正在讀取完整內容…",
  item_not_pending: "此訊息已開始處理，本次修改未儲存。", item_missing: "此訊息已離開佇列，修改內容已保留。", content_changed: "內容已在其他位置更新，你的修改已保留。", order_changed: "佇列已變更，請重新調整順序。", anchor_missing: "目標訊息已離開佇列，請重新調整。", session_changed: "工作階段已變更，修改內容已保留。", unsupported: "請更新服務以編輯及排序此佇列。", read_only: "此佇列為唯讀狀態。", structured_input: "此訊息包含結構化指令，暫不支援純文字編輯。", unconfirmed: "儲存結果尚未確認，修改內容已保留，未重複傳送。", error: "操作失敗，修改內容已保留。", blocked: "需要處理", uncertain: "執行結果未確認 · 重試前需確認",
};
export function inboxQueueCopy(locale: string) { return locale === "zh-TW" ? zhTW : locale.startsWith("zh") ? zh : en; }
