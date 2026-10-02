# User-visible errors

Desktop errors use the shared `ErrorMessage` component and `presentError`
classifier. Summaries follow the selected English, Simplified Chinese or
Traditional Chinese UI language. An explicit language preference takes priority;
automatic mode follows the system language. The UI language is independent of
the language used by the backend or model.

- Classify known errors using stable codes and explicitly labelled HTTP statuses
  before narrow legacy text matches. Give unknown failures a neutral summary.
- In English mode, an unclassified Chinese exception gets an English fallback;
  the Chinese original remains available in details.
- Render original diagnostics as escaped, selectable text in expandable details.
  Inspecting toast details stops automatic dismissal; manual dismissal remains available.
- Preserve original exceptions and codes for control flow, persistence and model
  input. Never make recovery or permission decisions from translated text.
- File contents, terminal output and model-generated content are not application
  error copy and remain unchanged.
- Provider failures return without automatic transport retries. A user resend is
  a new request. Readers for historical recovery records remain supported.

New error surfaces should reuse the shared component. Existing dedicated error
panels may reuse the classifier while keeping their diagnostic detail view.
Known business errors should supply stable codes and localized messages.

`desktop/frontend/src/__tests__/error-presentation.test.tsx` covers classification,
all three languages, unchanged exceptions, escaping, live language switching and
toast lifetime. The development fixture at `/bench/error-presentation.html`
supports real-browser interaction and layout checks.

See [the Chinese version](ERROR_PRESENTATION.zh-CN.md).
