// viewportShift returns how far (in px) to move a box horizontally so it
// sits at least `margin` inside [0, viewportWidth]. A box too wide to fit
// both edges is pinned to the left one: the start of the text is what a
// reader needs to find.
export function viewportShift(left: number, right: number, viewportWidth: number, margin: number): number {
  if (left < margin) return margin - left;
  if (right > viewportWidth - margin) return Math.max(viewportWidth - margin - right, margin - left);
  return 0;
}
