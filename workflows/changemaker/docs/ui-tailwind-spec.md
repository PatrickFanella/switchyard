# JuryRigged UI & Tailwind Recreation Spec

> **Purpose:** Complete spec to recreate the entire UI system from scratch.
> **Last updated:** 2026-06-09

---

## 1. Design Philosophy

**Aesthetic:** "Neubrutal HUD" — a high-contrast broadcast control panel. Terminal discipline meets sci-fi instrumentation. Bold, readable, technical, theatrical.

**Core rules (overlay/viewer):**
- Flat colors only. No gradients. No glassmorphism.
- Hard outlines (`border`, `border-3`). Hard corners (`rounded-none`, `rounded-[1px]` at most).
- Hard offset shadows for depth (e.g., `8px 8px 0 #000`). No blur, no soft shadows.
- Monospace metadata. Uppercase micro-labels. Large readable primary text.
- Deliberate asymmetry. Clear instrument-panel grouping.
- No green-on-black terminal cliché. Use purple (`signal`) and indigo (`pulse`) as primary accents.

**Priority order:** readability → information hierarchy → stream-safe layout → visual impact → decorative detail.

---

## 2. Architecture: Two SPAs, One Tailwind Config

The project is a **Vite + Express** monolith with two SPAs sharing one `tailwind.config.js`:

| SPA | Root | Base URL | Entry CSS | Vite Config |
|-----|------|----------|-----------|-------------|
| **Viewer** | `app/` | `/` (SPA fallback) | `app/src/styles.css` | `vite.app.config.ts` |
| **Dashboard** | `dashboard/` | `/operator` (SPA fallback) | `dashboard/src/index.css` | `vite.config.ts` |

Both build outputs are served as static files by Express (`src/server.ts`), with SPA fallback for client-side routing.

The viewer uses **query-param routing** (`?view=dashboard|overlay|transcripts|submit|about`). The dashboard uses **tab-based navigation** with lazy-loaded panels.

---

## 3. Color System

### 3.1 Viewer Color Tokens (CSS Variables → Tailwind)

All viewer colors are defined as **HSL CSS custom properties** in `:root` of `app/src/styles.css`, then referenced in `tailwind.config.js` via `hsl(var(--token))`. This enables Tailwind's opacity utilities (`bg-signal/50`).

| Tailwind Token | CSS Variable | HSL Value | Hex | Semantic Role |
|---|---|---|---|---|
| `void` | `--void` | `240 10% 4%` | `#09090B` | Main canvas/background |
| `void-900` | `--void-900` | `240 8% 8%` | — | Dark but not black |
| `void-800` | `--void-800` | `240 6% 12%` | — | Slightly lifted surface |
| `panel` | `--panel` | `240 5% 14%` | `#27272A` | Primary panel surfaces |
| `panel-raised` | `--panel-raised` | `240 6% 18%` | — | Active/hover surface |
| `border` | `--border` | `240 4% 26%` | `#3F3F46` | Standard borders |
| `border-strong` | `--border-strong` | `240 4% 38%` | — | Emphasized dividers |
| `border-faint` | `--border-faint` | `240 4% 18%` | — | Subtle separators |
| `ink` | `--ink` | `0 0% 98%` | `#FAFAFA` | Primary text |
| `ink-dim` | `--ink-dim` | `240 5% 72%` | `#A1A1AA` | Secondary/metadata text |
| `ink-mute` | `--ink-mute` | `240 4% 48%` | — | Tertiary/disabled text |
| `signal` | `--signal` | `271 91% 65%` | `#A855F7` | Primary accent (purple) |
| `signal-bright` | `--signal-bright` | `271 95% 75%` | — | Brighter accent |
| `pulse` | `--pulse` | `239 84% 70%` | `#6366F1` | Secondary accent (indigo) |
| `pulse-bright` | `--pulse-bright` | `239 88% 78%` | — | Brighter secondary |
| `alert` | `--alert` | `347 77% 56%` | `#F43F5E` | Error / rejected |
| `caution` | `--caution` | `38 92% 52%` | `#F59E0B` | Warning / attention |
| `confirm` | `--confirm` | `174 80% 42%` | `#14B8A6` | Success / completed |
| `dead` | `--dead` | `240 4% 38%` | — | Inactive / disabled |

**Usage pattern:** `className="text-[hsl(var(--ink))]"` or `className="bg-void"` (via Tailwind).

### 3.2 State Color Semantics

| State | Token | Usage |
|---|---|---|
| Live / Broadcast active | `signal` | Live indicators, active stingers, premium events |
| Sync / Info / Stream status | `pulse` | Sync LEDs, phase markers, notification states |
| Success / Confirmed | `confirm` | Approved actions, completed events (use sparingly) |
| Warning / Pending | `caution` | Queue delays, attention states |
| Error / Disconnected | `alert` | Failed calls, disconnected, rejected |
| Neutral / Idle | `dead` / `ink-mute` | Unknown state, waiting, disabled |

### 3.3 Role Color Mapping (Transcript/Courtroom)

