import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

import { rememberLanguage } from "../i18n";

rememberLanguage("zh-CN");
window.localStorage.clear();
beforeEach(() => { rememberLanguage("zh-CN"); window.localStorage.clear(); });

afterEach(() => {
  cleanup();
  window.localStorage.clear();
  document.documentElement.dataset.theme = "light";
  document.documentElement.style.colorScheme = "";
});

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false
  })
});

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

vi.stubGlobal("ResizeObserver", TestResizeObserver);

Object.defineProperty(HTMLCanvasElement.prototype, "getContext", {
  configurable: true,
  value: vi.fn(() => ({
    fillRect: vi.fn(),
    fillText: vi.fn(),
    measureText: (text: string) => ({ width: String(text).length * 7 }),
    scale: vi.fn()
  }))
});

const getComputedStyle = window.getComputedStyle.bind(window);
window.getComputedStyle = (element: Element) => getComputedStyle(element);
