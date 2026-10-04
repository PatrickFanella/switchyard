# Switchyard mark studies

These are retained vector studies for Signal Yard. The owner selected **E2 / Split plates** after the focused D/E/F round. Its editing master is [the selected mark](../identity/mark.svg); the remaining constructions stay as studies. Each file uses a 64×64 viewBox and `currentColor` so geometry can be reviewed in monochrome independently from the palette.

| Study | Construction | Review question |
| --- | --- | --- |
| [A — Turnout](turnout.svg) | Paired rails split into a vertical route and a diagonal branch. | Does the railway-switch idea read clearly at 24px? |
| [B — Dispatch](dispatch.svg) | One lower input connects to three upper destinations. | Does the broader routing idea retain enough Switchyard identity? |
| [C — Yard](yard.svg) | Three horizontal tracks with a diagonal crossover. | Is the compact industrial symbol distinctive without the wordmark? |

The selected construction is E2 below. A–C are retained from the first exploration.

## Second exploration

The owner requested more marks after reviewing A–C. The following constructions broadened the study within the selected Signal Yard direction; none of this round's constructions was selected directly.

| Study | Construction | Review question |
| --- | --- | --- |
| [D — Rail S](rail-s.svg) | A paired track drawing bends into an angular S. | Can the letter and track idea share one readable shape? |
| [E — Switch S](switch-s.svg) | Heavy switch plates form a compact S silhouette. | Does a solid mark carry the industrial character better than lines? |
| [F — Point Blade](point-blade.svg) | A straight rail and a broad diagonal blade share a footing. | Does the reduced turnout remain distinctive at small sizes? |
| [G — Interlock](interlock.svg) | Two bent tracks face each other across a diagonal channel. | Does the negative space communicate coordinated movement? |
| [H — Route Stamp](route-stamp.svg) | An S route is cut into a solid square. | Is a compact equipment-label mark a useful product identity? |
| [I — Three Lanes](three-lanes.svg) | Staggered tracks join through a diagonal turnout. | Does the wider track motif pair well with the wordmark? |
| [J — SY Monogram](sy-monogram.svg) | Angular S and Y lettering share a construction grid. | Should the letters lead the identity? |
| [K — Switch Lever](switch-lever.svg) | A mechanical switch lever rests on parallel tracks. | Would a more literal piece of equipment suit the character family? |

Render the second exploration from these SVG masters:

```sh
rtk proxy python3 docs/design/marks/render_studies.py /absolute/review-directory
```

The renderer creates two SVG review sheets and corresponding PNGs by default. It requires `rsvg-convert`; Space Grotesk should be installed for the intended review typography. Outputs are review material, not approved brand exports. The renderer refuses to overwrite its outputs unless `--overwrite` is provided.

## Focused D/E/F variations

The owner requested variations on D, E, and F. This round kept those families and explored their geometry. Each sheet includes the original beside three alternatives. **E2 was selected**; the other variations remain studies.

| Family | Variation | Construction |
| --- | --- | --- |
| D / Rail S | [D1 — Sharp points](rail-s-sharp.svg) | Narrower paired rails with mitered joints. |
| D / Rail S | [D2 — Open junction](rail-s-open.svg) | Separated paired bends with an open center. |
| D / Rail S | [D3 — Soft bends](rail-s-soft.svg) | Continuous paired tracks with rounded turns. |
| E / Switch S | [E1 — Wide cuts](switch-s-wide.svg) | Enlarged diagonal slots in the heavy plate. |
| E / Switch S | [E2 — Split plates](switch-s-split.svg) | Two separate plates divided by a horizontal channel. |
| E / Switch S | [E3 — Continuous plate](switch-s-continuous.svg) | A solid angular S with open counters and no enclosed slots. |
| F / Point Blade | [F1 — Clean turnout](point-blade-clean.svg) | One connected silhouette without auxiliary parts. |
| F / Point Blade | [F2 — Twin route](point-blade-twin.svg) | Two parallel diagonal blades. |
| F / Point Blade | [F3 — Split footing](point-blade-split.svg) | An independent upright and a blade with a broad lower rail. |

Render all three comparison sheets with:

```sh
rtk proxy python3 docs/design/marks/render_studies.py /absolute/review-directory --round 3
```

The studies use a 64px construction grid. Follow the [identity guide](../identity/README.md) for the selected mark's master and usage rules. The alternatives remain exploratory material.
