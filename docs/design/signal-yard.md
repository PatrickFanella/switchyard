# Signal Yard

Status: **selected direction, E2 mark construction, and Sora 600 wordmark; detailed specification proposed**. Owner selections: October 3, 2026. Product: Switchyard.

## Brand idea

Switchyard is an automation runtime for workflows, model requests, and agent execution. Its operator interface answers four questions: what is running, what is waiting, what needs attention, and what happened.

Signal Yard takes its visual language from railway switching diagrams and industrial dispatch systems. Parallel tracks represent independent work. A turnout represents routing or a decision. A signal represents a recorded state. Each motif must clarify a relationship when it appears in the interface.

The character layer belongs to the people and agents doing the work: the paired subcult-agent and Open Pilot robot avatars establish its illustration style. The product identity should feel related to that crew while remaining recognizable without a mascot.

Working positioning: **Route work. Follow every run.** This is sample copy for review.

## Identity hierarchy

| Level | Identity | Treatment |
| --- | --- | --- |
| Publisher | SUBCULT | Use the existing approved publisher identity where attribution is needed. |
| Product | Switchyard | The selected split-plate S and lowercase Sora 600 wordmark. |
| System | Signal Yard | Name of this design direction; not a product rename or service boundary. |
| Workflows | Open Pilot, Changemaker, Devlog | Names, capabilities, and optional character portraits within Switchyard. |
| Agents | Native profiles/personas | A consistent illustrated family, accompanied by readable names and roles. |

Blacktower remains Switchyard-native profiles/personas. Branding must not imply a separate Blacktower application. Workflow illustrations must not imply separate runtimes.

## Mark and wordmark

The owner selected **E2 / Split plates**: two angular plates separated by a horizontal channel imply an S. The [selected master](identity/mark.svg) preserves that geometry exactly. The [identity guide](identity/README.md) records its source and draft usage rules. Retain the [mark studies](marks/README.md) as the selection record. Show the mark beside the full product name where recognition is still developing.

The [horizontal lockup](identity/lockup.svg) uses lowercase `switchyard` in the owner-selected Sora Semibold (600). Its neutral tracking and spacing remain draft specifications. Keep the name legible. It has no slash or extra punctuation.

Establish clear space and minimum sizes after selecting and optically refining the mark. Deliver editable SVG masters, outlined SVGs, monochrome versions, and transparent raster exports from those masters. Generated raster output must retain source provenance.

## Color

The proposed palette uses warm graphite and ivory for the working surface, violet for deliberate interaction, and green for successful outcomes. Colors and exact values are in [tokens.json](tokens.json).

| Role | Dark draft | Light draft | Use |
| --- | --- | --- | --- |
| Canvas | `#111017` | `#F4F0E7` | Main working field. |
| Surface | `#1B1923` | `#FFFCF5` | Bounded inspection and editing surfaces. |
| Text | `#F2ECDE` | `#28242F` | Primary information. |
| Secondary text | `#B4ADBD` | `#625B6D` | Metadata and supporting explanation. |
| Action / focus | `#AD8AFF` | `#6334AA` | Selection, primary actions, and focus. |
| Successful outcome | `#50EF97` | `#196A43` | Completed or passed, paired with a label and symbol. |
| Attention | `#F4C16B` | `#85511F` | A checkpoint, warning, or operator intervention. |
| Failure | `#FF8A90` | `#A52B3B` | Failed operations and actionable errors. |
| Control boundary | `#746B84` | `#80768D` | Visible input and secondary-control boundaries. |

Green in a mascot is illustration color. In the interface, green must identify a successful outcome. A green brand illustration does not establish runtime health.

Violet fill uses the dedicated action foreground token. Do not put ivory text on the dark theme's light violet button. Preserve text contrast of at least 4.5:1 and control/focus contrast of at least 3:1 against the adjacent surface. Divider tokens are decorative and cannot replace control boundaries.

