import { useLayoutEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { Markdown } from "../src/components/Markdown";
import { LocaleProvider } from "../src/lib/i18n";
import { applyTheme, THEME_STYLES, type Theme, type ThemeStyle } from "../src/lib/theme";
import { applyThemePack, applyThemeScene, type ThemeScene } from "../src/lib/themePack";
import "../src/styles.css";

const source = '// 主题切换时，代码保持可读\nfunction greet(name: string) {\n  const count = 42;\n  return `你好，${name}! (${count})`;\n}\n';
const chunks = [source.slice(0, source.indexOf("  const")), source.slice(0, source.indexOf("  return")), source];
function Fixture() {
  const [mode, setMode] = useState<Theme>("light");
  const [style, setStyle] = useState<ThemeStyle>("graphite");
  const [pack, setPack] = useState("base");
  const [scene, setScene] = useState<ThemeScene>("task");
  const [step, setStep] = useState(0);
  useLayoutEffect(() => {
    applyThemePack(null);
    applyTheme(mode, style, { persist: false });
    applyThemeScene(scene);
    if (pack === "base") return;
    const tokens = pack === "inverted" ? { bg: "#ffffff", bgSoft: "#fafafa", fg: "#ffffff" }
      : pack === "midtone" ? { bg: "#3fcd1c", bgSoft: "#cd1ce4", fg: "#3fcd1c" }
      : { bg: "#203040", bgSoft: "#ffffff26", fg: "#ffffff55" };
    applyThemePack({ id: "code-preview", name: "Code preview", baseStyle: style, builtin: false, active: true,
      hasBackground: pack === "wallpaper", tokens: { light: tokens, dark: tokens }, recipes: {},
      backgroundUrl: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6X9sAAAAASUVORK5CYII=",
      background: { focusX: .5, focusY: .5, homeOpacity: 1, taskOpacity: 1, overlayStrength: 0, paneOpacity: .1 },
    }, { preview: true });
  }, [mode, style, pack, scene]);
  const growing = '```typescript\n' + chunks[Math.min(step, 2)] + (step >= 3 ? '```' : '');
  return <div className="app" style={{ display: "block" }}><main className="chat-transcript" style={{ height: "100vh", overflow: "auto", background: "var(--bg)", color: "var(--fg)", padding: "20px clamp(16px, 5vw, 64px)" }}>
    <p>代码块主题验证 · 使用实际 Markdown 组件与示例代码</p>
    <div style={{ display: "flex", gap: 12, flexWrap: "wrap" }}>
      <label>明暗 <select aria-label="Mode" value={mode} onChange={event => setMode(event.target.value as Theme)}>{["light", "dark", "auto"].map(value => <option key={value}>{value}</option>)}</select></label>
      <label>主题 <select aria-label="Style" value={style} onChange={event => setStyle(event.target.value as ThemeStyle)}>{THEME_STYLES.map(value => <option key={value}>{value}</option>)}</select></label>
      <label>自定义背景 <select aria-label="Palette" value={pack} onChange={event => setPack(event.target.value)}>{["base", "inverted", "midtone", "translucent", "wallpaper"].map(value => <option key={value}>{value}</option>)}</select></label>
      <label>场景 <select aria-label="Scene" value={scene} onChange={event => setScene(event.target.value as ThemeScene)}>{["home", "task"].map(value => <option key={value}>{value}</option>)}</select></label>
    </div>
    <section aria-label="Streaming code">
      <h3>流式生成</h3>
      <button type="button" onClick={() => setStep(value => Math.min(3, value + 1))} disabled={step >= 3}>追加代码</button>{" "}
      <button type="button" onClick={() => setStep(0)}>重新生成</button>
      <Markdown text={growing} streaming={step < 3} />
    </section>
    <section aria-label="Completed code">
      <Markdown text={'### 完整代码\n\n```typescript\n' + source + '```\n\n```python\n# 多行字符串与注释\nmessage = """first\nsecond"""\nprint(message, 42)\n```\n\n```\n没有指定语言，保留原文。\n```'} />
    </section>
  </main></div>;
}
createRoot(document.getElementById("root")!).render(<LocaleProvider><Fixture /></LocaleProvider>);
