import { useRef, useState } from "react";
import { useProviderT as useT } from "../lib/providerSettingsLocale";
import { Tooltip } from "./Tooltip";

export function RuleList({
  list,
  rules,
  busy,
  onAdd,
  onRemove,
}: {
  list: string;
  rules: string[];
  busy: boolean;
  onAdd: (rule: string) => Promise<boolean>;
  onRemove: (rule: string) => Promise<unknown>;
}) {
  const t = useT();
  const [draft, setDraft] = useState("");
  const adding = useRef(false);
  const add = async () => {
    const r = draft.trim();
    if (!r || busy || adding.current) return;
    adding.current = true;
    try {
      if (await onAdd(r)) setDraft(current => current.trim() === r ? "" : current);
    } finally {
      adding.current = false;
    }
  };
  return (
    <div className="set-rules">
      <div className="set-rules__head">
        <div className="set-rules__label">{ruleListLabel(list, t)}</div>
        {ruleListHint(list, t) && <div className="set-rules__hint">{ruleListHint(list, t)}</div>}
      </div>
      <div className="set-rules__chips">
        {rules.length === 0 && <span className="mem-empty">{t(list === "allow_write" ? "settings.noAdditionalDirectories" : "common.none")}</span>}
        {rules.map((r) => (
          <span className="set-rule" key={r}>
            <span className="set-rule__text" title={r}>{r}</span>
            <Tooltip label={t("common.delete")}>
              <button className="set-rule__x" aria-label={`${t("common.delete")} ${r}`} disabled={busy} onClick={() => void onRemove(r)}>
                ✕
              </button>
            </Tooltip>
          </span>
        ))}
      </div>
      <div className="set-rules__add">
        <input
          className="mem-input"
          placeholder={list === "allow_write" ? t("settings.addWriteDirectory") : t("settings.addRule", { list })}
          aria-label={ruleListLabel(list, t)}
          value={draft}
          disabled={busy}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing) { e.preventDefault(); void add(); }
          }}
        />
        <button className="btn btn--small" disabled={busy || !draft.trim()} onClick={() => void add()}>
          {t("common.add")}
        </button>
      </div>
    </div>
  );
}

function ruleListLabel(list: string, t: ReturnType<typeof useT>): string {
  switch (list) {
    case "deny":
      return t("settings.ruleDeny");
    case "ask":
      return t("settings.ruleAsk");
    case "allow":
      return t("settings.ruleAllow");
    case "allow_write":
      return t("settings.ruleAllowWrite");
    default:
      return list;
  }
}

function ruleListHint(list: string, t: ReturnType<typeof useT>): string {
  switch (list) {
    case "deny":
      return t("settings.ruleDenyHint");
    case "ask":
      return t("settings.ruleAskHint");
    case "allow":
      return t("settings.ruleAllowHint");
    default:
      return "";
  }
}