| Role | Token | Hex | Usage |
|---|---|---|---|
| Judge | `caution` | `#F59E0B` | Authority marker |
| Prosecutor | `pulse` | `#6366F1` | Accusation color |
| Defense | `signal` | `#A855F7` | Counter-argument |
| Witness | `confirm` | `#14B8A6` | Testimony |
| Bailiff | `ink-dim` | `#A1A1AA` | Procedural |
| Jury | `pulse` | `#6366F1` | Deliberation |

### 3.4 Dashboard Color Tokens (Separate CSS Vars)

The dashboard uses its own CSS variable palette in `dashboard/src/index.css` (different aesthetic):

| CSS Variable | HSL | Hex | Purpose |
|---|---|---|---|
| `--bg` | `210 42% 7%` | — | Dashboard canvas |
| `--surface` | `212 38% 10%` | — | Panel surfaces |
| `--surface-2` | `212 34% 14%` | — | Raised surfaces |
| `--border` | `205 28% 23%` | — | Panel borders |
| `--text` | `205 40% 92%` | — | Primary text |
| `--muted` | `207 18% 64%` | — | Secondary text |
| `--cyan` | `190 92% 58%` | — | Primary accent |
| `--purple` | `260 75% 62%` | — | Secondary accent |
| `--red` | `3 89% 59%` | — | Error/danger |
| `--gold` | `38 68% 60%` | — | Warning |
| `--green` | `145 64% 50%` | — | Success |

**Dashboard background** is a complex radial gradient with a subtle scanline overlay pseudo-element:
```css
background:
  radial-gradient(circle at 15% 10%, hsl(var(--purple)/0.14), transparent 30%),
  radial-gradient(circle at 85% 0%, hsl(var(--cyan)/0.12), transparent 24%),
  radial-gradient(circle at 75% 80%, hsl(var(--gold)/0.08), transparent 28%),
  linear-gradient(180deg, hsl(var(--bg)) 0%, hsl(211 41% 5%) 100%);
```
With `body::before` adding a 5px-height soft-light scanline pattern at 10% opacity.

**IMPORTANT:** The dashboard uses the viewer's Tailwind color tokens (`void`, `panel`, `signal`, `pulse`, etc.) as its primary Tailwind class vocabulary, NOT its own CSS vars. Its own CSS vars set the ambient background/gradient atmosphere. Components inside dashboard panels use the same `hsl(var(--signal))` / `text-void` patterns from the viewer.

---

## 4. Typography System

### 4.1 Font Families

```js
// tailwind.config.js
fontFamily: {
  mono: ['"JetBrains Mono"', '"SF Mono"', '"Cascadia Code"', '"Fira Code"', 'monospace'],
  body: ['"JetBrains Mono"', '"SF Mono"', 'monospace'],
}
```

- **Viewer/Overlay:** `font-body` (JetBrains Mono) everywhere. `font-size: 0.8125rem` default.
- **Dashboard:** `font-family: 'Space Grotesk', 'Inter', system-ui, sans-serif` for body text. Monospace for code elements only.
- Both import **JetBrains Mono** from Google Fonts (400/500/600/700/800 weights + italic).

### 4.2 Font Size Scale

All custom sizes in `tailwind.config.js`:

| Token | Size | Line Height | Tracking | Usage |
|---|---|---|---|---|
| `text-2xs` | `0.625rem` (10px) | `1rem` | `0.06em` | Micro-labels, timestamps |
| `text-hud` | `0.6875rem` (11px) | `1.125rem` | `0.08em` | HUD labels, metadata |
| `text-sm` | `0.75rem` (12px) | `1.25rem` | `0.04em` | Secondary text |
| `text-base` | `0.875rem` (14px) | `1.5rem` | `0.02em` | Body text |
| `text-lg` | `1rem` (16px) | `1.625rem` | — | Emphasized body |
| `text-xl` | `1.125rem` (18px) | `1.75rem` | — | Subheadings |
| `text-2xl` | `1.375rem` (22px) | `1.875rem` | — | Section titles |
| `text-3xl` | `1.75rem` (28px) | `2.125rem` | — | Stinger titles |
| `text-4xl` | `2.25rem` (36px) | `2.5rem` | `-0.02em` | Large display |
| `text-5xl` | `3rem` (48px) | `3rem` | `-0.03em` | Hero / verdict |

### 4.3 Uppercase Micro-Label Pattern

All HUD labels follow this pattern:
```
text-2xs uppercase tracking-[0.10em] text-ink-mute
```
Variants with tighter tracking: `tracking-[0.12em]`, `tracking-[0.15em]`, `tracking-[0.20em]`.

Examples: `CURRENT PHASE`, `EVIDENCE LOCKED`, `JURY MANIFEST`, `COMMS LOG`, `SYNCED 01:42:09`.

---

## 5. Spacing Scale

