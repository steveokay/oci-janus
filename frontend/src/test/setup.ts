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
