# Browser over CDP

[简体中文](BROWSER_CDP.zh-CN.md)

The [desktop browser](DESKTOP_BROWSER.md) gives the agent a Chromium surface
the user shares, served by the Electron shell. CLI, `reasonix serve`, and
headless sessions have no shell behind them, so the same `browser_*` tools were
registered and then failed closed with no host to answer them.

This backend is the third implementation of the host-neutral `browser.Executor`
(after the Electron shell and the SSH broker): it drives an external Chrome over
the DevTools Protocol. The tools, their descriptions, and their schemas are
unchanged — only who answers them is new.

## Enabling it

```toml
[browser]
enabled = true
```

That is the whole ordinary path. On the first browser tool call Reasonix
launches a Chrome it owns, in a throwaway profile, and kills it with the
session. Nothing is launched at session start: a session that never touches a
browser never pays for one.

To drive a Chrome you already have running, start it with a debugging port and
point the endpoint at it:

```bash
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --remote-debugging-port=9222
```

```toml
[browser]
enabled = true
endpoint = "http://127.0.0.1:9222"
```

| Key | Meaning |
| --- | --- |
| `enabled` | Off by default. On means the tools get a real browser. |
| `endpoint` | A running Chrome's DevTools endpoint. Empty launches one. |
| `allow_remote_endpoint` | Permits a non-loopback endpoint. Off by default. |
| `chrome_path` | Browser binary. Empty searches the usual Chrome, Chromium, and Edge locations, then `REASONIX_CHROME` and `CHROME_PATH`. |
| `chrome_args` | Extra launch flags, for example a proxy. |
| `user_data_dir` | Profile for a launched browser. Empty uses a throwaway directory, so logins never outlive the session. |
| `headless` | Launch without a window. |

The desktop ignores this section: it already owns a browser, and a host that
brought one keeps it.

## What the agent can reach

Only tabs this backend opened. An attached Chrome usually holds the user's own
logged-in tabs, and `browser_tabs` never enumerates them, so the agent can
neither read nor drive a page the user did not hand it. `temporary` tabs get
their own browser context, which shares no cookies and is discarded with the
tab.

## The refusals this backend owns

A raw browser keeps no record of what an agent asked it to do, so this backend
owns the guarantees the Electron shell's ledger provides:

- **One use per `operationId`.** A replayed id is refused forever. The earlier
  attempt's effect stands, including an unknown one, so the model is told to
  re-read the page rather than try again.
- **`documentToken` binds refs to a document version.** Each `browser_snapshot`
  mints a fresh opaque token; navigation, page replacement, and a user take-over
  retire it. A write carrying a retired token is refused as stale instead of
  being replayed against a page the model has not seen.
- **Unknown outcomes are rare and honest.** A failure before anything reached
  the page is reported as not executed, which the model may plan around. Only a
  failure *after* input already landed — the second event of a click, a page
  script that threw halfway — reports an unknown outcome, which must never be
  retried.

### Take-over is an approximation here

The shell knows when a human touches the page because its guest preload sees
trusted input that the shell did not synthesise. CDP-dispatched input is
indistinguishable from a hand at the keyboard once it reaches the DOM, so this
backend marks a short window around each of its own dispatches and counts
trusted input outside that window as the user's. A human click that lands inside
the window is missed. The failure mode is a stale snapshot, never a silent
replay, because every write still carries a single-use `operationId`.

Refs and the take-over counter live in a per-document isolated world, so page
script can neither read the agent's refs nor forge the counter.

## Artifacts

Screenshots and downloads land in a private directory that is removed with the
session. Downloads keep their server-suggested name, sanitised so a name can
never escape that directory, and never overwrite a file already there.

## Security

A DevTools endpoint grants full control of that browser and of every file it can
read. A non-loopback `endpoint` is therefore refused unless
`allow_remote_endpoint` is set explicitly.

`browser_upload` may only read from the session's write roots — the workspace
and any additional directories — plus the executor's own artifact directory, so
a file the agent just downloaded stays attachable. Symlinks are resolved before
that check, so a link inside the workspace cannot point a file input at a key
outside it. Any other path is refused with a reason rather than handed to the
page: the page is untrusted, and a file input is an upload channel.

## Cache

Nothing here is provider-visible. The tools stay registry-only and reachable
through `use_capability`, so the request's tool array and the system-prompt
prefix are byte-identical whether or not a browser is attached. The guard is
`TestConfiguredBrowserBackendStaysOffTheProviderSurface` in `internal/boot`.

## Verifying a change

```bash
go test ./internal/browser/... ./internal/boot/
```

The unit tests run against a scripted DevTools server. The injected page helper
— the snapshot walker, the ref table, the take-over listeners — is only really
exercised against a real browser:

```bash
REASONIX_LIVE_CHROME=1 go test ./internal/browser/cdp -run '^TestLiveChrome$' -v -count=1
```

That test needs a Chrome install and stays skipped otherwise.

## Limits

- Chrome, Chromium, and Chromium-based Edge only.
- The agent's tabs are its own; there is no way to hand it one of the user's.
- `browser_select` drives a `<select>` through the DOM, because a native
  dropdown is rendered by the platform and cannot be steered with synthetic
  mouse events. Every other write uses real input events.
- A snapshot stops at 2000 nodes; pass `selector` to scope it to one subtree.