Dark is implemented in the local dashboard. The light tokens remain a proposed counterpart; no theme preference is added to the application. Rendered checks qualify the local interface separately from a deployed release.

## Typography

| Role | Proposed family | Rules |
| --- | --- | --- |
| Wordmark and display | Sora | Selected weight 600 for the wordmark; proposed for page headings and short promotional statements. |
| Interface and reading | Public Sans | Regular/medium/semibold; sentence case for controls, explanations, and reports. |
| Technical evidence | IBM Plex Mono | Regular/medium; run IDs, timestamps, revisions, durations, code, and bounded dispatch labels. |

The local implementation uses these roles. The web bundle retains licenses, versions, immutable distribution blob IDs, and source/output hashes in `web/public/brand/manifest.json`. The Studio starter includes Space Grotesk, Public Sans, and JetBrains Mono; The local bundle adds Sora and IBM Plex Mono explicitly. The owner selected Sora 600 after comparing it with Barlow Semi Condensed and IBM Plex Sans. Do not copy proprietary reference fonts.

Use 16px as the reading default, 14px for compact interface text, and 12px for supporting technical labels. A 28–36px page heading should establish the task without dominating a dense view. Large promotional display type belongs in artwork and introductory pages. Use tabular figures for aligned changing values. Avoid uppercase paragraphs and excessive tracking on small text.

## Structure and components

Retain the application's Operate, Inspect, and Configure grouping. The design must support existing capabilities and permission boundaries.

Use a 4px spacing unit with 8, 12, 16, 24, 32, 48, and 64px steps. Use 4px radii for controls and 8px for inspection/editing panels. Separate surfaces with tone and thin rules. Reserve shadows for overlays that require depth to explain their stacking.

| Component | Design contract |
| --- | --- |
| Shell | Persistent navigation and an explicit environment/connection indicator. Brand color cannot replace a textual connection state. |
| Run list | Aligned rows with workflow, destination, state, and timing. Preserve scan order across filters and refreshes. |
| Attention queue | Show the intervention reason, affected run, last evidence, and available next action. A paused run is distinct from a failure. |
| Run cockpit | Group execution steps, events, artifacts, prompt/model policy, and operator controls around one run. |
| Step timeline | Track lines may connect actual ordered steps; labels and state symbols remain readable without color. |
| Evidence panel | Prioritize source revision, artifact identity, verification scope, and freshness. Separate local evidence from hosted and production evidence. |
| Model queue | Show admission and request lifecycle independently from upstream provider health. |
| Editor | Surface draft/version state, validation, unsaved work, stale conflicts, and permissions close to the affected controls. |
| Confirmation | Identify the target and consequence of a consequential operation. Preserve the application's policy and authorization gates. |

An action uses a verb and target: `Inspect evidence`, `Retry run`, `Cancel request`. Do not invent an available action for a mockup. Mark sample records and conceptual screens visibly.

## States and accessibility

| State | Visual treatment | Required information |
| --- | --- | --- |
| Queued | Neutral label and waiting symbol. | Queue position or admission context when known. |
| Running | Clear active label; a restrained activity indicator. | Start time, latest event, and freshness. |
| Paused | Attention treatment when intervention is needed. | Pause reason and available resolution. |
| Cancel requested | Explicit transitional label. | Cancellation has been requested; a terminal outcome is still pending. |
| Completed | Green success treatment and check symbol. | Outcome and relevant evidence. |
| Failed | Failure treatment and error symbol. | Concrete failure, retained evidence, and available recovery. |
| Canceled / dropped | Distinct neutral terminal labels. | What stopped and which lifecycle applies. |
| Stale / disconnected | Visible freshness or connection notice. | Last successful observation; do not reinterpret missing data as zero. |

Workflow and model-request state vocabularies remain distinct. Apply only states supported by the relevant contract.

Every component needs applicable loading, empty, error, disabled, read-only, selected, and keyboard-focus treatments. Use a neutral empty state with a meaningful next action when one exists. Avoid a smiling mascot in an incident or failure notice where it would obscure the consequence.

