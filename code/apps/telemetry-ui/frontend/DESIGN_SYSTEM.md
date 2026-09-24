# Signal Deck design system

Signal Deck is a dense operational interface, not a marketing surface. The
system prioritizes scan speed, stable layouts, and clear signal hierarchy.

## Color

All components consume semantic CSS variables from `src/design-system.css`.
Never place raw theme colors in route components.

| Token family | Purpose |
| --- | --- |
| `--ui-canvas`, `--ui-rail` | Application and navigation backgrounds |
| `--ui-surface*` | Panels, controls, and interactive hover states |
| `--ui-text*` | Primary, secondary, and low-emphasis text |
| `--ui-border*` | Default and emphasized separators |
| `--ui-accent*` | Amber navigation, focus, status, and primary actions |
| `--ui-orange*`, `--ui-danger*` | Warning and error semantics |
| `--ui-track` | Empty chart and progress tracks |

Light mode uses neutral white and cool-gray surfaces with near-black text. Dark
mode uses neutral near-black surfaces and white text. Yellow and orange are accents rather
than large decorative fills. The operating-system preference is the default;
the user selection is stored under `signal-deck-theme`.

## Type and density

- Use the sans stack for interface text and the mono stack for measurements,
  metadata, and compact labels.
- Page titles stay between 21 and 28 pixels; panel titles stay at 11 pixels.
- Prefer removing repeated copy over shrinking readable content.
- Use the `--ui-space-*` scale and `--ui-radius` for new components.

## Components

- Navigation uses one active amber edge and a low-contrast background.
- Panels use a single border; nested cards should not add shadows.
- Primary actions use amber. Destructive or failed states use danger tokens.
- Focusable custom controls require a visible amber `:focus-visible` outline.
- Status color must be accompanied by text; color alone is never the signal.

## Responsive behavior

The desktop rail becomes a compact horizontal bar below 760 pixels. Route
navigation and the theme control must remain visible. Multi-column metrics and
overview cards collapse to one column below 520 pixels.
