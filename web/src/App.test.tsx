import { renderToString } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import App from './App';

describe('App', () => {
  it('renders the template heading', () => {
expect(renderToString(<App />)).toContain('Vite React TypeScript');
  });
});
