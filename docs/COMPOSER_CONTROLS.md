# Desktop composer controls

[简体中文](COMPOSER_CONTROLS.zh-CN.md)

## Messages while a turn is running

You can continue typing while Reasonix works. Add the message to the queue to
run later, or choose **Guide current turn** to supply it to the active turn at
its next supported boundary. Guidance is not an immediate interruption of a
model request. If the turn finishes before guidance is accepted, the inbox
retains the message for a later turn.

The current input is saved automatically. A rejected submission keeps the text
and restores editing automatically when its status can be confirmed. If a reply
is lost, Reasonix checks the existing submission instead of sending it again.
An unresolved result shows one **Retry** action to check recovery; it does not
resend the message. Input remains protected until the result is known.

There are no saved/local version choices or recoverable input-copy lists. A
concurrent save refreshes its revision once while keeping the current text.
Queue-item editing retains its separate conflict handling.

## Controls

Use the **+** menu to attach content or enable Plan or Goal mode. Reasonix uses
one standard execution behavior; there is no delivery-quality selector or
mode chip. Permission remains a separate Read only/Workspace write/Full access menu. Model and
reasoning effort have independent selectors; unsupported models hide effort.
The status bar no longer repeats the model name. Its turn cost uses two decimal
places; detailed cost values retain their existing precision.

An active Goal can be edited from the **+** menu without replacing its identity
or resetting admitted rounds. The optional maximum-round field is blank for
unlimited execution. The same menu remains available during an automatic Goal
round so the user can pause immediately.

The context ring opens usage details. **Turn time** excludes waits on the user,
whether an approval, an answer, or an MCP interaction, and stops at the
controller's completion timestamp. Retry time remains part of the turn. Turn
tokens and throughput remain available during waits, retries, and after
completion. In-flight tokens are estimated from character density rather than a
flat four characters per token: ASCII prices at roughly four characters per
token and CJK at roughly 1.3. The ring's throughput and token rows are
turn-scoped, counting the turn's cumulative output and carrying the estimate cue
while streaming; the status bar's throughput item covers the most recent request
and its turn-token item is the turn's prompt-plus-output total. **Session time**
is the separately reported session aggregate. Starting a new turn resets turn
metrics. These live metrics are not a persisted historical report. Completed
metrics are settled once per turn; later background-job updates do not replace
them.

The composer defaults to 140px and preserves manual resizing. Running work
uses a theme-aware perimeter trace; reduced motion uses a static outline. Above
the input, a run strip names the current state and, while a turn is live, pins
its readings to the right: the turn clock, the running token total, and
throughput. Colour and position do the separating, not punctuation. The clock
reads first, so the strip answers "is this stuck?"; only the token reading
carries the estimate cue, since the clock is exact and throughput derives from
the reading. A narrow strip spends the state word's width first, never cuts a
reading mid-number, and drops the rate whole below its threshold. Throughput
also appears only while the model is emitting, so a rate frozen by a wait is
never shown as a current speed. The strip's live region still announces the
stable state text alone. Approval, answer, and retry notices remain visible.

The bottom status bar combines workspace and branch into one item: it shows
the branch name, with both workspace path and branch in the tooltip. Non-Git
workspaces show the workspace name. Older item lists are deduplicated. For a
model-only legacy configuration, migration retains only workspace and branch. On the
first upgrade, status labels default to icons; subsequent manual text/icon
choices are preserved. An older binary that rewrites preferences can remove
the upgrade marker, causing the icon default to apply again on re-upgrade.
