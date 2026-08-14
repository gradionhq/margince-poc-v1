import type { RefObject } from "react";
import { useEffect, useState } from "react";

type UseFileDropArgs = Readonly<{
  /** The region a dropped file is accepted in. When null, a drop anywhere in
   * the window feeds the caller — correct only for a surface that is the whole
   * window and says so. When set, drops outside it are neutralized but NOT
   * ingested: a file dropped on a global overlay (the command palette, a modal)
   * belongs to nobody, and silently feeding it to whatever screen sits behind
   * is worse than doing nothing. */
  container: RefObject<HTMLElement | null> | null;
  /** While false: file drags are still claimed so the browser cannot navigate,
   * but no files are delivered and no affordance shows. */
  active: boolean;
  onFiles: (files: readonly File[]) => void;
}>;

// Window-level drag and drop for file intake. The listeners are on `window`
// rather than a div because the browser's default action for a dropped file is
// to NAVIGATE to it, discarding unsaved work on the page — that has to be
// prevented wherever the file lands, not only over the drop zone.
export function useFileDrop({ container, active, onFiles }: UseFileDropArgs): {
  dragOver: boolean;
} {
  const [dragOver, setDragOver] = useState(false);

  useEffect(() => {
    // Only FILE drags are claimed: dragging selected text elsewhere on the
    // page is a native interaction this must not swallow.
    const isFileDrag = (event: globalThis.DragEvent) =>
      event.dataTransfer?.types.includes("Files") ?? false;
    // Whether the pointer is over the region that actually accepts files.
    const inZone = (event: globalThis.DragEvent) => {
      const zone = container?.current;
      if (!zone) {
        return container === null;
      }
      return event.target instanceof Node && zone.contains(event.target);
    };
    const onDragOver = (event: globalThis.DragEvent) => {
      if (!isFileDrag(event)) {
        return;
      }
      event.preventDefault();
      setDragOver(active && inZone(event));
    };
    const onDragLeave = (event: globalThis.DragEvent) => {
      // relatedTarget is null only when the drag exits the window; moving
      // between elements inside it must not flicker the affordance off.
      if (event.relatedTarget === null) {
        setDragOver(false);
      }
    };
    const onDrop = (event: globalThis.DragEvent) => {
      if (!isFileDrag(event)) {
        return;
      }
      event.preventDefault();
      setDragOver(false);
      if (active && inZone(event)) {
        onFiles(Array.from(event.dataTransfer?.files ?? []));
      }
    };
    window.addEventListener("dragover", onDragOver);
    window.addEventListener("dragleave", onDragLeave);
    window.addEventListener("drop", onDrop);
    return () => {
      window.removeEventListener("dragover", onDragOver);
      window.removeEventListener("dragleave", onDragLeave);
      window.removeEventListener("drop", onDrop);
    };
  }, [active, container, onFiles]);

  return { dragOver };
}
