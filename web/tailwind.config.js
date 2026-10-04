import { readFileSync } from 'node:fs';
import { URL } from 'node:url';

// The product design tokens are canonical; these aliases preserve component contracts.
const brand = JSON.parse(readFileSync(new URL('./brand-tokens.json', import.meta.url), 'utf8'));
const dark = brand.color.dark;
function hsl(hex) {
  const [r, g, b] = hex.slice(1).match(/../g).map((value) => parseInt(value, 16) / 255);
  const max = Math.max(r, g, b), min = Math.min(r, g, b), delta = max - min;
  const lightness = (max + min) / 2;
  const saturation = delta === 0 ? 0 : delta / (1 - Math.abs(2 * lightness - 1));
  let hue = 0;
  if (delta !== 0) {
    hue = max === r ? ((g - b) / delta) % 6 : max === g ? (b - r) / delta + 2 : (r - g) / delta + 4;
    hue = (hue * 60 + 360) % 360;
  }
  return `${hue.toFixed(4)} ${(saturation * 100).toFixed(4)}% ${(lightness * 100).toFixed(4)}%`;
}
const colors = {
  void: dark.canvas, panel: dark.surface, 'panel-soft': dark.surfaceRaised,
  'panel-strong': dark.surface, ink: dark.text, muted: dark.textSecondary,
  faint: dark.textSecondary, border: dark.controlBorder, 'border-faint': dark.divider,
  shadow: '#09080D', signal: dark.action, 'signal-soft': '#30273F',
  'action-foreground': dark.actionForeground, pulse: dark.action, 'pulse-soft': '#30273F',
  confirm: dark.success, 'confirm-soft': '#173328',
  warn: dark.attention, 'warn-soft': '#382C1F',
  danger: dark.failure, 'danger-soft': '#3B232B',
};
const tokens = Object.fromEntries(Object.entries(colors).map(([key, value]) => [key, hsl(value)]));
const tokenColors = Object.fromEntries(Object.keys(tokens).map((token) => [token, `hsl(var(--${token}))`]));

/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: tokenColors,
      fontFamily: {
        hud: ['var(--font-display)'],
        mono: ['var(--font-mono-stack)'],
      },
      boxShadow: {
        hud: '0 1px 2px hsl(var(--shadow) / .16)',
        'hud-strong': '0 8px 24px hsl(var(--shadow) / .24)',
      },
    },
  },
  plugins: [
    ({ addBase }) => {
      addBase({
        ':root': {
          ...Object.fromEntries(Object.entries(tokens).map(([token, value]) => [`--${token}`, value])),
          '--font-stack': '"Public Sans", ui-sans-serif, system-ui, sans-serif',
          '--font-display': 'Sora, ui-sans-serif, system-ui, sans-serif',
          '--font-mono-stack': '"IBM Plex Mono", ui-monospace, monospace',
        },
      });
    },
  ],
};
