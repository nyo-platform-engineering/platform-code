# UI conventions

See [README.md](./README.md) for frontend structure, form ownership, and commands.

## Tokens and Tailwind

`src/design-system.css` owns light/dark semantic tokens exposed through
Tailwind's `@theme inline`. Use utilities like `bg-canvas`, `bg-surface`,
`bg-surface-hover`, `text-ink`, `text-muted`, `text-dim`, `border-border`,
`border-border-strong`, `text-accent-strong`, and `text-danger`.
Use `font-mono` for timestamps, IDs, log messages, and numeric diagnostics.
Use the shared Button and Table components for consistent general controls.
No page-specific global CSS. Layout and responsive variants belong with JSX.
Only theme definitions and minimal base styles belong in CSS; runtime geometry
and chart colors may use inline styles.

## Pages and sections

Route directories mirror page directories. Logs sections live in
`pages/logs/sections`; trace sections live in `pages/traces/sections`.
`pages/_shared/telemetry` contains domain logic/UI reused by both.
`components` is reserved for domain-independent components, not page sections.

Telemetry views prioritize the query: compact page title/live controls, search and
Run on one row, service/time/status filters directly below, and optional attribute
conditions. Results follow a slim metrics strip. Charts start collapsed behind
Show charts; search help and JSON extraction use progressive disclosure. SQL
preview remains a secondary tool after the results. Avoid repeated headings,
large summary cards, and always-visible instructional paragraphs.
Expanded log charts use two columns from 800px; trace charts use three from
1100px. Attribute conditions use one row from 1200px and wrap below that.

Severity colors are consistent across charts, summary markers, log rows, and
correlated logs: info blue, warning amber, error red, fatal rose, debug cyan,
trace slate, and unspecified gray. Each theme has its own readable palette.
Latency uses teal/blue/violet/pink for P50/P90/P95/P99 plus distinct line dashes.
Keep text labels alongside colors.

Search is server-side and explicitly submitted. Search and multi-select state
use React Hook Form; committed values persist in the URL. Search anchors the
time range, resets pagination, and pauses automatic refresh.

## Interaction and accessibility

SearchableSelect has a labelled dialog, searchable combobox and listbox.
Arrow keys move options, Enter selects, Escape closes and restores focus,
and Tab exits through the footer. Multi-select remains open until dismissed.
Empty multi-selection means all values. Keep selected services visible during
refreshes and failures.

Logs show full, wrapping messages in bordered boxes; monospace applies to the log content box. The left sidebar selects the metadata and log/resource attributes displayed above each message, with searchable checkboxes and locally saved preferences. Attribute choices come from the bounded result page; selected keys remain available across pages. Log content is a display checkbox, enabled by default. Metadata and selector labels use the normal UI font. There is no record details disclosure. Clicking a record or its keyboard-accessible arrow opens a right-side drawer with boxed log content, record fields, log/resource attributes, and a related trace link. Selecting text or following a trace link does not open the drawer. The drawer retains the selected record during live refresh and restores focus when closed. On mobile the field selector stacks above the records.

Trace rows open a right-side modal drawer. Use a native button for keyboard
activation, hover/focus feedback, and a React Icons arrow. Selecting row text
must not open the drawer. The native dialog traps focus, closes with Escape or
backdrop click, and restores focus on dismissal. It fills the mobile viewport.

The trace flamegraph uses actual start offsets/durations, with children below
parents and concurrent spans on separate rows. Hover/focus shows details;
click pins a span for subtree zoom; clicking it again or blank timeline space clears selection. Pinned details stay stable while hovering other spans. Back retraces zoom history, Reset zoom returns to the full trace, and Clear selection unpins details. Escape inside the timeline clears selection first, then backs out of zoom, before closing the drawer. Missing parents and truncation are disclosed.
This is a trace timeline, not an aggregated CPU profile.

Correlated logs use monospace disclosure rows. Click or Enter/Space expands
the message and record fields underneath. Exact timestamps and IDs stay selectable.
Raw API JSON is a secondary disclosure. Long messages wrap in a bounded scroll area.

Charts support hover/tap, Arrow/Home/End/Escape navigation, series toggles,
and metric switching. Zero-volume buckets remain zero;
missing latency remains null. Partial minutes are visibly shaded.

## Verification

Before completing UI changes, run `pnpm test`, `pnpm build`, and
`pnpm format:check`. Use browser checks for interactive changes, including
keyboard access, both themes, mobile overflow, search/filter URL persistence,
and drawer behavior. Prefer role/name locators over styling classes.
