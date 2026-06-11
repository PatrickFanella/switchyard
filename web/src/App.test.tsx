import { renderToString } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import App from './App';

describe('App', () => {
  it('renders the Switchyard control-plane dashboard', () => {
    const html = renderToString(<App />);

    expect(html).toContain('Switchyard control-plane suite');
    expect(html).toContain('UI → Switchyard → llama-line → OCQ → Blacktower');
    expect(html).toContain('Open Pilot');
    expect(html).toContain('Run detail');
    expect(html).toContain('Events and artifacts');
    expect(html).toContain('Queues');
    expect(html).toContain('Agents');
    expect(html).toContain('Skills');
  });
});
