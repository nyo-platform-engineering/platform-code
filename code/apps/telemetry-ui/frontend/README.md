# Telemetry frontend

React + TypeScript, TanStack file routes, Tailwind CSS, React Hook Form, React Icons.

## Structure

```text
src/
  routes/
    __root.tsx                # App layout and not-found wiring
    index.tsx                 # /
    logs/index.tsx            # /logs
    traces/index.tsx          # /traces
  pages/
    index.tsx                 # Overview
    not-found.tsx
    logs/
      index.tsx               # Logs page composition
      sections/               # Log table, summary, correlated log disclosures
    traces/
      index.tsx               # Traces page composition
      sections/               # Trace/span tables, summary, drawer, flamegraph
      lib/trace-layout.ts     # Pure trace hierarchy/layout
    _shared/telemetry/        # Domain-specific workspace, queries, filters, charts
  layouts/                   # App shell, page header, metadata provider
  components/                # General UI only: Button, Table, SearchableSelect
  design-system.css          # Theme tokens / Tailwind semantic color mapping
  styles.css                 # Tailwind import and minimal global base styles
```

Routes are thin and import the page at the corresponding path. Put page-specific
sections and logic inside that page folder. Use `pages/_shared/telemetry` only
for code genuinely shared by both telemetry pages. Shared code composes page
sections through render props and does not import concrete pages.
The trace drawer reuses the log domain's correlated-log section.

## Styling

Use Tailwind utilities directly on components. Prefer semantic tokens such as
`bg-surface`, `text-muted`, `border-border`, and `text-danger` so both themes
stay consistent. Do not introduce global page selectors or feature CSS files.
Inline styles are reserved for runtime geometry (span position, popover bounds)
and data-driven series colors. Reusable Button and Table components centralize
general UI defaults; Tailwind Merge handles caller overrides.
Prettier sorts utility classes through its Tailwind plugin.

## Forms and query state

Search uses React Hook Form validation and explicit submission. Custom selects
use Controller with URL-backed values; selection commits immediately.
Popover visibility, chart hover, and drawer tabs remain local UI state.
Keep server-side search semantics, cancellation, query bounds, fixed pagination
windows, and trace-detail context in the shared API/hooks rather than in pages.
Search text is never used to filter only the currently loaded rows.

## Commands

From this directory:

- `pnpm dev` — Vite development server
- `pnpm build` — production build and TypeScript check
- `pnpm test` — trace layout and search validation tests
- `pnpm format` / `pnpm format:check` — formatting and Tailwind class ordering

From `code/apps`, `task dev:test` includes frontend checks and Go tests/vet.
Keep generated `src/routeTree.gen.ts` untouched; the router plugin owns it.
