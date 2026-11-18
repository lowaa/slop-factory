import { describe, expect, it } from "vitest";
import { MAX_IMAGE_BYTES, MAX_VIDEO_BYTES, fileKind, pastedName, validateFile } from "./attachment_rules";

function fileOf(name: string, type: string, size = 10): File {
  return new File([new Uint8Array(size)], name, { type });
}

describe("validateFile", () => {
  it("accepts the allowed images and videos", () => {
    for (const [name, type] of [
      ["a.png", "image/png"],
      ["a.jpg", "image/jpeg"],
      ["a.webp", "image/webp"],
      ["a.gif", "image/gif"],
      ["a.mp4", "video/mp4"],
      ["a.webm", "video/webm"],
      ["a.mov", "video/quicktime"],
    ]) {
      expect(validateFile(fileOf(name, type))).toBeNull();
    }
  });

  it("takes a .mov the browser gave no type", () => {
    expect(fileKind(fileOf("recording.MOV", ""))).toBe("video");
  });

  it("refuses SVG, HTML and anything else", () => {
    expect(validateFile(fileOf("a.svg", "image/svg+xml"))).toMatch(/not supported/);
    expect(validateFile(fileOf("a.html", "text/html"))).toMatch(/not supported/);
  });

  it("holds an image to 10 MB and a video to 100 MB", () => {
    expect(validateFile(fileOf("a.png", "image/png", MAX_IMAGE_BYTES))).toBeNull();
    expect(validateFile(fileOf("a.png", "image/png", MAX_IMAGE_BYTES + 1))).toMatch(/over the 10 MB limit for images/);
    expect(validateFile(fileOf("a.mp4", "video/mp4", MAX_IMAGE_BYTES + 1))).toBeNull();
    expect(validateFile(fileOf("a.mp4", "video/mp4", MAX_VIDEO_BYTES + 1))).toMatch(/over the 100 MB limit for videos/);
  });

  it("refuses an empty file", () => {
    expect(validateFile(fileOf("a.png", "image/png", 0))).toMatch(/empty/);
  });
});

describe("pastedName", () => {
  const at = new Date(2026, 10, 9, 4, 12, 0);

  it("names a clipboard image by when it was pasted", () => {
    expect(pastedName(fileOf("image.png", "image/png"), at)).toBe("screenshot-2026-11-09T04-12-00.png");
  });

  it("takes the extension from the type and keeps several apart", () => {
    expect(pastedName(fileOf("", "image/jpeg"), at, 1)).toBe("screenshot-2026-11-09T04-12-00-2.jpg");
  });
});
