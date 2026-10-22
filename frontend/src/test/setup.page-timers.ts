// Vitest's jsdom keeps Node's process-wide timers, so a callback a page queued can fire after
// teardown deleted `window`. A browser drops a closed page's timers; this makes jsdom do the same.
type Callback = (...args: unknown[]) => void;
type Schedule = (callback: Callback, ...rest: unknown[]) => unknown;
type Cancel = (handle: unknown) => void;

const page = (globalThis as unknown as { jsdom: { window: { close(): void } } }).jsdom;
const native = {
  setTimeout: globalThis.setTimeout,
  setInterval: globalThis.setInterval,
  setImmediate: globalThis.setImmediate,
};
let pageClosed = false;

function forThisPage(schedule: Schedule, cancel: Cancel): Schedule {
  return (callback, ...rest) => {
    const handle = schedule(
      function (this: unknown, ...args: unknown[]) {
        if (pageClosed) cancel(handle);
        else callback.apply(this, args);
      },
      ...rest,
    );
    return handle;
  };
}

Object.assign(globalThis, {
  setTimeout: forThisPage(native.setTimeout as Schedule, clearTimeout as Cancel),
  setInterval: forThisPage(native.setInterval as Schedule, clearInterval as Cancel),
  setImmediate: forThisPage(native.setImmediate as Schedule, clearImmediate as Cancel),
});

// Whatever runs after the page closes gets the process timers back, not dropped callbacks.
const closePage = page.window.close;
page.window.close = () => {
  pageClosed = true;
  Object.assign(globalThis, native);
  closePage();
};
