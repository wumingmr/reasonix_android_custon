# Sandbox settings — selected direction 3

final result: passed

## Visual truth and evidence

- Selected source: the third generated direction shown during the design review; it supersedes direction 1.
- Preview route: `/dev/settings-layout-preview.html?page=sandbox&platform=windows`.
- Visual comparison artifacts were kept in the local QA workspace and are not part of this repository.
- Implementation screenshot: `option3-final.png` in the local QA artifacts.
- Full-view comparison: `comparison-final.png`; focused environment-table comparison: `comparison-detail.png`. Both were opened and inspected with source and implementation together.
- Source pixels: 1312 × 1200, normalized to the intended 1032 × 944 viewport. Implementation: 1032 × 944 screenshot pixels and CSS viewport, density 1. No device/browser chrome.
- Matched state: light graphite theme, Windows preview fixtures, Git Bash selected and active, no pending shell reload, default workspace, no additional writable directories. The saved indicator follows a successful add/remove operation.
- Additional responsive captures: `option3-600.png`, `option3-390.png`, and `option3-390-fields.png`. Temporary viewport overrides were reset after verification.

## Findings and comparison history

1. **P2, first desktop comparison: excess vertical spacing.** `comparison-first.png` showed the directory entry reaching the bottom edge, unlike the target. Reduced table row padding, aligned the root label to its input, narrowed the directory label column, and removed unnecessary field spacing. `comparison-final.png` shows the complete form and empty state visible within the comparison viewport.
2. **P2, narrow-window review: fragmented executable paths.** At 600 px, three columns left too little room for Windows paths. Below a 660 px content width, each environment uses a name/status row followed by a full-width path. Post-fix 600 px and 390 px captures retain readable paths, accessible copy controls, and no horizontal overflow. The lower form was scrolled into view and checked at 390 px.
3. **P2, misleading save feedback and stale drafts.** Rule additions now clear only after a successful save and reject concurrent Enter submissions. Failed root saves retain input; refreshed authoritative roots replace stale values. Saved feedback uses a dedicated label and appears only after confirmed success. Deterministic tests cover both writable directories and the sibling permission-rule form.

No actionable P0/P1/P2 findings remain.

## Required fidelity surfaces

- **Typography:** existing system/PingFang fallback retained; 22 px page title, 16 px section headings, 13–14 px form and table text. Long paths wrap rather than being truncated. Native font rendering differs slightly from the raster mock; hierarchy and readability are preserved.
- **Spacing/layout:** current-shell strip, three-column diagnostic table, Windows boundary note, network status, and compact directory form match direction 3. Existing navigation, container radius and page routing remain intact. The final form sits roughly 20 px lower than the generated target; this is minor rhythm variation, with all controls visible at the matched viewport.
- **Colors/tokens:** existing warm graphite light surfaces, subtle border tokens, foreground/dim text and semantic success color retained. Dark mode inherits the existing theme tokens; no hardcoded light-only colors were introduced.
- **Assets/icons:** no raster assets are required by this settings screen. Existing Lucide library icons supply terminal, Git, information, copy and check affordances. Monochrome terminal icons intentionally preserve the application's icon system instead of introducing PowerShell branding from the generated mock.
- **Copy/content:** file-tool scope explicitly excludes Windows Shell commands; Windows network access is a factual unrestricted status. Git is labelled as a dependency. PowerShell keeps its existing precise runtime label. Scope and automatic saving are explained; manual reload remains a separate action. Successful save status is not fabricated on initial load.

## Verification

- 55 shell/settings interaction assertions passed, including Git Bash preference compatibility, pending-reload state, Linux/macOS repair behavior, path copying, failed/retried writes, duplicate Enter prevention, reloaded root state, empty-root reset and permission-rule sibling behavior.
- 99 settings snapshot assertions passed.
- Frontend and test TypeScript checks passed; changed-file ESLint, CSS syntax and application-layer checks passed.
- Browser checks: Git Bash selection updates active state; root Enter save and keyboard clear restore default workspace; directory add/remove updates effective directories; copy returns the full executable path; narrow-window inputs and actions remain reachable.
- Console: locale-module hot replacement produced transient context errors during editing. After a clean reload, no new console errors occurred during the final interaction and responsive checks.
- Browser data are fixtures, so this visual pass does not claim native desktop-shell validation. Shell detection and execution have separate native Windows regression coverage.

## Implementation checklist

- [x] Implement selected direction 3 in existing components.
- [x] Preserve Git Bash support and native PowerShell automatic preference.
- [x] Repair shared rule saving and authoritative-root synchronization.
- [x] Verify desktop and narrow layouts with source/render comparisons.
- [x] Keep the local preview available.

## Follow-up polish

P3 only: a later product-wide icon pass could introduce official runtime logos consistently. No functional work is blocked on it.

## Retrospective

The existing Reasonix skill already covers the reusable lessons applied here: repair shared draft ownership, test failure ordering, and inspect stylesheet specificity in the rendered UI. No additional skill rule was needed.
