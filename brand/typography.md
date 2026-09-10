# Atelier — Typography

Two typefaces carry the brand: **Georgia** for the mark and display moments, **Inter**
for everything else. Code and tool output use the system monospace stack.

## Georgia — brand & display

- The logo "A", the "Atelier" wordmark, and the sidebar title (`Georgia, serif`,
  25px, weight 500 — see `.brand h1` in `frontend/src/App.css`).
- Rendered in `build/render_appicon.swift` at 690px on the 1024px icon grid.
- Georgia ships with macOS and Windows, so it renders natively in screen captures.
  **For render farms or Linux CI, don't rely on the font being installed** — the SVGs in
  this folder (`logo.svg`, `logo-wordmark.svg`) have the glyphs baked as vector outlines
  and render identically anywhere. Use them for anything pre-rendered.

## Inter — UI text

- Stack (verbatim from `frontend/src/style.css`):
  `Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif`
- Weights in use in the app: 400, 500, 600, 650, 700, 750, 800 (Inter's variable weight
  shows up as the odd 650/750 values).
- **Inter is not bundled or web-loaded** — the app uses a locally installed Inter and
  falls back to the system sans (SF Pro on macOS) when absent. For the Remotion project,
  load it explicitly (e.g. `@remotion/google-fonts`' `loadFont("Inter")` or a self-hosted
  `@font-face` via `staticFile`) so the video matches the app regardless of what's installed.
- Representative app sizes: 11–13px chrome/labels, 14px buttons, 15px settings section
  heads, 16px composer text, body line-height 1.55 (`.markdown-body`).

## Monospace — code & tool output

`ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace`
(verbatim from `App.css`). Used for code blocks and harness/tool output. In the video,
any "terminal beat" should use SF Mono / Menlo to match.

## Rules of thumb for the intro video

- **Titles and the logo moment: Georgia.** It's the voice of the mark; keep UI-recap
  text in Inter so screen recordings and overlays agree.
- Don't substitute the logo's letterform — if the "A" appears, use the SVGs, not retyped
  Georgia.
- Pair sizes like the app does: one large Georgia statement per scene, Inter at 16px+
  for supporting copy (the app's chrome is small — 11–13px — which reads poorly at video
  distances; scale it up rather than showing true-size UI chrome as text overlays).
