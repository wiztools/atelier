# Atelier brand kit

Assets for the intro video (and any future brand-adjacent output).

| File | What it is |
| --- | --- |
| `logo.svg` | The app mark: dark rounded-square canvas, bronze-bordered tile, gold Georgia "A". Glyphs are outlined paths — renders correctly with no fonts installed. |
| `logo-wordmark.svg` | Horizontal lockup: mark + "Atelier" wordmark in Georgia (outlined). Cream word expects a dark background. |
| `colors.md` | Palette and color-usage rules. |
| `typography.md` | Georgia / Inter / mono stack and usage. |
| `brand-guide.md` | Self-contained guide (no source-file references) — safe to paste into video-generation prompts. |

## Construction (matches `build/render_appicon.swift`)

On a 1024×1024 grid: canvas `#151719` (rounded `rx≈230` — the macOS icon mask; the
generated PNG is square and masked by the OS), tile inset 92 (840×840) with corner
radius 39, fill `#1d2022`, centered 9px stroke `#b8905c`; the "A" is Georgia at 690px
in `#ffcf7b`, line-box centered with a 28px optical drop. The SVG letter placement was
verified against `build/appicon.png` — ink bounding boxes match within ~2px.