```js
// tailwind.config.js — 4px base unit
spacing: {
  'hud': '0.25rem',  // 4px — micro gaps
  'px':  '1px',
  '0':   '0px',
  '1':   '0.25rem',  // 4px
  '2':   '0.5rem',   // 8px
  '3':   '0.75rem',  // 12px
  '4':   '1rem',     // 16px
  '5':   '1.25rem',  // 20px
  '6':   '1.5rem',   // 24px
  '8':   '2rem',     // 32px
  '10':  '2.5rem',   // 40px
  '12':  '3rem',     // 48px
  '16':  '4rem',     // 64px
  '20':  '5rem',     // 80px
  '24':  '6rem',     // 96px
}
```

**Common spacing patterns:**
- Panel padding: `p-3` (12px) or `p-4` (16px)
- Gap between panels: `gap-4` (16px)
- Gap between items within a panel: `space-y-2` (8px) or `space-y-1` (4px)
- Section header padding: `py-0.5` + `border-b` + `mb-3`
- Transcript row padding: `py-1` (4px vertical)
- Border width: `border` (1px default), `border-2`, `border-3` (custom 3px for stinger frames)

---

## 6. Border System

```js
borderWidth: { '3': '3px' }  // custom addition for stinger frames
```

**Standard panel border:** `border border-border-faint`
**Active/hover border:** `border border-pulse`
**Stinger frame:** `border-3` + accent color
**Role-colored border (transcripts):** `border-l-2` or `border-r-2` with inline style `borderColor: roleColor`
**Focus ring:** `focus-visible:ring-1 focus-visible:ring-[hsl(var(--pulse))]`

Border radius is effectively `0` (Tailwind default). Hard corners everywhere. The style guide specifies `0–2px` max.

---

## 7. Layout Architecture

### 7.1 16:9 Framing Container

Both viewer and overlay wrap content in a **16:9 aspect-ratio container**:

```html
<div class="grid min-h-screen place-items-center overflow-hidden bg-void text-ink font-body">
  <div class="relative aspect-video w-screen max-w-[calc(100vh*16/9)] overflow-hidden border border-border-faint hud-bracket">
    <!-- content -->
  </div>
</div>
```

- `aspect-video` = 16/9 ratio
- `max-w-[calc(100vh*16/9)]` prevents overflow on ultrawide screens
- `hud-bracket` adds corner bracket decorations via pseudo-elements
- This container is the **only viewport-level scroll container**

### 7.2 Viewer Layout (App Shell)

```
┌──────────────────────────────────────────────┐
│ HEADER BAR: logo v0.1 · LED · mode · [ADMIN]│
├──────────────────────────────────────────────┤
│ TAB BAR: [Dashboard] [Transcripts] [Submit]  │
│          [About]                              │
├──────────────────────────────────────────────┤
│                                              │
│              VIEW CONTENT AREA                │
│                                              │
└──────────────────────────────────────────────┘
```

- Header: `border-b border-border-faint bg-panel px-4 py-3`
- Tab bar: `px-4 flex gap-2` with `TabButton` components
- Content: `flex-1 px-4 pb-4` with `hidden`/visible tab panels

### 7.3 Overlay Layout (Broadcast)

```
┌──────────────────────────────────────────────┐
│ STATUS BAR: JURYRIGGED · LIVE · PHASE · UPT  │
├──────────────────────────────────────────────┤
│ CASE FILE HEADER: topic + prompt              │
├────────────────────────┬─────────────────────┤
│                        │  JURY MANIFEST       │
│    COMMS LOG           │  (6 jurors)          │
│    (transcript feed)   ├─────────────────────┤
│                        │  SIGNALS (Twitch)    │
│                        ├─────────────────────┤
│                        │  QUEUE STATUS        │
│                        ├─────────────────────┤
│                        │  EVIDENCE / OBJ /    │
│                        │  CASE TYPE           │
└────────────────────────┴─────────────────────┘
```

- Left column: `flex-1 flex flex-col min-w-0` with `border-r border-border-faint`
- Right sidebar: `w-[320px] flex flex-col min-h-0 bg-void-800 overflow-y-auto`
- Height calculation: `height: calc(100% - 146px)` (minus status bar + case header)
- Stinger overlay: `absolute inset-0 z-30` with `animate-stinger-shake`

### 7.4 Dashboard Layout (Operator)

```
┌──────────────────────────────────────────────┐
│ TOP BAR: logo · session selector · links     │
├──────────────────────────────────────────────┤
│ TAB BAR: [Monitor][Broadcast][Moderation]    │
│          [LLM Audit][Ops][Recap][Controls]   │
│          [Case Queue][Analytics]             │
├──────────────────────────────────────────────┤
│              TAB PANEL CONTENT                │
│  (lazy-loaded per tab)                       │
└──────────────────────────────────────────────┘
```

- Full-height layout with top bar + tab bar + content area
- Tabs are horizontal buttons with "active" state
- No 16:9 constraint on dashboard — fills viewport

### 7.5 Responsive Behavior