Keyboard focus is a visible 2px outline with 3px offset. Use approximately 44px touch targets without making desktop evidence rows unnecessarily tall. Respect reduced motion. Animate only a meaningful transition or activity; persistent glows and blinking decorative lights must not compete with alerts.

At narrow widths, retain the primary task and attention reason. Move supporting detail below it, allow technical values to copy without truncation loss, and contain wide evidence tables in a labeled scrolling region. Do not scale an entire desktop composition into a small viewport.

## Illustration and promotional grammar

The paired robots establish a rounded dark screen face, warm ivory outline, vivid purple/green fields, expressive eyes, and distressed print texture. Role accessories distinguish characters: Open Pilot has raised flight goggles and a headset. Character names remain visible beside small portraits.

Keep grunge in illustration, large promotional fields, and occasional section artwork. Controls, evidence text, and small status symbols remain sharp. Do not redraw input boundaries as distressed strokes.

Promotional compositions can use oversized track junctions, numbered dispatch labels, a small crew portrait, and a real product capture. Preserve the screenshot's proportions and mark fixture data. Use one central product claim tied to evidence. Current sample positioning and layouts are not approved public copy.

Switchyard's selected mark is the split-plate S. Supporting graphics can use railway turnouts, parallel tracks, and selected branches. Keep them distinct from Patrick's existing routed-signal/spectrum identity and from other SUBCULT product marks.

## Voice

Use direct operator language: `Verification passed`, `Waiting for review`, `Last updated 2 minutes ago`, `Provider unavailable`. Explain what the state means and what the operator can do.

For introductions, explain durable workflows, routing, agent execution, and evidence through concrete examples. Avoid promises that agents always succeed or that automation requires no supervision. A metaphor can establish identity; task labels should describe the actual action.

## Sources and evidence limits

The following Studio records informed the selected direction. They are study material, not owner-supplied design systems or reusable identity assets.

- [Linear study](https://subcult.tv/admin/studio/references?reference=refero-90ce5883-bb24-4466-93f7-801cd617b0d1): restrained hierarchy and precise surfaces. The retained screenshot was visually inspected; its original capture date is unknown.
- [Inngest study](https://subcult.tv/admin/studio/references?reference=website-inngest): execution-state presentation and action hierarchy. The curated study records desktop visual/style observations and incomplete interaction evidence.
- [GT Planar study](https://subcult.tv/admin/studio/references?reference=study-gt-planar): instrument-strip labeling. The supplied image was visually inspected; the animation scene is not an application pattern to reproduce.
- [Studio brand library](https://git.subcult.tv/subculture-collective/subcult-tv/src/branch/main/studio/branding/library): existing pack conventions and product-specific identities.
- [Studio starter](https://git.subcult.tv/subculture-collective/subcult-tv/src/branch/main/studio/branding/library/starter-template): asset outputs, guide, tokens, licensed-font packaging, and provenance requirements.
- The owner-supplied subcult-agent avatar and the generated Open Pilot avatar in this thread: character-family references. Preserve supplied artwork and original generated files; derive assets only from the selected revision.

The initial carousel tested the direction with illustrative workflow states. It is not the running application. Token checks cannot prove component accessibility, responsive behavior, or a deployed interface.

## Implementation sequence

1. Preserve the selected E2 construction and Sora 600 wordmark; refine spacing, usage rules, and small-size treatments.
2. Review the detailed palette, typography, and illustration rules using a representative run list, attention state, and run cockpit.
3. Produce licensed-font bundles, final vector masters, a product guide, and role-based CSS/JSON tokens with source hashes.
4. Apply the system to shared shell/components and preserve existing behavior, semantics, and authorization. Verify representative wide/narrow screens and interaction states.
5. Generate the Studio pack from the product source: icons, avatars, banners, share images, screenshot frames, social and video layouts. Record source revision and manifests.
6. Qualify the application and assets separately. Publishing and deployment are distinct actions from preparing the design.
