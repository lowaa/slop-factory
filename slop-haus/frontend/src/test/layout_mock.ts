import { afterEach } from "vitest";

type Size = number | ((el: HTMLElement) => number);

type SizeProp = "offsetHeight" | "offsetWidth" | "clientHeight" | "clientWidth";

const originals = new Map<SizeProp, PropertyDescriptor | undefined>();

// jsdom lays nothing out, so every offset and client size is 0 and a windowed
// list draws no rows: the virtualizer reads its viewport's client size and its
// rows' offset size. mockOffsetSize gives elements the size a browser would, for
// the current test only: the afterEach below puts the prototype's own getters
// back, so a getter can't leak into a later test in the same file.
// A function size is told which element is asked, to size a viewport and its
// rows differently.
export function mockOffsetSize(sizes: { height?: Size; width?: Size }): void {
  override("offsetHeight", sizes.height);
  override("offsetWidth", sizes.width);
  override("clientHeight", sizes.height);
  override("clientWidth", sizes.width);
}

function override(prop: SizeProp, size: Size | undefined): void {
  if (size === undefined) return;
  if (!originals.has(prop)) originals.set(prop, Object.getOwnPropertyDescriptor(HTMLElement.prototype, prop));
  Object.defineProperty(HTMLElement.prototype, prop, {
    configurable: true,
    get(this: HTMLElement) {
      return typeof size === "function" ? size(this) : size;
    },
  });
}

afterEach(() => {
  originals.forEach((descriptor, prop) => {
    if (descriptor) Object.defineProperty(HTMLElement.prototype, prop, descriptor);
    else delete (HTMLElement.prototype as unknown as Record<string, unknown>)[prop];
  });
  originals.clear();
});
