# Atelier — Color Usage

Atelier's identity is a **warm-dark workshop palette**: charcoal-blue-black surfaces, warm
paper-white text, and an artisan **gold** accent family that echoes the logo's framed "A".
This document is the video-safe reference. Sources of truth: `frontend/src/style.css`,
`frontend/src/App.css` (UI), and `build/render_appicon.swift` (logo).

## Core palette

| Color | Hex | Role |
| --- | --- | --- |
| Canvas | `#151719` | The app background — shell, body, and the logo's dark canvas. The color a video frame should default to. |
| Tile / elevated panel | `#1d2022` | The logo tile fill; in-app elevated cards (e.g. permission cards use a sibling `#1d1f20`). |
| Recessed wells | `#121415`, `#0f1112` | Inputs, code blocks, and sunken panels — one step *darker* than canvas, not lighter. |
| Panels / hover | `#181b1d`, `#25292b` | Secondary panels and hover fills. |
| Dividers | `#2b2e30` | Hairline separators (composer top edge, sidebar resizer track). |
| Borders | `#303438` | Standard control/list borders (history items, buttons, chips). |
| Strong borders | `#3b3f42` | Emphasized outlines (capability chips). |

## Text colors (on dark surfaces only)

| Color | Hex | Role |
| --- | --- | --- |
| Primary text | `#f3f0e8` | Warm off-white "paper" — headings, chat text, the wordmark. |
| Secondary text | `#d7d1c3` | Labels, usage lines, transcript names. |
| Muted text | `#9ea5a3` | Captions, hints, metadata — the most-used text color in the UI. |

## The gold family (brand accent)

| Color | Hex | Role |
| --- | --- | --- |
| Letter gold | `#ffcf7b` | The logo "A" and sidebar mark — the brightest gold; reserved for the mark. |
| Bright gold | `#e0c078` | Hover/emphasis states, model labels, image-preview controls. |
| **Accent gold** | `#d5a64f` | The workhorse accent — active states, highlights, focus marks. Use this for highlights in the video. |
| Warn gold | `#d9a25a` | Warning flags ("hash changed"). |
| Bronze | `#b8905c` | The logo tile border and sidebar mark border — the structural outline tone. |

Gold is always an accent, never a body-text color, and always sits on dark surfaces.

## Functional colors

| Color | Hex | Role |
| --- | --- | --- |
| Teal | `#5da9b7` | Info — markdown links, selections, focus accents, resizer grips. |
| Green | `#6fa56d` (border `#35563e`) | Success — OK flags, ready states. |
| Error red | text `#f0a09a`, border `#8f4a45`, surface `#2b1817` | Errors and startup warnings — desaturated red that sits quietly on dark. |

## Overlays

Shadows are black at 35–55% opacity; hovers/disables are white at 4–22%. For video glows
and vignettes, stay in that range to match the app's depth language.

## Rules of thumb for the intro video

- **Background:** `#151719`. Step content up with `#1d2022` panels, down with `#121415` wells.
- **Headlines:** `#f3f0e8`; captions/metadata: `#9ea5a3`.
- **Highlights, CTAs, the "gold moment":** `#d5a64f` (or `#ffcf7b` only when the logo itself is on screen).
- **Never** set `#f3f0e8` or the golds on light backgrounds — they are tuned for dark surfaces.
  On a white frame, the logo wordmark needs a dark `#151719` panel behind it.
- Use teal/green/red sparingly and only with their meaning (info / success / error).