- **Viewer/Overlay:** Content at ≥1280px gets `overlay-safe` padding bump (`1rem` → `2rem`) + safe-area-inset support
- **Dashboard:** Sidebar columns use `xl:grid-cols-[1fr_280px]` breakpoint. Below `xl`, single column.
- **Transcripts:** `xl:grid-cols-[320px_1fr]` — search sidebar collapses to full-width below breakpoint.
- **Overlay-safe:** CSS `env(safe-area-inset-*)` for mobile notch/browser chrome.
- **Overlay only renders at 16:9** — letterboxed on non-16:9 viewports.

### 7.6 Content-Grid Pattern

Two-column content areas use:
```html
<div class="grid gap-4 xl:grid-cols-[1fr_280px]">
  <div class="space-y-4"><!-- main content --></div>
  <div class="space-y-4"><!-- sidebar --></div>
</div>
```

Three-column patterns not used. All two-column layouts collapse to single-column below `xl` (1280px).

---

## 8. Component Library

### 8.1 Primitive Components

All primitives are defined in `app/src/components.tsx`. They are shared between all viewer views.

#### `ConsolePanel`
```tsx
function ConsolePanel({ className, children }) {
  return <div className={cn('border border-border-faint bg-panel', className)}>{children}</div>
}
```
- Base panel. Always `border border-border-faint bg-panel`.
- Content typically gets `p-4` padding via `className`.

#### `HudSection`
```tsx
function HudSection({ label, note }) {
  return (
    <div className="hud-section">
      <span className="hud-section-label">{label}</span>
      <span className="hud-section-line" />
      {note ? <span className="text-2xs text-ink-mute">{note}</span> : null}
    </div>
  )
}
```
- CSS classes: `hud-section` (flex, gap, border-bottom), `hud-section-label` (signal-colored, uppercase, tracking), `hud-section-line` (flex-1 divider line).

#### `StatusLed`
```tsx
function StatusLed({ state }) {
  // state: 'live' | 'sync' | 'ok' | 'warn' | 'dead'
  // CSS: .hud-led (6×6px, border-radius 1px, dead bg)
  //      .hud-led-live → alert color + glow box-shadow
  //      .hud-led-sync → pulse color + glow
  //      .hud-led-ok → confirm color + glow
  //      .hud-led-warn → caution color + glow
}
```

#### `HudBadge`
```tsx
function HudBadge({ children, tone = 'ink-dim' }) {
  // Inline: border-color + color both use hsl(var(--tone))
  // Tailwind: inline-flex items-center border px-1.5 py-0 text-2xs uppercase tracking-[0.12em]
}
```

#### `TabButton`
```tsx
function TabButton({ active, label, note, onClick, ...a11yProps }) {
  // role="tab", aria-selected, aria-controls, tabIndex management
  // Active: border-pulse bg-panel-raised
  // Inactive: border-border-faint bg-panel hover:border-pulse hover:bg-panel-raised
  // Focus: focus-visible:ring-1 focus-visible:ring-pulse
  // Transition: duration-100
}
```

#### `HudRow`
```tsx
function HudRow({ label, value, accent }) {
  // CSS: .hud-row (flex, baseline, gap)
  //      .hud-row-key (muted, uppercase, tracking, monospace, ::after→":")
  //      .hud-row-val (ink, font-weight 500)
  // accent prop: inline color style on value
}
```

#### `TranscriptRow`
```tsx
function TranscriptRow({ speaker, role, dialogue, turnNumber, phase, alignRight, roleColor }) {
  // Alternates left/right justification per `alignRight`
  // Left: border-l-2 pl-3 | Right: border-l-0 border-r-2 pl-0 pr-3
  // Role tag: [PROS] [DEFN] [WITN] [JUDG] [BAIL] [JURY] (5-char uppercase, role-colored)
  // Metadata line: #turnNumber · phase (2xs, uppercase, tracking)
  // Body: text-sm leading-relaxed text-ink-dim
  // Max width: max-w-[88%]
}
```

#### `CaseCard`
```tsx
function CaseCard({ item, active, onClick }) {
  // Full-width button with docket (signal-colored, uppercase), title, summary, tags
  // Risk badge: alert=Elevated, caution=Moderate, ink-mute=Low
  // Active: border-pulse bg-panel-raised
  // Inactive: border-border-faint bg-panel hover:border-pulse
}
```

#### `EvidenceRow`
```tsx
function EvidenceRow({ item }) {
  // border border-border-faint bg-panel p-3
  // Header: label (ink, semibold), type·source (ink-dim), badge (caution)
  // Body: summary (ink-dim), confidence (ink-mute, uppercase)
}
```

#### `VoteCard`
```tsx
function VoteCard({ option }) {
  // Disabled: cursor-not-allowed, opacity-60, border-border-faint
  // Enabled: bg-panel, hover:border-pulse
  // Badge: confirm=AVAILABLE, alert=UNAVAILABLE
}
```

#### `JuryRow`
```tsx
function JuryRow({ juror }) {
  // flex row with dot indicator (color-coded by status), juror label, name, note
  // Separator: border-b border-border-faint/0.4 last:border-0
}
```

### 8.2 Dashboard-Specific Components

