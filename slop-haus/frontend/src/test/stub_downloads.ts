import { vi } from "vitest";

// stubDownloads records the files saveFile hands to the browser.
export function stubDownloads() {
  const clicked: { filename: string; blob: Blob | undefined }[] = [];
  const blobs = new Map<string, Blob>();

  vi.stubGlobal("URL", {
    ...URL,
    createObjectURL: (blob: Blob) => {
      const url = `blob:${blobs.size}`;
      blobs.set(url, blob);
      return url;
    },
    revokeObjectURL: () => {},
  });

  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push({ filename: this.download, blob: blobs.get(this.href) });
  });

  return clicked;
}
