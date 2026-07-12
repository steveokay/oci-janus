import "@testing-library/jest-dom/vitest";

// Beacon — vitest setup. Anything that needs to run before every test goes here.

// jsdom doesn't ship ResizeObserver — Radix's Switch primitive uses it via
// useSize and blows up on mount with "ResizeObserver is not defined".
// Polyfill with a no-op stub so components under test render without
// exercising real size observation.
if (typeof globalThis.ResizeObserver === "undefined") {
  class ResizeObserverPolyfill {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
  globalThis.ResizeObserver =
    ResizeObserverPolyfill as unknown as typeof ResizeObserver;
}

// jsdom doesn't implement the Pointer Capture API or Element.scrollIntoView,
// both of which Radix's Select primitive calls when an option is clicked
// (select.tsx uses hasPointerCapture / scrollIntoView). Without these stubs
// any test that opens a Radix Select throws "hasPointerCapture is not a
// function". Polyfill with no-ops so Select-driven tests (e.g. the FUT-009
// sign-manifest dialog) can exercise the option list.
if (typeof Element !== "undefined") {
  if (!Element.prototype.hasPointerCapture) {
    Element.prototype.hasPointerCapture = (): boolean => false;
  }
  if (!Element.prototype.setPointerCapture) {
    Element.prototype.setPointerCapture = (): void => {};
  }
  if (!Element.prototype.releasePointerCapture) {
    Element.prototype.releasePointerCapture = (): void => {};
  }
  if (!Element.prototype.scrollIntoView) {
    Element.prototype.scrollIntoView = (): void => {};
  }
}
