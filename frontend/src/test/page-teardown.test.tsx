import { createRoot } from 'react-dom/client';
import { setTimeout as sleep } from 'node:timers/promises';
import { cleanup, render } from '@testing-library/react';
import { Button } from 'antd';
import { expect, it } from 'vitest';

const page = (globalThis as unknown as { jsdom: { window: { close(): void } } }).jsdom;
const scope = globalThis as { window?: unknown; IS_REACT_ACT_ENVIRONMENT?: boolean };

// The page closes here, so this file holds a single test. Both callbacks were queued
// when CI tore the page down, then threw "window is not defined" and failed the run.
it('drops what a closed page left queued instead of running it without window', async () => {
  // BalancerFormModal: antd's useDelayState timer, which unmounting does not cancel.
  render(<Button loading={{ delay: 100 }}>Save</Button>);
  cleanup();

  // SubOpenVPNSection: antd's message renders in a root RTL does not own, outside act.
  const root = createRoot(document.body.appendChild(document.createElement('div')));
  scope.IS_REACT_ACT_ENVIRONMENT = false;
  root.render(<span>Not ready yet</span>);
  scope.IS_REACT_ACT_ENVIRONMENT = true;

  const crashes: string[] = [];
  const record = (error: Error) => crashes.push(error.stack ?? String(error));
  const savedWindow = scope.window;
  process.on('uncaughtException', record);
  try {
    // What vitest's teardown does: close the jsdom page, then delete `window`.
    page.window.close();
    delete scope.window;
    await sleep(300);
  } finally {
    scope.window = savedWindow;
    process.off('uncaughtException', record);
  }

  expect(crashes).toEqual([]);
});