Dashboard components are in `dashboard/src/components/` and use the same Tailwind token vocabulary as the viewer but live in the dashboard's ambient gradient environment.

| Component | File | Purpose |
|---|---|---|
| `SessionMonitor` | `SessionMonitor.tsx` | Live session summary with evidence cards, objection count, vote tallies, event feed |
| `ModerationQueue` | `ModerationQueue.tsx` | Approve/reject flagged content with filter tabs |
| `ManualControls` | `ManualControls.tsx` | Create sessions, advance phases |
| `AdminTriggers` | `AdminTriggers.tsx` | Send overlay messages and stingers |
| `CaseQueue` | `CaseQueue.tsx` | Queue management, automation toggle |
| `LLMAuditLog` | `LLMAuditLog.tsx` | Searchable LLM call audit trail |
| `OpsMetrics` | `OpsMetrics.tsx` | Prometheus/health metrics dashboard |
| `ObjectionCounter` | `ObjectionCounter.tsx` | Animated objection tally widget |
| `EvidenceCard` | `EvidenceCard.tsx` | Animated evidence reveal card |
| `Analytics` | `Analytics.tsx` | Event statistics with phase/type breakdowns |

### 8.3 Scene Components (PixiJS / Canvas)

The courtroom scene system is a canvas-based layer within the overlay:

| Component | Purpose |
|---|---|
| `CourtStage` (`Stage.tsx`) | 16:9 courtroom with character sprites, camera presets |
| `DialogueBox` (`DialogueBox.tsx`) | Click-to-advance typewriter dialogue overlay |
| `FXOverlay` (`FXOverlay.tsx`) | Full-screen flash/shake/stamp effects |
| `useSceneRunner` (`runner.ts`) | Queue-based event runner with auto-advance |
| `useCourtStage` (`useCourtStage.ts`) | Camera/pose/speaker state hook |

Scene system is activated by a `SCENE` toggle button in the overlay status bar, or auto-activated when `render_directive` SSE events arrive.

---

## 9. HUD CSS Utility Classes

These are custom CSS classes in `app/src/styles.css` that are NOT Tailwind utilities:

| Class | Purpose |
|---|---|
| `.hud-bracket` | Corner bracket decorations on all 4 corners (pseudo-elements, 12×12px, border-colored) |
| `.hud-bracket-tl/tr/bl/br` | Single-corner bracket variants |
| `.hud-bracket-accent` | Bracket corners in `signal` color instead of `border` |
| `.hud-prompt` | `::before` adds `▸ ` marker in `signal` color |
| `.hud-led` | 6×6px LED dot base (border-radius 1px, `dead` bg) |
| `.hud-led-live/sync/ok/warn` | LED color + glow variants |
| `.hud-cursor` | `::after` blinking `█` block cursor in `signal` color |
| `.hud-scan` | Full-screen scanline overlay (repeating linear gradient + bottom fade) |
| `.hud-row` / `.hud-row-key` / `.hud-row-val` | Terminal-style key:value row |
| `.hud-section` / `.hud-section-label` / `.hud-section-line` | Section header with decorative line |
| `.hud-statusbar` | Status bar (void-800 bg, border-bottom, uppercase, tracking) |
| `.hud-panel` / `.hud-panel-raised` | Panel surfaces (panel/panel-raised bg + border) |
| `.hud-divide` | Horizontal rule divider |
| `.hud-badge` | Inline badge/chip (inline-flex, border, uppercase, 2xs) |
| `.hud-transcript-entry` / `.hud-transcript-meta` / `.hud-transcript-speaker` / `.hud-transcript-dialogue` | CSS Grid transcript layout |
| `.overlay-safe` | Viewport-safe padding with env(safe-area-inset-*) fallback |

### Scrollbar Styling

```css
::-webkit-scrollbar       { width: 6px; height: 6px; }
::-webkit-scrollbar-track { background: hsl(var(--void-800)); }
::-webkit-scrollbar-thumb { background: hsl(var(--border)); border-radius: 1px; }
::-webkit-scrollbar-thumb:hover { background: hsl(var(--border-strong)); }
```

---

## 10. Animation & Motion

### 10.1 Tailwind Keyframes/Animations

```js
animation: {
  'blink':         'blink 1s step-end infinite',         // cursor blink
  'slide-in-right': 'slideInRight 0.2s ease-out',        // panel entry from right
  'slide-in-up':   'slideInUp 0.2s ease-out',            // panel entry from below
  'stinger-shake': 'stingerShake 0.3s ease-out',         // objection/high-impact shake
  'scan':          'scan 3s linear infinite',            // scanline sweep
  'pulse-slow':    'pulse 3s ease-in-out infinite',      // slow pulse for waiting states
},
```

**Keyframe details:**
- `blink`: 50% opacity toggle (step-end for hard on/off)
- `slideInRight`: translateX(1rem)→0, opacity 0→1
- `slideInUp`: translateY(0.5rem)→0, opacity 0→1
- `stingerShake`: ±6px → ±4px horizontal bounce sequence
- `scan`: translateY(-100%) → translateY(400%) vertical sweep

