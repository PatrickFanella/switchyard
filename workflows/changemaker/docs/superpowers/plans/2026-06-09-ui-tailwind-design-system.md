# UI Tailwind Design System Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Rebuild the Changemaker admin web UI around the `docs/ui-tailwind-spec.md` neubrutal HUD design system using semantic Tailwind tokens and reusable utility-class primitives.

**Architecture:** This repo has one Vite React SPA under `web/`, so the imported two-SPA spec is adapted into a single operator-control-room interface. `web/src/styles.css` owns Tailwind v4 CSS-first theme tokens and HUD utility classes; `web/src/components/ui.tsx` owns small reusable React primitives. Existing feature panels keep their state/API behavior and are refactored to consume semantic utilities such as `bg-panel`, `text-ink`, `border-border-faint`, `text-signal`, `text-pulse`, `text-alert`, and `text-confirm`.

**Tech Stack:** React 19, Vite 8, Tailwind CSS 4 via `@tailwindcss/vite`, TypeScript, global CSS custom properties, utility classes.

---

## File Structure

- Modify: `web/src/styles.css` — define design tokens with HSL CSS variables, Tailwind v4 `@theme` entries, HUD utility classes, markdown styling, scrollbars, and reduced-motion handling.
- Create: `web/src/components/ui.tsx` — reusable control-room primitives: `cn`, `Panel`, `PanelHeader`, `SectionIntro`, `NoticeBanner`, `Button`, `Input`, `Textarea`, `MicroLabel`, `Badge`, `StatusLed`, `TabButton`, `SelectableCard`, `EmptyState`, `MetaCard`, and `CodeBlock`.
- Modify: `web/src/App.tsx` — replace glassy shell, rounded pills, cyan/slate classes, and ad hoc tabs with semantic HUD shell and primitives.
- Modify: `web/src/components/PostReader.tsx` — refactor post list, markdown renderer, empty states, links, code, and quote styles to semantic tokens.
- Modify: `web/src/components/PromptEditor.tsx` — refactor prompt list, save controls, textarea, notices, and panel layout to primitives.
- Modify: `web/src/components/ConfigEditor.tsx` — refactor runtime config form fields, toggles, save controls, and notices to primitives.
- Modify: `web/src/components/RunControl.tsx` — refactor run trigger, segmented control, status cards, logs, and messages to primitives.
- Modify: `web/src/components/ServiceControl.tsx` — refactor confirmation cards, destructive buttons, service outputs, and status panels to primitives.
- Keep: `docs/ui-tailwind-spec.md` — source spec and recreation reference.

## Scope Decisions

- Implement the semantic design system for the current Changemaker admin SPA only.
- Do not add a separate viewer SPA, dashboard SPA, PixiJS/court scene, or query-param routing from the imported JuryRigged spec.
- Use Tailwind v4 CSS-first theme tokens instead of adding `tailwind.config.js`, because this repo already uses `@tailwindcss/vite` and has no Tailwind config.
- Use flat surfaces, hard borders, minimal radius, hard offset shadows, and semantic token utilities. Avoid gradients inside panels, blur, glassmorphism, rounded pills, and cyan/slate one-off palette classes.

---

### Task 1: Commit the Spec and Plan

**Files:**
- Add: `docs/ui-tailwind-spec.md`
- Add: `docs/superpowers/plans/2026-06-09-ui-tailwind-design-system.md`

- [ ] **Step 1: Inspect status**

Run: `rtk git status --short`

Expected: only `docs/ui-tailwind-spec.md` and this plan are uncommitted before implementation starts.

- [ ] **Step 2: Commit planning artifacts**

Run:

```bash
rtk git add docs/ui-tailwind-spec.md docs/superpowers/plans/2026-06-09-ui-tailwind-design-system.md
rtk git commit -m "docs: plan HUD Tailwind design system"
```

Expected: commit succeeds and implementation code can be committed separately.

---

