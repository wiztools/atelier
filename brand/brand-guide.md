# Atelier — Brand Guide

Atelier is a warm-dark, craftsman-studio brand: charcoal-black surfaces, warm paper-white text, and an artisan gold accent family around a framed serif "A".

## Logo

The mark is a rounded dark square (`#151719`) holding an inset rounded tile (`#1d2022`) outlined in bronze (`#b8905c`, thin centered stroke), with a large gold (`#ffcf7b`) serif capital "A" (Georgia) centered inside the tile with a slight downward optical drop. The overall impression: a picture frame on a gallery wall.

On a 1024×1024 grid: outer canvas corner radius ≈230; tile inset 92 on each side (840×840) with corner radius 39; border stroke weight 9; letter size 690, centered, shifted down 28.

## Colors

**Surfaces (dark to light):**

| Color | Hex | Role |
| --- | --- | --- |
| Deep wells | `#0f1112`, `#121415` | Recessed areas — inputs, code panels; darker than the background, never lighter. |
| Canvas | `#151719` | The signature background — app screens and the logo canvas. Default color for any brand frame. |
| Elevated panels | `#181b1d`, `#1d2022` | Cards and raised surfaces; `#1d2022` is the logo tile fill. |
| Hover fills | `#25292b` | Subtle hover states. |
| Hairline dividers | `#2b2e30` | Thin separators. |
| Borders | `#303438` | Standard control and list-item outlines. |
| Strong borders | `#3b3f42` | Emphasized outlines. |

**Text (dark surfaces only):**

| Color | Hex | Role |
| --- | --- | --- |
| Primary text | `#f3f0e8` | Warm off-white "paper" — headlines, body copy, the wordmark. |
| Secondary text | `#d7d1c3` | Labels and supporting lines. |
| Muted text | `#9ea5a3` | Captions, hints, metadata — cool gray-green, used widely. |

**Gold family (the brand accent):**

| Color | Hex | Role |
| --- | --- | --- |
| Letter gold | `#ffcf7b` | The brightest gold — reserved for the logo "A" itself. |
| Bright gold | `#e0c078` | Hover and emphasis highlights. |
| Accent gold | `#d5a64f` | The main accent — active states, highlights, calls to action. Use this for gold moments outside the logo. |
| Warn gold | `#d9a25a` | Cautious/warning tones. |
| Bronze | `#b8905c` | The logo's border; a structural, muted outline tone. |

**Functional colors:** info teal `#5da9b7`, success green `#6fa56d` (border `#35563e`), error red trio — text `#f0a09a`, border `#8f4a45`, surface `#2b1817`. Use only with their meanings, sparingly.

**Shadows/depth:** black shadows at 35–55% opacity; white overlays at 4–22% for hover/disabled states.

**Rules:** Gold is always an accent on dark surfaces, never body text. The off-white and gold tones must never sit on light backgrounds — on a white frame, place the logo/wordmark on a `#151719` panel. Depth is expressed by darker wells *below* the canvas and lighter panels above it.

## Typography

- **Georgia (serif) — brand & display.** The logo "A", the "Atelier" wordmark, and titles. One large Georgia statement per scene; it is the voice of the mark.
- **Inter (sans-serif) — everything else.** UI text, captions, supporting copy. Weights 400–800 (regular through extra-bold). If unavailable, it falls back to the system sans (SF Pro on macOS).
- **Monospace — terminal/code beats.** SF Mono or Menlo: `ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace`.

**Pairing for video:** titles and logo moments in Georgia; all supporting text in Inter; anything terminal-like in SF Mono. Keep screen-style UI text at 16px or larger — true UI chrome sizes (11–13px) don't read at video distance.