### 10.2 Motion Duration Guidelines (from style guide)

| Motion | Duration |
|---|---|
| Hover/focus | 120–160ms |
| Panel reveal | 180–260ms |
| Stinger entry | 240–360ms |
| Stinger exit | 180–240ms |
| Count-up animation | 300–500ms |

**Allowed motion types:** snap-in slide, hard wipe, terminal cursor blink, step reveal, count-up numbers, frame-lock stinger, brief shake for objections.

**Forbidden:** elastic bounce, particle spam, constant flicker, long cinematic transitions, glowing trails.

### 10.3 Reduced Motion

```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    scroll-behavior: auto !important;
    transition-duration: 0.01ms !important;
  }
}
```

Also use `motion-safe:` prefix on animations: `motion-safe:animate-stinger-shake`.

---

## 11. Interaction States

| State | Treatment |
|---|---|
| **Default** | Dark panel (`bg-panel`), neutral border (`border-border-faint`) |
| **Hover** | Brighter border (`border-pulse`), raised bg (`bg-panel-raised`), `transition duration-100` |
| **Active/Selected** | Pulse border + raised bg + matching label color |
| **Focus** | `focus-visible:ring-1 focus-visible:ring-[hsl(var(--pulse))]` (2-3px solid ring) |
| **Disabled** | `cursor-not-allowed`, `opacity-60` (or `opacity-40`), reduced contrast |
| **Error** | `border-alert` or `text-alert` |
| **Warning** | `text-caution` label/edge |
| **Success** | `text-confirm` or `bg-confirm` |

**Rule:** Never use color alone. Always pair with text labels, icons (LEDs), or motion.

---

## 12. Accessibility Patterns

- Transcript logs: `role="log" aria-live="polite" aria-relevant="additions text"`
- Tab buttons: `role="tab" aria-selected={active} aria-controls={panelId}` with keyboard arrows (ArrowLeft/ArrowRight/Home/End)
- Tab panels: `role="tabpanel" aria-labelledby={tabId}` with `hidden` attribute
- State indicators: LEDs have `aria-hidden="true"`; always accompanied by text labels
- Focus management: `focus-visible:` ring on all interactive elements, `tabIndex` management for tab navigation
- Vote cards: `aria-describedby` linking to reason text
- Motion: `prefers-reduced-motion` respected globally; `motion-safe:` prefix on animations
- No autoplay without user activation

---

## 13. Tailwind Configuration (Complete)

```js
// tailwind.config.js
/** @type {import('tailwindcss').Config} */
export default {
  content: [
    './app/index.html',
    './app/src/**/*.{ts,tsx}',
    './dashboard/index.html',
    './dashboard/**/*.{js,ts,jsx,tsx}',
  ],
  theme: {
    extend: {
      colors: {
        void:       { DEFAULT: 'hsl(var(--void))',        900: 'hsl(var(--void-900))',        800: 'hsl(var(--void-800))' },
        panel:      { DEFAULT: 'hsl(var(--panel))',       raised: 'hsl(var(--panel-raised))' },
        border:     { DEFAULT: 'hsl(var(--border))',      strong: 'hsl(var(--border-strong))', faint: 'hsl(var(--border-faint))' },
        ink:        { DEFAULT: 'hsl(var(--ink))',         dim: 'hsl(var(--ink-dim))',          mute: 'hsl(var(--ink-mute))' },
        signal:     { DEFAULT: 'hsl(var(--signal))',      bright: 'hsl(var(--signal-bright))' },
        pulse:      { DEFAULT: 'hsl(var(--pulse))',       bright: 'hsl(var(--pulse-bright))' },
        alert:      { DEFAULT: 'hsl(var(--alert))' },
        caution:    { DEFAULT: 'hsl(var(--caution))' },
        confirm:    { DEFAULT: 'hsl(var(--confirm))' },
        dead:       { DEFAULT: 'hsl(var(--dead))' },
      },
      fontFamily: {
        mono: ['"JetBrains Mono"', '"SF Mono"', '"Cascadia Code"', '"Fira Code"', 'monospace'],
        body: ['"JetBrains Mono"', '"SF Mono"', 'monospace'],
      },
      fontSize: {
        '2xs': ['0.625rem',  { lineHeight: '1rem',      letterSpacing: '0.06em' }],
        hud:   ['0.6875rem', { lineHeight: '1.125rem',  letterSpacing: '0.08em' }],
        sm:    ['0.75rem',   { lineHeight: '1.25rem',   letterSpacing: '0.04em' }],
        base:  ['0.875rem',  { lineHeight: '1.5rem',    letterSpacing: '0.02em' }],
        lg:    ['1rem',      { lineHeight: '1.625rem' }],
        xl:    ['1.125rem',  { lineHeight: '1.75rem' }],
        '2xl': ['1.375rem',  { lineHeight: '1.875rem' }],
        '3xl': ['1.75rem',   { lineHeight: '2.125rem' }],
        '4xl': ['2.25rem',   { lineHeight: '2.5rem',    letterSpacing: '-0.02em' }],
        '5xl': ['3rem',      { lineHeight: '3rem',      letterSpacing: '-0.03em' }],
      },
      spacing: {
        hud: '0.25rem', px: '1px',
        0: '0px', 1: '0.25rem', 2: '0.5rem', 3: '0.75rem', 4: '1rem',
        5: '1.25rem', 6: '1.5rem', 8: '2rem', 10: '2.5rem',
        12: '3rem', 16: '4rem', 20: '5rem', 24: '6rem',
      },
      borderWidth: { '3': '3px' },
      animation: {
        blink:         'blink 1s step-end infinite',
        'slide-in-right': 'slideInRight 0.2s ease-out',
        'slide-in-up':   'slideInUp 0.2s ease-out',
        'stinger-shake': 'stingerShake 0.3s ease-out',
        scan:          'scan 3s linear infinite',
        'pulse-slow':  'pulse 3s ease-in-out infinite',
      },
      keyframes: {
        blink:        { '0%,100%': { opacity: '1' }, '50%': { opacity: '0' } },
        slideInRight: { '0%': { transform: 'translateX(1rem)', opacity: '0' }, '100%': { transform: 'translateX(0)', opacity: '1' } },
        slideInUp:    { '0%': { transform: 'translateY(0.5rem)', opacity: '0' }, '100%': { transform: 'translateY(0)', opacity: '1' } },
        stingerShake: { '0%,100%': { transform: 'translateX(0)' }, '20%': { transform: 'translateX(-6px)' }, '40%': { transform: 'translateX(6px)' }, '60%': { transform: 'translateX(-4px)' }, '80%': { transform: 'translateX(4px)' } },
        scan:         { '0%': { transform: 'translateY(-100%)' }, '100%': { transform: 'translateY(400%)' } },
      },
    },
  },
  plugins: [],
};
```

