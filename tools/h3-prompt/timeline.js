/* Timestamp picker — a scrubbable bar bounded by the neighbouring cut times.
 *
 * Values are frame indices, never floats: H3 runs at a fixed frame rate, so a
 * cut that falls between two frames is not a thing you can ask for. The bar
 * snaps accordingly and the readout shows both the timecode and the frame.
 */
window.Timeline = (() => {
  'use strict';

  let el = null;
  let ctx = null;

  function format(sec) {
    const m = Math.floor(sec / 60);
    const s = Math.floor(sec % 60);
    const ms = Math.round((sec - Math.floor(sec)) * 1000);
    return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}.${String(ms).padStart(3, '0')}`;
  }

  /** `silent` is used when one popover replaces another: the caller is about to
   *  anchor on an element the close handler would otherwise re-render away. */
  function close(silent) {
    if (!el) return;
    const done = ctx?.onClose;
    window.Tips?.suppress(false);
    el.remove();
    el = null;
    ctx = null;
    document.removeEventListener('mousedown', onOutside, true);
    document.removeEventListener('keydown', onKey, true);
    window.removeEventListener('resize', place);
    document.querySelectorAll('.pane').forEach((p) => p.removeEventListener('scroll', place));
    if (!silent) done?.();
  }

  function onOutside(e) {
    if (el && !el.contains(e.target) && e.target !== ctx.anchor) close();
  }

  function onKey(e) {
    if (!ctx) return;
    if (e.key === 'Escape' || e.key === 'Enter') {
      e.preventDefault();
      close();
    } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
      e.preventDefault();
      const step = (e.key === 'ArrowRight' ? 1 : -1) * (e.shiftKey ? ctx.fps : 1);
      commit((ctx.value ?? ctx.min) + step);
    }
  }

  const clamp = (frame) => Math.max(ctx.min, Math.min(ctx.max, Math.round(frame)));

  /** Only the readout and the playhead move. Rebuilding the markup here would
   *  detach the very bar a drag is being tracked on. */
  function paint() {
    const { total, fps, value, readout, head } = ctx;
    readout.innerHTML =
      value === null
        ? 'Click the bar to set a cut time'
        : `${format(value / fps)} &middot; frame ${value} / ${total}`;
    readout.classList.toggle('muted', value === null);
    head.hidden = value === null;
    if (value !== null) head.style.left = `${(value / total) * 100}%`;
  }

  function commit(frame) {
    if (!ctx || !el) return;
    ctx.value = clamp(frame);
    paint();
    ctx.onPick(ctx.value);
  }

  function place() {
    if (!el || !ctx) return;
    const a = ctx.anchor.getBoundingClientRect();
    const w = el.offsetWidth;
    const left = Math.max(8, Math.min(a.left + a.width / 2 - w / 2, window.innerWidth - w - 8));
    const below = a.bottom + 6;
    const flip = below + el.offsetHeight > window.innerHeight - 8;
    el.style.left = `${left}px`;
    el.style.top = `${flip ? a.top - el.offsetHeight - 6 : below}px`;
  }

  function build() {
    const { total, fps, min, max, marks } = ctx;
    const pct = (frame) => `${(frame / total) * 100}%`;

    el.innerHTML = `
      <div class="tl-readout" data-readout></div>
      <div class="tl-bar" data-bar>
        <div class="tl-allowed" style="left:${pct(min)};width:${pct(max - min)}"></div>
        ${marks.map((m) => `<div class="tl-mark${m.shot ? ' shot' : ''}" style="left:${pct(m.frame)}"></div>`).join('')}
        <div class="tl-head" data-head hidden></div>
      </div>
      <div class="tl-scale"><span>00:00.000</span><span>${format(total / fps)}</span></div>
      <div class="tl-foot">
        <span class="tl-hint">← → nudge a frame &middot; ⇧ a second</span>
        <button type="button" class="btn tl-clear" data-clear>Clear</button>
      </div>`;

    ctx.readout = el.querySelector('[data-readout]');
    ctx.head = el.querySelector('[data-head]');
    const bar = el.querySelector('[data-bar]');

    const pick = (e) => {
      const r = bar.getBoundingClientRect();
      if (!r.width) return;
      commit(((e.clientX - r.left) / r.width) * total);
    };

    bar.addEventListener('mousedown', (e) => {
      e.preventDefault();
      pick(e);
      const move = (ev) => pick(ev);
      const up = () => {
        document.removeEventListener('mousemove', move);
        document.removeEventListener('mouseup', up);
      };
      document.addEventListener('mousemove', move);
      document.addEventListener('mouseup', up);
    });

    el.querySelector('[data-clear]').addEventListener('click', () => {
      const clear = ctx.onClear;
      close(true);
      clear();
    });
  }

  /**
   * @param anchor  element the popover hangs from
   * @param total   video length in frames
   * @param fps     frames per second
   * @param value   current frame, or null
   * @param min/max inclusive frame bounds allowed for this beat
   * @param marks   [{frame, shot}] cut times already placed
   */
  function open(opts) {
    close(true);
    window.Tips?.suppress(true);
    ctx = { ...opts };
    el = document.createElement('div');
    el.className = 'timeline-pop';
    document.body.append(el);
    build();
    paint();
    place();
    document.addEventListener('mousedown', onOutside, true);
    document.addEventListener('keydown', onKey, true);
    window.addEventListener('resize', place);
    document.querySelectorAll('.pane').forEach((p) => p.addEventListener('scroll', place));
  }

  return { open, close, format };
})();
