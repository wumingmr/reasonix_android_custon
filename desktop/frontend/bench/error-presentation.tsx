import React from "react";
import { createRoot } from "react-dom/client";
import { ErrorMessage } from "../src/components/ErrorMessage";
import { LocaleProvider, preloadLocale, useI18n } from "../src/lib/i18n";
import { ToastProvider, useToast } from "../src/lib/toast";
import "../src/styles.css";

await Promise.all([preloadLocale("zh"), preloadLocale("zh-TW")]);
function Fixture() {
  const { setPref } = useI18n();
  const { showToast } = useToast();
  return <main style={{ maxWidth: 760, margin: "40px auto", padding: 24 }}>
    <h1>错误提示验证</h1>
    <nav style={{ display: "flex", gap: 12, margin: "24px 0" }}>
      <button onClick={() => setPref("zh")}>简体中文</button>
      <button onClick={() => setPref("zh-TW")}>繁體中文</button>
      <button onClick={() => setPref("en")}>English</button>
      <button onClick={() => showToast("HTTP 503: upstream unavailable; trace=browser-fixture", "error")}>测试错误弹窗</button>
    </nav>
    {[
      ["模型连接", "dial tcp: connection refused"],
      ["服务限流", "HTTP 429: too many requests"],
      ["历史会话", "读取会话失败：unrecognized storage record revision"],
      ["文件操作", "open /tmp/数据.txt: permission denied"],
      ["工具连接", "MCP server unavailable"],
      ["未知异常", "unknown library fault <script>not executable</script>"],
    ].map(([title, error]) => <section key={title} style={{ marginBottom: 24 }}>
      <h2 style={{ fontSize: 15 }}>{title}</h2><p role="alert"><ErrorMessage error={error} /></p>
    </section>)}
  </main>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><ToastProvider><Fixture /></ToastProvider></LocaleProvider>);