---

## 14. CSS Variable Schemas

### 14.1 Viewer (app/src/styles.css)

```css
:root {
  --void:         240 10% 4%;
  --void-900:     240 8% 8%;
  --void-800:     240 6% 12%;
  --panel:        240 5% 14%;
  --panel-raised: 240 6% 18%;
  --border:         240 4% 26%;
  --border-strong:  240 4% 38%;
  --border-faint:   240 4% 18%;
  --ink:          0 0% 98%;
  --ink-dim:      240 5% 72%;
  --ink-mute:     240 4% 48%;
  --signal:       271 91% 65%;
  --signal-bright:271 95% 75%;
  --pulse:        239 84% 70%;
  --pulse-bright: 239 88% 78%;
  --alert:        347 77% 56%;
  --caution:      38 92% 52%;
  --confirm:      174 80% 42%;
  --dead:         240 4% 38%;
  color-scheme: dark;
}
```

### 14.2 Dashboard (dashboard/src/index.css)

```css
:root {
  color-scheme: dark;
  --bg:       210 42% 7%;
  --surface:  212 38% 10%;
  --surface-2:212 34% 14%;
  --border:   205 28% 23%;
  --text:     205 40% 92%;
  --muted:    207 18% 64%;
  --cyan:     190 92% 58%;
  --purple:   260 75% 62%;
  --red:      3 89% 59%;
  --gold:     38 68% 60%;
  --green:    145 64% 50%;
}
```

---

## 15. Common Tailwind Class Patterns

### 15.1 Panel with Section Header

```html
<div className="border border-[hsl(var(--border-faint))] bg-[hsl(var(--panel))] p-4">
  <div className="hud-section">
    <span className="hud-section-label">SECTION NAME</span>
    <span className="hud-section-line" />
    <span className="text-2xs text-[hsl(var(--ink-mute))]">optional note</span>
  </div>
  <!-- panel content with space-y-2 -->
</div>
```

### 15.2 Interactive Card (Selectable)

```html
<button className="w-full border p-3 text-left transition duration-100
  border-[hsl(var(--border-faint))] bg-[hsl(var(--panel))]
  hover:border-[hsl(var(--pulse))]
  focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[hsl(var(--pulse))]">
```

When active/selected: swap `border-[hsl(var(--border-faint))]` for `border-[hsl(var(--pulse))]` and `bg-[hsl(var(--panel))]` for `bg-[hsl(var(--panel-raised))]`.

### 15.3 Status Bar Row

```html
<div className="flex items-center gap-6 px-4 py-1.5 border-b border-[hsl(var(--border-faint))] bg-[hsl(var(--void-800))] text-2xs uppercase tracking-[0.1em] text-[hsl(var(--ink-dim))]">
  <span className="text-[hsl(var(--signal))] font-semibold">BRAND</span>
  <StatusLed state="sync" />
  <span>STATUS</span>
  <span className="text-[hsl(var(--ink-mute))]">|</span>
  <!-- more segments -->
  <span className="flex-1" /> <!-- spacer -->
  <span>TIMESTAMP</span>
</div>
```

### 15.4 Input Field

