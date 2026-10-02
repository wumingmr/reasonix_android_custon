export type ListReadAnchor = { scroller: HTMLElement; row: HTMLElement; key: string; top: number; scrollTop: number };

export function captureListReadAnchor(root: HTMLElement | null): ListReadAnchor | undefined {
  if (!root) return;
  let scroller: HTMLElement | null = root.querySelector(".project-tree__list") ?? root;
  while (scroller && !(scroller.scrollHeight > scroller.clientHeight && /(auto|scroll)/.test(getComputedStyle(scroller).overflowY))) scroller = scroller.parentElement;
  if (!scroller) return;
  const bounds = scroller.getBoundingClientRect();
  const row = Array.from(root.querySelectorAll<HTMLElement>("[data-topic-open-key]")).find((row) => {
    const box = row.getBoundingClientRect(); return box.bottom > bounds.top && box.top < bounds.bottom;
  });
  if (row) return { scroller, row, key: row.dataset.topicOpenKey ?? "", top: row.getBoundingClientRect().top, scrollTop: scroller.scrollTop };
}

export function restoreListReadAnchor(root: HTMLElement | null, anchor?: ListReadAnchor): void {
  if (!root || !anchor || !anchor.scroller.isConnected || anchor.scroller.scrollTop !== anchor.scrollTop) return;
  const row = anchor.row.isConnected ? anchor.row : Array.from(root.querySelectorAll<HTMLElement>("[data-topic-open-key]")).find((row) => row.dataset.topicOpenKey === anchor.key);
  if (row) anchor.scroller.scrollTop += row.getBoundingClientRect().top - anchor.top;
}