### Task 2: Add CSS Tokens and HUD Utilities

**Files:**
- Modify: `web/src/styles.css`

- [ ] **Step 1: Replace global CSS with semantic token system**

Implement:

```css
@import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:ital,wght@0,400;0,500;0,600;0,700;0,800;1,400&display=swap');
@import 'tailwindcss';

:root {
  --void: 240 10% 4%;
  --void-900: 240 8% 8%;
  --void-800: 240 6% 12%;
  --panel: 240 5% 14%;
  --panel-raised: 240 6% 18%;
  --border: 240 4% 26%;
  --border-strong: 240 4% 38%;
  --border-faint: 240 4% 18%;
  --ink: 0 0% 98%;
  --ink-dim: 240 5% 72%;
  --ink-mute: 240 4% 48%;
  --signal: 271 91% 65%;
  --signal-bright: 271 95% 75%;
  --pulse: 239 84% 70%;
  --pulse-bright: 239 88% 78%;
  --alert: 347 77% 56%;
  --caution: 38 92% 52%;
  --confirm: 174 80% 42%;
  --dead: 240 4% 38%;
  color-scheme: dark;
}

@theme inline {
  --color-void: hsl(var(--void));
  --color-void-900: hsl(var(--void-900));
  --color-void-800: hsl(var(--void-800));
  --color-panel: hsl(var(--panel));
  --color-panel-raised: hsl(var(--panel-raised));
  --color-border: hsl(var(--border));
  --color-border-strong: hsl(var(--border-strong));
  --color-border-faint: hsl(var(--border-faint));
  --color-ink: hsl(var(--ink));
  --color-ink-dim: hsl(var(--ink-dim));
  --color-ink-mute: hsl(var(--ink-mute));
  --color-signal: hsl(var(--signal));
  --color-signal-bright: hsl(var(--signal-bright));
  --color-pulse: hsl(var(--pulse));
  --color-pulse-bright: hsl(var(--pulse-bright));
  --color-alert: hsl(var(--alert));
  --color-caution: hsl(var(--caution));
  --color-confirm: hsl(var(--confirm));
  --color-dead: hsl(var(--dead));
  --font-body: 'JetBrains Mono', 'SF Mono', 'Cascadia Code', 'Fira Code', monospace;
  --font-mono: 'JetBrains Mono', 'SF Mono', 'Cascadia Code', 'Fira Code', monospace;
  --text-2xs: 0.625rem;
  --text-2xs--line-height: 1rem;
  --text-2xs--letter-spacing: 0.06em;
  --text-hud: 0.6875rem;
  --text-hud--line-height: 1.125rem;
  --text-hud--letter-spacing: 0.08em;
  --shadow-hud: 8px 8px 0 hsl(var(--void));
  --animate-blink: blink 1s step-end infinite;
  --animate-slide-in-right: slideInRight 0.2s ease-out;
  --animate-slide-in-up: slideInUp 0.2s ease-out;
  --animate-stinger-shake: stingerShake 0.3s ease-out;
  --animate-scan: scan 3s linear infinite;
}
```

Also define `.hud-panel`, `.hud-section`, `.hud-badge`, `.hud-led`, `.hud-bracket`, `.control-input`, `.markdown`, scrollbar styling, and reduced-motion rules.

- [ ] **Step 2: Verify CSS-first Tailwind build**

Run: `rtk pnpm build:web`

Expected: Tailwind recognizes `bg-panel`, `text-ink`, `border-border-faint`, `shadow-hud`, `font-body`, `text-2xs`, and animation utilities.

- [ ] **Step 3: Commit CSS tokens**

Run:

```bash
rtk git add web/src/styles.css
rtk git commit -m "feat: add HUD Tailwind tokens"
```

---

### Task 3: Add Shared UI Primitives

**Files:**
- Create: `web/src/components/ui.tsx`

- [ ] **Step 1: Create reusable semantic primitives**

Create `web/src/components/ui.tsx` with typed primitives that use semantic utilities, including:

