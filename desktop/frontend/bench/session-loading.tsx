import React from "react";
import { createRoot } from "react-dom/client";
import { flushSync } from "react-dom";
import { LocaleProvider } from "../src/lib/i18n";
import { SessionLoadingFixture, type LoadingFixtureInput } from "../src/test-support/sessionLoadingFixture";
import "../src/styles.css";
import "../src/components/ChatTranscript.css";

const root = createRoot(document.getElementById("root")!);
const paintLoading = (input: LoadingFixtureInput) => flushSync(() => root.render(
  <LocaleProvider><div className="app" style={{ height: "100vh", display: "flex", flexDirection: "column" }}>
    <SessionLoadingFixture {...input} />
  </div></LocaleProvider>,
));
Object.assign(window, { paintLoading });
