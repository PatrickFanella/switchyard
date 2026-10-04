# Switchyard identity

The owner selected **E2 / Split plates** on October 3, 2026. [mark.svg](mark.svg) preserves the chosen study's geometry exactly. It is the editing master for the selected construction. Keep the historical [E2 study](../marks/switch-s-split.svg) as the selection reference.

The mark consists of two angular plates separated by a horizontal channel. It implies an S while retaining the industrial switching language of Signal Yard.

The owner selected **Sora Semibold (600)** for the wordmark on October 3, 2026. [lockup.svg](lockup.svg) pairs it with E2 in lowercase. The mark and wordmark family/weight are selected; spacing, color treatments, and usage rules remain draft specifications.

Both masters use `currentColor`. The mark's 64-unit construction frame contains 8-unit top and bottom margins. Keep at least 8 units of additional external clear space around that frame. Use 24px as the working minimum size for interface use; inspect the 16px treatment separately for compact icons. These are draft usage rules.

Generate review exports with:

```sh
rtk proxy python3 docs/design/identity/render_identity.py /absolute/review-directory
```

The renderer needs Sora Semibold (600), Inkscape, fontconfig, and `rsvg-convert`. The font must be visible to fontconfig; an isolated review font directory can be supplied through `FONTCONFIG_FILE`. It records source and font hashes, produces an outlined horizontal lockup, transparent color/monochrome marks, and a review sheet. It refuses to overwrite outputs unless `--overwrite` is supplied. The application uses generated copies of the mark and an outlined wordmark under `web/public/brand`, with fonts hosted locally. Review sheets remain separate from application assets.

Palette roles come from [the draft tokens](../tokens.json). Violet/ivory on graphite and deep violet/ink on paper are the proposed identity treatments. The robot portraits remain the agent/workflow character layer.

The local dashboard branding is implemented. The web asset manifest records font versions, immutable source blob IDs, source/output hashes, and design source hashes; bundled licenses accompany the fonts. Deployment and the Studio pack remain separate work.

After editing canonical tokens or the mark, run `rtk proxy python3 docs/design/identity/sync_web_assets.py` to refresh `web/brand-tokens.json`, the web mark/favicon, and their hashes. Do not edit generated copies independently. The outlined wordmark must be regenerated with the identity renderer when its source changes. Font conversion uses fontTools to encode complete TTF distributions as WOFF2 without glyph subsetting.
