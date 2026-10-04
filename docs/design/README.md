# Switchyard design

**Signal Yard is the owner-selected direction.** The owner selected direction A on October 3, 2026. The product name remains Switchyard; Signal Yard names its visual system.

Switchyard routes work, coordinates execution, and gives operators the evidence and controls to intervene. Its identity should make those relationships visible through track junctions, dispatch labels, and clear signals.

The owner also selected **E2 / Split plates** as the mark construction and **Sora Semibold (600)** for the wordmark. The detailed system, lockup spacing, and exact tokens remain drafts for review.

- [Signal Yard design system](signal-yard.md): identity, typography, interface rules, illustration, and implementation sequence.
- [Draft tokens](tokens.json): color, type, spacing, and shape roles; the generated web copy drives the dark theme.
- [Selected identity](identity/README.md): E2 master and selected Sora 600 wordmark with draft spacing.
- [Mark studies](marks/README.md): retained vector explorations and selection reference.

These files are the editing source for Switchyard's product design. A future Studio pack should carry generated copies, a source revision, and asset hashes. Shared Studio generators retain their own ownership. Keep this product source and the generated pack linked rather than maintaining two independently editable design guides.

The local application now applies Signal Yard through `web/src/styles.css`, `web/tailwind.config.js`, and generated web tokens. It retains the shared HUD component class names for compatibility. The dark theme is implemented; the light palette remains a proposal. This records local source changes, not deployed behavior.
