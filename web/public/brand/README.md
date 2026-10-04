# Generated Switchyard assets

Editable identity masters and product tokens live in `docs/design`. Run `python3 docs/design/identity/sync_web_assets.py` from the repository root after changing the mark or tokens. This refreshes the mark, favicon, generated web tokens, and their manifest hashes. The outlined wordmark comes from the identity renderer and must be refreshed when its master changes.

The fonts are local WOFF2 encodings of complete Google Fonts distributions, without glyph subsetting. `manifest.json` records font versions, immutable Git blob IDs, and source/output SHA-256 hashes. Keep each family’s bundled OFL license beside its files. Font sources: [Sora](https://github.com/sora-xor/sora-font), [Public Sans](https://github.com/uswds/public-sans), and [IBM Plex](https://github.com/IBM/plex).

The dark application palette consumes the generated `web/brand-tokens.json`. Tailwind maps product roles to the existing HUD class contracts. Tinted status backgrounds are implementation values in `web/tailwind.config.js`; the light palette remains a design proposal.
