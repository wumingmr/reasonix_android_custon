# Queue inline editing design QA / 队列行内编辑设计验收

Date: 2026-09-21. Scope: selected A interaction, hiding the main composer while editing.

## Visual target and evidence / 设计目标与证据

- Source: the locally generated 1586 × 992 comparison board for editing and saved states (not bundled in the repository).
- Source size: 1586 × 992 comparison board with editing and saved panes; not a full-product screenshot.
- Implementation: the local `?mock=guidance` preview built from this worktree.
- Browser uses production React components with a development-only in-memory queue fixture. No live user session or model was mutated.
- Artifacts: local QA workspace screenshots, not bundled in the repository.
- `desktop-edit.png`: 1280 × 720 CSS/pixels, density 1; running, three visible rows, second row editing, main composer hidden, stop above queue.
- `desktop-saved.png`: 1280 × 720 CSS/pixels, density 1; saved message still second, original main draft and input focus restored.
- `narrow-edit.png`: 900 × 700 CSS/pixels, density 1; restored unsaved edit, save and stop reachable.
- `console-check.json`: clean-load console check with development HMR warning recorded separately.

## Comparison history / 对照过程

The previous separate large editor is superseded by the user's selection of A and subsequent request to hide the main composer. Earlier evidence remains under the sibling `queue-implementation/` directory; it does not validate the new design.

The source board and both desktop implementation screenshots were opened together. The narrow screenshot was inspected independently. Editor text, chips, buttons and row relationships were legible; no separate crop was needed.

Normalization: compare each source pane's queue/editor/composer relationship with the corresponding implementation state, excluding presentation labels and simulated OS chrome. No raster resampling or whole-image pixel diff was applied. Existing sidebar, transcript, toolbar and theme remain the product context.

### Resolved findings

- **[P1] Detached editor:** the editor is now a child of the original keyed row. Its preview/actions are replaced by full text, references, Cancel and Save.
- **[P2] Excess controls:** removed duplicate exit and permanent preservation explanation; moved pause to the header menu. Paused state and Resume remain visible.
- **[P2] Automatic expansion:** editing preserves disclosure state. A restored draft outside the first two rows stays reachable without expanding every row.
- **[P2] False retained-edit state:** clean cancellation clears the temporary edit; only modified or conflicted text retains a recovery entry.
- **[P2] Lost focus:** saving/cancelling returns focus to the mounted main input without scrolling the transcript. Deferred focus work is cancelled on teardown.

Post-fix evidence: all three screenshots above, React regression tests and browser checks below. No actionable P0/P1/P2 visual findings remain.

## Required fidelity surfaces / 必查项目

| Surface | Result |
| --- | --- |
| Typography | Existing system/PingFang stack, 13 px body and 20.8 px editor line-height. Previews truncate; full text wraps. |
| Spacing/layout | Editor inside original row; main composer hidden while mounted. Expanded queue at 1280 × 720 is about 311 px high, ending at 680 px. At 900 × 700 Save ends at 605 px, Stop at 341 px; no horizontal document overflow. |
| Colors/tokens | Existing background, border, text and accent tokens. Queue background measured rgb(16, 17, 21). Warm accent marks selected row and Save. |
| Assets/icons | Existing product assets and Lucide controls; no raster illustration or fake OS chrome added. |
| Copy | English, simplified/traditional Chinese Cancel/Save labels. Exceptional states show recovery instructions; main draft is not duplicated in the editor. |

Intentional differences: existing toolbar and current-turn guidance remain. Stop appears above the queue while editing and returns to the existing composer control on exit. Surrounding conversation and simulated native window chrome are outside this change.

## Verification / 功能验证

- Full multiline body edits inside the original row; collapsed queue remains at two rows.
- Main composer computed display is none while its draft remains intact.
- Save preserves queue position and restores original main draft and focus.
- Esc cancels editing without stopping; modified text reopens exactly.
- Ctrl/Command+Enter saves; IME composition is excluded.
- Main attachment DOM identity survives editing, saving and cancellation (React test).
- Conflict leaves the editor open with retained text and an inline reason.
- When a message leaves the queue, an explicit recovery surface preserves input; Stop does not discard it (React test).
- Space → Up → Space reordered messages; pointer drag from third to first succeeded.
- Header-menu pause exposes Resume while the current task continues.
- Cold-load console check passed. A development HMR warning about a changed effect dependency array is recorded separately.
- Production build, test typecheck, queue regressions and feature CSS syntax/z-index checks passed; existing bundle budget preserved.

## Boundaries / 边界

Local implementation and browser verification only. No native installation, hosted CI, push, PR or release in this iteration. Earlier backend evidence is in `docs/INBOX_QUEUE_IMPLEMENTATION.zh-CN.md`; this UI iteration changes no Go code.

## Checklist

- [x] Original-row editor with only Cancel/Save in normal state.
- [x] Main draft, attachments and input focus restored.
- [x] Stop available; Esc exits editing only.
- [x] Conflict/departure recovery retained.
- [x] Desktop/narrow views, pointer/keyboard sorting, pause checked.
- [x] Existing theme and component ownership retained.

Follow-up polish: dnd-kit live drag announcements remain English; visible actions have localized accessible labels.

## Bug-check addendum / 行为复查（2026-09-21）

- Fixed late replies from an unmounted editor clearing/replacing a newer draft after returning to the same session. Owner identity and draft identity fence async completion.
- Fixed delayed target capture issuing a command after departure, and stale failed saves issuing reconciliation reads.
- Fixed Load latest reusing an expired selection; explicit reload now captures the current target and preserves previous text in recovery.
- Fixed Esc only working inside the textarea; it now exits from editor action buttons too, without stopping the task.
- Five deterministic lifecycle cases, existing editor/Composer integration tests, test typecheck and production build passed. No bundle budget was relaxed.
- Browser check at `http://127.0.0.1:5191/?mock=guidance`: focus Save → Esc restores the original main draft and input focus; task remains running. Fresh preview console has no warnings/errors. This check uses disposable preview data, not the user's original failed session or an installed package.

final result: passed
