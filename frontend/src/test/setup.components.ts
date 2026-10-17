import { afterEach, vi } from 'vitest';
import { cleanup } from '@testing-library/react';
import i18next from 'i18next';
import { initReactI18next } from 'react-i18next';

import enUS from '../../../internal/web/translation/en-US.json';

// RTL sets this from a global beforeAll, which never runs with `globals: false`.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('persian-calendar-suite', () => ({
  PersianDateTimePicker: () => null,
}));

if (typeof globalThis.localStorage === 'undefined') {
  const store = new Map<string, string>();
  const storage = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => {
      store.set(k, String(v));
    },
    removeItem: (k: string) => {
      store.delete(k);
    },
    clear: () => {
      store.clear();
    },
    key: (i: number) => Array.from(store.keys())[i] ?? null,
    get length() {
      return store.size;
    },
  } as Storage;
  Object.defineProperty(globalThis, 'localStorage', { value: storage, configurable: true });
  Object.defineProperty(globalThis, 'sessionStorage', { value: storage, configurable: true });
}

if (!window.matchMedia) {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

if (typeof globalThis.ResizeObserver === 'undefined') {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
}

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// jsdom does not implement pseudo-element styles or Range geometry. Ant
// Design and CodeMirror use these APIs for layout, so supply harmless test
// fallbacks instead of emitting noisy "Not implemented" errors.
const nativeGetComputedStyle = window.getComputedStyle.bind(window);
window.getComputedStyle = ((element: Element) =>
  nativeGetComputedStyle(element)) as typeof window.getComputedStyle;

if (!Range.prototype.getClientRects) {
  Range.prototype.getClientRects = () => [] as unknown as DOMRectList;
}

if (!i18next.isInitialized) {
  void i18next.use(initReactI18next).init({
    lng: 'en-US',
    fallbackLng: 'en-US',
    resources: { 'en-US': { translation: enUS } },
    interpolation: { escapeValue: false, prefix: '{', suffix: '}' },
    returnNull: false,
  });
}

afterEach(async () => {
  cleanup();
  document.body.innerHTML = '';
  /*
   * React 19 defers passive-effect flushes onto a macrotask (setImmediate),
   * whose callback reads `window.event`. If one is still queued when vitest
   * tears down the jsdom environment, it fires after `window` is gone and
   * throws "window is not defined". Drain a few macrotask ticks here so any
   * pending callback runs while `window` still exists. Several ticks are used
   * because a microtask resolving mid-drain (rc-trigger/AntD) can queue a new
   * one behind the first.
   */
  for (let i = 0; i < 3; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
});

import { HttpUtil, Msg } from '@/utils';

// Every permission key the panel knows, mirroring authz.AllPermissions. A
// component test renders as a super admin unless it says otherwise, because
// the alternative is worse: an empty /admins/me fails its schema, every
// can() reads false, and a test asserting on a permission-gated control
// fails with "unable to find button" — which names the wrong cause.
const ALL_PERMISSIONS = [
  'sections.inbounds',
  'sections.clients',
  'sections.groups',
  'sections.hosts',
  'sections.nodes',
  'sections.outbounds',
  'sections.routing',
  'sections.xray',
  'sections.settings',
  'sections.admins',
  'clients.create',
  'clients.edit',
  'clients.delete',
  'clients.resetTraffic',
  'clients.unlimitedData',
  'clients.unlimitedExpiry',
  'clients.viewLinks',
  'inbounds.create',
  'inbounds.edit',
  'inbounds.delete',
  'inbounds.resetTraffic',
];

const TEST_ADMIN_ME = {
  id: 1,
  username: 'test-admin',
  role: 'super_admin',
  permissions: Object.fromEntries(ALL_PERMISSIONS.map((key) => [key, true])),
  clientCount: 0,
  expireAt: 0,
  trafficQuota: 0,
  usedUp: 0,
  usedDown: 0,
  usersLimit: 0,
};

// eslint-disable-next-line @typescript-eslint/no-explicit-any
vi.spyOn(HttpUtil, 'post').mockResolvedValue({ success: true, obj: {} } as any);
vi.spyOn(HttpUtil, 'get').mockImplementation(async (url: string) => {
  if (url.includes('/panel/api/admins/me')) return new Msg(true, '', TEST_ADMIN_ME);
  return new Msg(true, '', url.includes('/panel/api/inbounds/options') ? [] : {});
});