```html
<input className="w-full border border-[hsl(var(--border-faint))] bg-[hsl(var(--void-800))] px-3 py-1.5 text-xs text-[hsl(var(--ink))] outline-none placeholder:text-[hsl(var(--ink-mute))] focus:border-[hsl(var(--pulse))]" />
```

### 15.5 Accent Button

```html
<button className="w-full border border-[hsl(var(--pulse))] bg-[hsl(var(--pulse)/0.12)] px-3 py-2 text-sm font-semibold uppercase tracking-[0.12em] text-[hsl(var(--pulse))] hover:bg-[hsl(var(--pulse)/0.24)] disabled:opacity-40 disabled:cursor-not-allowed">
  ACTION LABEL
</button>
```

### 15.6 Transcript Row (Role-Colored)

```html
<article className="flex w-full py-1 justify-start text-left">
  <div className="max-w-[88%] border-l-2 pl-3" style={{ borderColor: roleColor }}>
    <p className="text-xs font-semibold" style={{ color: roleColor }}>
      [ROLE] <span className="text-[hsl(var(--ink))]">Speaker Name</span>
    </p>
    <p className="text-2xs uppercase tracking-[0.1em] text-[hsl(var(--ink-mute))] mt-0.5">
      #turnNumber · phase
    </p>
    <p className="mt-1 text-sm leading-relaxed text-[hsl(var(--ink-dim))]">
      Dialogue text here.
    </p>
  </div>
</article>
```

### 15.7 Stinger Overlay

```html
<div className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center motion-safe:animate-stinger-shake">
  <div className="border-3 px-8 py-6 bg-[hsl(var(--void-900))] border-[hsl(var(--signal))]">
    <p className="text-xs uppercase tracking-[0.2em] text-[hsl(var(--signal))] hud-prompt">COURT STINGER</p>
    <p className="mt-3 text-3xl font-bold text-[hsl(var(--ink))]">TITLE</p>
    <p className="mt-2 text-base text-[hsl(var(--ink-dim))]">Message</p>
  </div>
</div>
```

---

## 16. Implementation Checklist (Recreation Order)

1. **Set up Tailwind** with the exact config from §13
2. **Define CSS variables** — viewer tokens in `:root` of main CSS file (§14.1)
3. **Import JetBrains Mono** from Google Fonts (400/500/600/700/800 + italic)
4. **Apply base reset** (void background, mono font, antialiased, border-box)
5. **Build CSS utility classes** (§9): brackets, LEDs, prompt marker, cursor, scanlines, sections, rows, panels, badges, transcript grid, scrollbars, safe-area
6. **Build primitive components** (§8.1): `ConsolePanel`, `HudSection`, `StatusLed`, `HudBadge`, `TabButton`, `HudRow`, `TranscriptRow`, `CaseCard`, `EvidenceRow`, `VoteCard`, `JuryRow`, `cn` utility
7. **Build layout shell** (§7.2): 16:9 framing container + header + tab bar + tab panel content area
8. **Build overlay** (§7.3): status bar, case header, two-column layout (transcript log + sidebar with jury/signals/queue/metadata), stinger overlay
9. **Build overlay standby state**: centered 16:9 frame with "AWAITING SIGNAL" message and system info footer
10. **Build dashboard** (§7.4): full-height layout, tab system with lazy loading, dark gradient background with scanline overlay
11. **Implement all interaction states** (§11): hover, active, focus, disabled with transitions
12. **Add reduced motion** support (§10.3)
13. **Add accessibility** (§12): ARIA roles, keyboard navigation, text labels alongside color
14. **Test at** 2560×1440, 1920×1080, 1280×720

---

## 17. Dependency Requirements

```json
{
  "devDependencies": {
    "tailwindcss": "^3.x",
    "autoprefixer": "^10.x",
    "postcss": "^8.x"
  },
  "dependencies": {
    "react": "^18.x",
    "react-dom": "^18.x"
  }
}
```

PostCSS config (`postcss.config.js`):
```js
export default {
  plugins: {
    tailwindcss: {},
    autoprefixer: {},
  },
}
```

---

## 18. Key Design Decisions

1. **CSS variables as single source of truth** — Tailwind tokens all reference `hsl(var(--token))`, making theming trivial (swap `:root` values).
2. **No component library** — no Radix, no shadcn, no Headless UI. Pure Tailwind + React. All components are hand-built.
3. **Two distinct visual treatments** — viewer/overlay is strict neubrutalist (flat, hard, no gradients); dashboard has atmospheric dark gradients + scanlines for a control-room feel. Both share the same Tailwind color token vocabulary.
4. **16:9 is the only layout** for viewer-facing content. Overlay is letterboxed, not responsive.
5. **Role colors are inline styles** — `style={{ borderColor: roleColor }}` — because they're dynamic based on runtime role data. Same for badge tones.
6. **No CSS modules or CSS-in-JS** — one global CSS file per SPA + Tailwind utilities.
7. **Emoji used sparingly** in dashboard phase buttons only. Nowhere else in the UI system.