```tsx
export function cn(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(' ');
}
```

Components must include `Panel`, `PanelHeader`, `SectionIntro`, `NoticeBanner`, `Button`, `Input`, `Textarea`, `MicroLabel`, `Badge`, `StatusLed`, `TabButton`, `SelectableCard`, `EmptyState`, `MetaCard`, and `CodeBlock`.

- [ ] **Step 2: Verify types**

Run: `rtk pnpm format`

Expected: TypeScript passes for the CLI project and web project.

- [ ] **Step 3: Commit primitives**

Run:

```bash
rtk git add web/src/components/ui.tsx
rtk git commit -m "feat: add admin UI primitives"
```

---

### Task 4: Refactor the Admin Shell and Tabs

**Files:**
- Modify: `web/src/App.tsx`

- [ ] **Step 1: Replace shell styling**

Use `font-body`, `bg-void`, `text-ink`, `hud-scan`, `Panel`, `StatusLed`, `Badge`, `Input`, `Button`, and `TabButton`.

- [ ] **Step 2: Preserve behavior**

Ensure token save, token clear, unauthorized reset, banner display, tab switching, `role="tablist"`, `role="tab"`, and `aria-selected` still work.

- [ ] **Step 3: Verify web build**

Run: `rtk pnpm build:web`

Expected: build succeeds.

- [ ] **Step 4: Commit shell refactor**

Run:

```bash
rtk git add web/src/App.tsx
rtk git commit -m "feat: refactor admin shell to HUD design"
```

---

### Task 5: Refactor Admin Panels

**Files:**
- Modify: `web/src/components/PostReader.tsx`
- Modify: `web/src/components/PromptEditor.tsx`
- Modify: `web/src/components/ConfigEditor.tsx`
- Modify: `web/src/components/RunControl.tsx`
- Modify: `web/src/components/ServiceControl.tsx`

- [ ] **Step 1: Replace panel-level styles**

Use `SectionIntro`, `Panel`, `PanelHeader`, `SelectableCard`, `EmptyState`, `NoticeBanner`, `Button`, `Input`, `Textarea`, `MetaCard`, and `CodeBlock` instead of direct `surface`, `rounded-*`, `slate`, `cyan`, `rose`, `emerald`, and `white/` styles.

- [ ] **Step 2: Keep interaction behavior unchanged**

Preserve API calls, loading states, error/notice flows, disabled state logic, selected post/prompt logic, manual run payload creation, service action confirmation, and markdown parsing.

- [ ] **Step 3: Verify web build**

Run: `rtk pnpm build:web`

Expected: build succeeds.

- [ ] **Step 4: Commit panel refactor**

Run:

```bash
rtk git add web/src/components/PostReader.tsx web/src/components/PromptEditor.tsx web/src/components/ConfigEditor.tsx web/src/components/RunControl.tsx web/src/components/ServiceControl.tsx
rtk git commit -m "feat: apply HUD design to admin panels"
```

---

### Task 6: Final Verification

**Files:**
- Verify all changed files.

- [ ] **Step 1: Run full verification**

Run: `rtk make verify`

Expected: install, format/type checks, lint, Vitest, build, and web asset build pass.

- [ ] **Step 2: Inspect final diff/status**

Run:

```bash
rtk git status --short
rtk git log --oneline -10
```

Expected: clean working tree after commits; recent history shows planning, token, primitive, shell, and panel commits.

## Self-Review

- Spec coverage: semantic tokens, Tailwind utility classes, hard-panel visual language, HUD utilities, interaction states, accessibility labels, reduced motion, and panel/dashboard layout are covered. Two-SPA viewer/overlay/PixiJS-only requirements are explicitly out of scope because the current app is a single admin SPA.
- Placeholder scan: no TBD/TODO/fill-in-later steps are present.
- Type consistency: all tasks refer to the same primitive names exported by `web/src/components/ui.tsx`.
