# KVM Center Style Guide

The visual design system for KVM Center, inspired by the KVM and QEMU logos.
Every new UI surface — modals, pages, standalone portals — should pull its
tokens from this guide so the product feels consistent end-to-end.

## Design principles

- **Production-grade, not playful.** Dense information, clear hierarchy, no unnecessary chrome.
- **Dark-first.** Operators spend hours here. Backgrounds are slate-black, text is warm neutral grey, accents are used sparingly.
- **QEMU amber-orange is reserved for action and identity.** Brand elements, primary buttons, the active nav item — nothing else.
- **Status colors are semantic.** Green means the thing is working. Red means it isn't. Yellow means caution. Never use them for decoration.
- **Typography uses a single family** (system UI / Inter) with three sizes and three weights. Avoid custom font files.

---

## Color tokens

### Brand

| Token | Hex | Usage |
|---|---|---|
| `--accent` | `#E3571E` | Primary action, brand, logo fill, active nav border, progress bars on CPU charts, focus rings |
| `--accent-hover` | `#FF8A3C` | Hover state on primary buttons, top stop of orange gradients |
| `--accent-dim` | `#C04512` | Pressed/darker stop on orange gradients, primary button border |
| `--accent-subtle` | `#2D1810` | Large translucent backgrounds for selected/active rows, badge backgrounds |
| `--accent-badge` | `#3D2510` | Badge backgrounds paired with `--accent-hover` text |

### Surfaces

| Token | Hex | Usage |
|---|---|---|
| `--bg` | `#0B0E13` | Page body background |
| `--bg-topbar` | `#13161D` | Top navigation bar |
| `--bg-sidebar` | `#0F1217` | Left sidebar |
| `--bg-input` | `#0D1117` | Text inputs, selects, code blocks, nested surfaces |
| `--surface` | `#161B22` | Cards, panels, list rows (resting state) |
| `--surface-hover` | `#1C2128` | Card hover, elevated surface |
| `--surface-active` | `#21262D` | Pressed state, active row background, secondary button |

### Borders

| Token | Hex | Usage |
|---|---|---|
| `--border` | `#30363D` | Default border on cards, inputs, dividers |
| `--border-subtle` | `#21262D` | Hairline dividers inside panels, table row separators |

### Text

| Token | Hex | Usage |
|---|---|---|
| `--text` | `#F0F6FC` | Headings, primary values, VM names |
| `--text-body` | `#C9D1D9` | Body copy, form input text, table cells |
| `--text-muted` | `#8B949E` | Secondary info (timestamps, meta strips, labels) |
| `--text-faded` | `#6E7681` | Tertiary (column headings, help text) |
| `--text-disabled` | `#484F58` | Disabled labels, placeholder hints |

### Status

| Token | Hex | Usage |
|---|---|---|
| `--success` | `#3FB950` | "Running", "Connected", Up pill, success toast, progress bars under 65% |
| `--success-bg` | `#1E3D23` | Success pill background |
| `--warning` | `#D29922` | Warnings, progress bars 65–85% |
| `--warning-bg` | `#5A3A0A` | Warning pill background |
| `--danger` | `#F85149` | Errors, offline, progress bars over 85% |
| `--danger-bg` | `#3D1E1E` | Error pill/toast background |
| `--info` | `#58A6FF` | Links, external-link anchors |

### Selection / focus

- Text selection: `background: #E3571E44; color: #F0F6FC;`
- Keyboard focus: `box-shadow: 0 0 0 2px #E3571E55;` on focus-visible

---

## Typography

```css
font-family: -apple-system, BlinkMacSystemFont, 'Inter', 'Segoe UI', sans-serif;
-webkit-font-smoothing: antialiased;
text-rendering: optimizeLegibility;
```

| Size | Usage |
|---|---|
| 10px | Labels, column headings, tiny meta |
| 11px | Status pills, column headers, footer version text |
| 12px | Body text, table cells, buttons |
| 13px | Default body size on `body` |
| 14px | Card headings |
| 15px | Page titles / H3 in modals |
| 17–22px | Summary card values |
| 28px | Summary card hero numbers |

| Weight | Usage |
|---|---|
| 400 | Default body text |
| 500 | Buttons, meta emphasis |
| 600 | Labels, VM names, card headings, nav items |
| 700 | Brand wordmark, large values |

Monospace for code, paths, MACs, UUIDs: `'SF Mono', 'Fira Code', 'Courier New', monospace`.

---

## Spacing

All spacing is multiples of 4px. Prefer these values:

| | |
|---|---|
| 4px | Inline gap between tiny adjacent items |
| 6px | Icon-to-label gap |
| 8px | Small gap between related controls |
| 10px | Nav item internal padding |
| 12px | Standard card padding |
| 14–16px | Panel/card padding, grid gaps |
| 18–20px | Page/content padding |
| 24px | Section separators, modal padding |

---

## Radius

| Value | Usage |
|---|---|
| 3px | Tiny chips, tight pills, inline badges |
| 4px | Status pills, code blocks, table wrappers |
| 5px | Buttons |
| 6px | Cards, panels, modals, list containers |
| 10px | Large pills ("● Connected") |
| 38px | PWA icon background (iOS/Android compatibility) |

---

## Shadows / elevation

