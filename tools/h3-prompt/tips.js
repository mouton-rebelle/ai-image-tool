/* Tooltips for [data-tip] elements.
 *
 * Body-level so nothing gets clipped by the scrolling panes, and delegated so
 * it survives the re-renders. Keeps the guidance out of the layout, which is
 * what a ComfyUI node has room for.
 */
window.Tips = (() => {
  'use strict';

  const DELAY = 260;
  let el = null;
  let timer = null;
  let target = null;
  let suppressed = false;

  function hide() {
    clearTimeout(timer);
    target = null;
    if (el) { el.remove(); el = null; }
  }

  function show(node) {
    const text = node.getAttribute('data-tip');
    if (!text || suppressed) return;
    hide();
    target = node;
    el = document.createElement('div');
    el.className = 'tip';
    el.textContent = text;
    document.body.append(el);

    const r = node.getBoundingClientRect();
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    const below = r.bottom + 7;
    const flip = below + h > window.innerHeight - 8;
    el.style.left = `${Math.max(8, Math.min(r.left + r.width / 2 - w / 2, window.innerWidth - w - 8))}px`;
    el.style.top = `${flip ? r.top - h - 7 : below}px`;
  }

  function schedule(node) {
    if (suppressed || node === target) return;
    clearTimeout(timer);
    timer = setTimeout(() => show(node), DELAY);
  }

  document.addEventListener('mouseover', (e) => {
    const node = e.target.closest?.('[data-tip]');
    if (node) schedule(node);
    else if (target) hide();
  });

  document.addEventListener('focusin', (e) => {
    const node = e.target.closest?.('[data-tip]');
    if (node) show(node);
  });

  document.addEventListener('focusout', hide);
  document.addEventListener('mousedown', hide);
  window.addEventListener('scroll', hide, true);
  window.addEventListener('resize', hide);

  /** Popovers own the space under the control they hang from; a tooltip
   *  drifting on top of one is just noise. */
  function suppress(on) {
    suppressed = on;
    if (on) hide();
  }

  return { hide, suppress };
})();
