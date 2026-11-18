import { useEffect } from "react";
import type { TenantColour } from "./tenant_colour";

const SIZE = 32;
const LINK_ID = "tenant-favicon";

type CanvasFactory = () => HTMLCanvasElement;

const defaultCanvas: CanvasFactory = () => {
  const canvas = document.createElement("canvas");
  canvas.width = SIZE;
  canvas.height = SIZE;
  return canvas;
};

// One data URL per colour: drawn the first time a tenant is shown and kept,
// so a re-render or a tenant list refresh never repaints. A failed draw is
// cached too, as null, rather than retried on every render.
const drawn = new Map<string, string | null>();

export function resetTenantFaviconCache() {
  drawn.clear();
}

// The tenant's colour as a CSS value, from the same tokens the badge uses so
// the tab and the header can't drift apart.
function colourValue(colour: TenantColour): string {
  return getComputedStyle(document.documentElement).getPropertyValue(`--tenant-${colour}-bg`).trim();
}

// tenantFaviconUrl draws a filled disc in the tenant's colour with a ring
// that keeps it visible on light and dark tab strips. Null where canvas (or
// the colour token) is unavailable, so the caller falls back to the plain
// icon.
export function tenantFaviconUrl(colour: TenantColour, createCanvas: CanvasFactory = defaultCanvas): string | null {
  if (drawn.has(colour)) return drawn.get(colour) ?? null;
  let url: string | null = null;
  try {
    const fill = colourValue(colour);
    const ctx = fill === "" ? null : createCanvas().getContext("2d");
    if (ctx) {
      const centre = SIZE / 2;
      ctx.beginPath();
      ctx.arc(centre, centre, centre - 2, 0, Math.PI * 2);
      ctx.fillStyle = fill;
      ctx.fill();
      ctx.lineWidth = 3;
      ctx.strokeStyle = "#ffffff";
      ctx.stroke();
      url = ctx.canvas.toDataURL("image/png");
    }
  } catch {
    url = null;
  }
  drawn.set(colour, url);
  return url;
}

// Points the tab icon at the tenant's disc while the caller is mounted, and
// removes our link after so the browser's own icon returns. Any icon the
// page declares itself is left alone (we only add and remove our own link).
export function useTenantFavicon(colour: TenantColour | null) {
  useEffect(() => {
    if (colour === null) return;
    const url = tenantFaviconUrl(colour);
    if (url === null) return;
    const link = document.createElement("link");
    link.id = LINK_ID;
    link.rel = "icon";
    link.type = "image/png";
    link.href = url;
    document.head.appendChild(link);
    return () => link.remove();
  }, [colour]);
}
