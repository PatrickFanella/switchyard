const tokens = {
  void: '228 22% 7%',
  panel: '228 18% 11%',
  'panel-soft': '228 16% 14%',
  'panel-strong': '228 20% 9%',
  ink: '210 40% 96%',
  muted: '220 12% 72%',
  faint: '220 10% 54%',
  border: '220 14% 30%',
  'border-faint': '220 12% 20%',
  shadow: '228 26% 4%',
  signal: '286 95% 68%',
  'signal-soft': '286 70% 20%',
  pulse: '236 90% 70%',
  'pulse-soft': '236 60% 18%',
  confirm: '158 84% 56%',
  'confirm-soft': '158 55% 16%',
  warn: '42 100% 60%',
  'warn-soft': '42 80% 18%',
  danger: '0 84% 66%',
  'danger-soft': '0 68% 18%',
};

const tokenColors = Object.fromEntries(Object.keys(tokens).map((token) => [token, `hsl(var(--${token}))`]));

/** @type {import('tailwindcss').Config} */
export default {
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    extend: {
      colors: tokenColors,
      fontFamily: {
        hud: ['var(--font-stack)'],
        mono: ['var(--font-stack)'],
      },
      boxShadow: {
        hud: '6px 6px 0 hsl(var(--shadow))',
        'hud-strong': '8px 8px 0 hsl(var(--shadow))',
      },
    },
  },
  plugins: [
    ({ addBase }) => {
      addBase({
        ':root': {
          ...Object.fromEntries(Object.entries(tokens).map(([token, value]) => [`--${token}`, value])),
          '--font-stack':
            '"IBM Plex Mono", "JetBrains Mono", ui-monospace, "SFMono-Regular", Menlo, Monaco, Consolas, monospace',
        },
      });
    },
  ],
};
