// The server's limits (internal/attachment), checked here for fast feedback.
// The server decides from the bytes and stays the authority: a refusal it
// makes is shown as it words it.
export const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
export const MAX_VIDEO_BYTES = 100 * 1024 * 1024;

export const IMAGE_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"];
export const VIDEO_TYPES = ["video/mp4", "video/webm", "video/quicktime"];

// What a file picker offers. A .mov is named too, as some systems report
// no type for one.
export const ACCEPT = [...IMAGE_TYPES, ...VIDEO_TYPES, ".mov"].join(",");

export type MediaKind = "image" | "video";

export function mediaKind(contentType: string): MediaKind | null {
  if (IMAGE_TYPES.includes(contentType)) return "image";
  if (VIDEO_TYPES.includes(contentType)) return "video";
  return null;
}

// The type a browser reports for a file, with a .mov told by its name when
// the browser reports nothing.
export function fileKind(file: File): MediaKind | null {
  const kind = mediaKind(file.type);
  if (kind) return kind;
  return !file.type && /\.mov$/i.test(file.name) ? "video" : null;
}

export function formatBytes(bytes: number): string {
  if (bytes >= 1024 * 1024) return `${Math.round((bytes / (1024 * 1024)) * 10) / 10} MB`;
  return `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

// validateFile is the reason a file will not be attached, or null.
export function validateFile(file: File): string | null {
  const kind = fileKind(file);
  if (!kind) return `${file.name} is not supported: only PNG, JPEG, WebP and GIF images and MP4, WebM and MOV videos can be attached.`;
  const limit = kind === "image" ? MAX_IMAGE_BYTES : MAX_VIDEO_BYTES;
  if (file.size > limit) {
    return `${file.name} is ${formatBytes(file.size)}, over the ${formatBytes(limit)} limit for ${kind === "image" ? "images" : "videos"}.`;
  }
  if (file.size === 0) return `${file.name} is empty.`;
  return null;
}

const EXTENSIONS: Record<string, string> = {
  "image/png": "png",
  "image/jpeg": "jpg",
  "image/webp": "webp",
  "image/gif": "gif",
};

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

// pastedName names a clipboard blob, which arrives as "image.png" or with no
// name at all: screenshot-2026-11-09T04-12-00.png. The index keeps several
// images pasted in one go apart.
export function pastedName(file: File, now: Date, index = 0): string {
  const stamp = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}T${pad(now.getHours())}-${pad(now.getMinutes())}-${pad(now.getSeconds())}`;
  const ext = EXTENSIONS[file.type] ?? "png";
  return `screenshot-${stamp}${index > 0 ? `-${index + 1}` : ""}.${ext}`;
}
