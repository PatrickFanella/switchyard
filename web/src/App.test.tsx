import { renderToString } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import App from './App';

describe('App', () => {
  it('renders the Switchyard heading', () => {
expect(renderToString(<App />)).toContain('Route work through the right lane');
  });
});