Keep shadows subtle. Used only on:
- Modals: `box-shadow: 0 20px 60px #00000060;`
- Toasts: `box-shadow: 0 4px 16px rgba(0,0,0,0.5);`
- Logo SVG (drop-shadow filter): `filter: drop-shadow(0 0 8px rgba(227,87,30,0.25));`

Cards do NOT use box-shadow — elevation is communicated via `--border` and subtle bg colour shift only.

---

## Components

### Buttons

```html
<button class="btn btn-primary">Primary</button>
<button class="btn btn-secondary">Secondary</button>
<button class="btn btn-danger">Destructive</button>
<button class="btn btn-warning">Warning</button>
<button class="btn btn-primary btn-sm">Small</button>
```

- **Primary**: orange gradient (`linear-gradient(180deg, #F26419 0%, #E3571E 100%)`) on `--accent-dim` border. Reserved for the main action on each surface.
- **Secondary**: `--surface-active` bg, `--border` border. Default action style.
- **Danger**: dark red bg `#7A1C1C`, red text. Only for destructive irreversible actions (delete snapshot, force stop).
- **Warning**: dark amber `#5A3A0A`. For graceful shutdown, irreversible-but-recoverable actions.
- **Small** (`.btn-sm`): 4px × 10px padding, 11px font. Used inline in table rows, action bars.
- **Focus**: 2px orange-translucent ring via `box-shadow` on `:focus-visible`.
- **Disabled**: opacity 0.35, `cursor: not-allowed`.

### Cards / Panels

```html
<div class="detail-panel">content</div>
```

- Background `--surface`, 1px `--border`, 6px radius, 16px padding default.
- Section title above: `.section-title` (uppercase, 11px, `--text-faded`, with underline).

### Status pills

```html
<span class="status-pill online">online</span>
<span class="vm-state running">running</span>
```

- Rounded 10px, 2px × 8px padding, 10px uppercase font, 600 weight, 0.6px letter-spacing.
- Paired bg + text tokens from the Status section above.

### Inputs

- Fixed height 36px (text/password/number), min-height 80px (textarea).
- `--bg-input` background, `--border` border, 4px radius, 12px font.
- Focus: border colour changes to `--accent`.
- Selects use a custom SVG arrow (`appearance: none`) at right 10px center for cross-platform consistency.

### Tables

- Wrapper `.table-wrap`: `--surface` bg, `--border` border, 6px radius.
- Header: `--bg-input` bg, 10px uppercase font, `--text-faded`.
- Row hover: `--surface-hover`.
- Sortable column: `.sortable` class on th. Active column shows ▲/▼ in accent orange; inactive shows faded ⇅.

### Navigation

- Sidebar width 220px, `--bg-sidebar` bg, 1px right border in `--border`.
- Nav items: 7px × 12px padding (10px on touch devices), 14px icon, optional right-aligned badge.
- Active state: left-to-right gradient fade from `rgba(227,87,30,0.12)` to transparent, 3px orange left border.

### Progress bars

- Track: 4–8px tall, `--border` bg, 2–3px radius, `overflow: hidden`.
- Fill: colour based on value — green ≤ 65%, yellow ≤ 85%, red > 85%, or `--accent` when neutral.

### Toasts

- Fixed bottom-right, 10–16px from edge, 240px min-width, 4px left border in status colour, 4px radius.
- Auto-dismiss after 4–5 seconds with `slideIn` animation.

---

## Logo

The brand logo is a hexagonal shield in QEMU amber-orange enclosing three
descending horizontal bars (stacked VMs).

```svg
<svg width="28" height="28" viewBox="0 0 32 32" fill="none">
  <defs>
    <linearGradient id="logo-accent" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0%" stop-color="#FF8A3C"/>
      <stop offset="100%" stop-color="#E3571E"/>
    </linearGradient>
  </defs>
  <path d="M16 3 L27 10 L27 22 L16 29 L5 22 L5 10 Z"
        fill="none" stroke="url(#logo-accent)" stroke-width="2" stroke-linejoin="round"/>
  <rect x="10" y="11" width="12" height="3" rx="0.5" fill="url(#logo-accent)"/>
  <rect x="10" y="15" width="9"  height="3" rx="0.5" fill="#E3571E" opacity="0.7"/>
  <rect x="10" y="19" width="6"  height="3" rx="0.5" fill="#E3571E" opacity="0.45"/>
</svg>
```

- Accompanying wordmark: `kvm` in `--text`, `center` in `--accent`, lowercase, 700 weight, -0.01em letter-spacing, 2px gap between the two spans.
- Logo SVG carries a subtle orange drop-shadow: `filter: drop-shadow(0 0 8px rgba(227,87,30,0.25))`.
- On the PWA home-screen icon (192/512), the hex sits on a `#0B0E13 → #1C2128` vertical gradient with 38px corner radius.

---

## PWA manifest colors

```json
{
  "background_color": "#0B0E13",
  "theme_color": "#E3571E"
}
```

---

## Do / don't

- ✅ Use QEMU orange for brand and a single primary action per surface.
- ✅ Use status colors only for semantic meaning.
- ✅ Keep cards flat (border-only, no shadow).
- ✅ Respect the 36px input height everywhere.
- ❌ Don't introduce additional accent colors (no purple, teal, pink).
- ❌ Don't use cyan — that's from pre-v2.0 and is out.
- ❌ Don't mix serif with the sans-serif body font.
- ❌ Don't put more than one primary button on a surface at the same time.
