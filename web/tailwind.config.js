const tokens = {
  void: '222 32% 7%',
  panel: '221 25% 11%',
  'panel-soft': '220 22% 15%',
  'panel-strong': '222 29% 9%',
  ink: '210 30% 96%',
  muted: '215 14% 68%',
  faint: '217 11% 48%',
  border: '217 17% 26%',
  'border-faint': '218 18% 19%',
  shadow: '224 40% 3%',
  signal: '187 92% 52%',
  'signal-soft': '187 62% 14%',
  pulse: '219 92% 68%',
  'pulse-soft': '219 58% 16%',
  confirm: '155 72% 52%',
  'confirm-soft': '155 50% 14%',
  warn: '39 96% 60%',
  'warn-soft': '39 65% 15%',
  danger: '0 78% 65%',
  'danger-soft': '0 54% 15%',
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
            'Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
        },
      });
    },
  ],
};
