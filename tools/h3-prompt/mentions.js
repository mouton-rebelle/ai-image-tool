/* Mention field — "@alias" autocomplete and "/" insert commands over a plain
 * <textarea>.
 *
 * Two triggers: "@" completes a subject alias, "<" offers a spoken line and the
 * reference assets (<Picture 1>, <Video 1>, <Audio 1>). Speaker IDs are left to
 * the writer — the builder only formats "(Sn)" correctly when one is by hand.
 *
 * The textarea keeps its native behaviour (caret, undo, selection, spellcheck);
 * a mirror <div> sits behind it, rendering the same text with a coloured pill
 * under each recognised mention and a script treatment on each <d> block. The
 * same mirror gives us caret geometry, so the popup anchors on the trigger
 * without a second measuring pass.
 *
 * What is STORED is "@alias" — never "<Subject 3>". Resolution to a numbered
 * label happens at serialisation time, so renaming, reordering or deleting a
 * subject can never leave a stale number behind in the prose.
 */
window.Mentions = (() => {
  'use strict';

  const MENTION = '@';
  const ANGLE = '<';
  const MAX_QUERY = 48;

  const escapeHtml = (str) =>
    str.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]);

  const escapeRe = (str) => str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

  /** Longest-first so "@paul atreides" wins over "@paul". */
  function mentionRegex(subjects) {
    const named = subjects.filter((s) => s.name).sort((a, b) => b.name.length - a.name.length);
    if (!named.length) return null;
    return new RegExp(`${MENTION}(${named.map((s) => escapeRe(s.name)).join('|')})(\\(S\\d*\\))?(?![\\w])`, 'gi');
  }

  /** Every resolved mention, as {start, nameEnd, end, subject, speaker}. */
  function findMentions(text, subjects) {
    const re = mentionRegex(subjects);
    if (!re) return [];
    const out = [];
    let m;
    while ((m = re.exec(text)) !== null) {
      const name = m[1].toLowerCase();
      out.push({
        start: m.index,
        nameEnd: m.index + 1 + m[1].length,
        end: m.index + m[0].length,
        speaker: m[2] || null,
        subject: subjects.find((s) => s.name.toLowerCase() === name),
      });
    }
    return out;
  }

  /** Every "<Picture 2>"-style label, resolved against the declared assets.
   *  An unresolved one is still reported, so it can be flagged rather than hidden. */
  function findAssets(text, assets) {
    const out = [];
    // "?" is the orphan marker left behind when a cited reference is deleted.
    // Case-insensitive: hand-typed "<picture 1>" is the same reference.
    const re = /<(Picture|Video|Audio) (\d+|\?)>/gi;
    let m;
    while ((m = re.exec(text)) !== null) {
      const index = m[2] === '?' ? null : Number(m[2]);
      const kind = m[1][0].toUpperCase() + m[1].slice(1).toLowerCase();
      out.push({
        start: m.index,
        end: m.index + m[0].length,
        kind,
        index,
        label: m[0],
        asset: assets.find((a) => a.kind === kind && a.index === index) ?? null,
      });
    }
    return out;
  }

  /** Spoken blocks, as {start, tagEnd, closeStart, end, lang, said}. Matches an
   *  unclosed block too, so the styling follows along while it is being typed. */
  function findDialogue(text) {
    const out = [];
    const re = /<d>\[([^\]\n]*)\]([\s\S]*?)(<\/d>|$)/gi;
    let m;
    while ((m = re.exec(text)) !== null) {
      if (!m[0]) break;
      const start = m.index;
      const end = start + m[0].length;
      out.push({
        start,
        tagEnd: start + 3 + 1 + m[1].length + 1,
        closeStart: m[3] ? end - m[3].length : end,
        end,
        lang: m[1],
        said: m[2],
        closed: Boolean(m[3]),
      });
    }
    return out;
  }

  /** Labels are written in one canonical casing whatever the writer typed. */
  const canonical = (text) =>
    text
      .replace(/<(subject|picture|video|audio) (\d+|\?)>/gi,
        (_, kind, n) => `<${kind[0].toUpperCase()}${kind.slice(1).toLowerCase()} ${n}>`)
      .replace(/<\/?d>/gi, (m) => m.toLowerCase());

  /** Replace "@alias" with the subject's label, "@alias(Sn)" with label + ID. */
  function resolve(text, subjects) {
    const mentions = findMentions(text, subjects);
    if (!mentions.length) return text;
    let out = '';
    let last = 0;
    for (const m of mentions) {
      out += text.slice(last, m.start) + m.subject.label + (m.speaker ? ` ${m.speaker}` : '');
      last = m.end;
    }
    return out + text.slice(last);
  }

  /** "@foo" written by hand that matches no subject, as {name, start}. */
  function danglingMentions(text, subjects) {
    const resolved = findMentions(text, subjects);
    const covered = (i) => resolved.some((m) => i >= m.start && i < m.end);
    const out = [];
    const re = /@([\w.-]+)/g;
    let m;
    while ((m = re.exec(text)) !== null) if (!covered(m.index)) out.push({ name: m[1], start: m.index });
    return out;
  }

  /** The "@que|ry" or "<que|ry" the caret currently sits in, if any. */
  function activeToken(value, caret) {
    let i = caret - 1;
    while (i >= 0 && caret - i <= MAX_QUERY) {
      const ch = value[i];
      if (ch === MENTION || ch === ANGLE) break;
      if (ch === '\n') return null;
      i--;
    }
    if (i < 0) return null;
    const kind = value[i];
    if (kind !== MENTION && kind !== ANGLE) return null;
    const before = value[i - 1];
    if (before !== undefined && /[\w@<]/.test(before)) return null;
    const query = value.slice(i + 1, caret);
    // A completed label is no longer a query — "<Picture 1>" must stop offering.
    if (kind === ANGLE && !/^[A-Za-z]*(?: ?\d*)?$/.test(query)) return null;
    return { kind, start: i, query };
  }

  /** The spoken-line skeleton, and where the caret should land inside it. */
  function skeleton(language) {
    const text = `<d>[${language}] </d>`;
    return { text, caret: text.indexOf('</d>') };
  }

  function attach(textarea, {
    getSubjects,
    getAssets = () => [],
    getLanguage = () => 'English',
    allowDialogue = false,
  }) {
    const field = document.createElement('div');
    field.className = 'mention-field';
    textarea.parentNode.insertBefore(field, textarea);

    const mirror = document.createElement('div');
    mirror.className = 'mention-mirror';
    mirror.setAttribute('aria-hidden', 'true');

    const menu = document.createElement('div');
    menu.className = 'mention-menu';
    menu.hidden = true;

    field.append(mirror, textarea, menu);
    textarea.setAttribute('autocomplete', 'off');

    let items = [];
    let cursor = 0;
    let active = null; // { kind, start, query, command? }

    // ---------------------------------------------------------- mirror

    function paint() {
      const value = textarea.value;
      const anchor = active ? active.start : null;

      // Mentions win over dialogue when they somehow overlap; nothing nests.
      const marks = [];
      for (const m of findMentions(value, getSubjects())) marks.push({ ...m, mark: 'mention' });
      for (const a of findAssets(value, getAssets())) marks.push({ ...a, mark: 'asset' });
      for (const d of findDialogue(value)) marks.push({ ...d, mark: 'dialogue' });
      marks.sort((a, b) => a.start - b.start || (a.mark === 'dialogue' ? 1 : -1));

      const kept = [];
      for (const mark of marks) if (!kept.length || mark.start >= kept[kept.length - 1].end) kept.push(mark);

      const plain = (text, from) => {
        if (anchor === null || anchor < from || anchor > from + text.length) return escapeHtml(text);
        const cut = anchor - from;
        return `${escapeHtml(text.slice(0, cut))}<span data-anchor></span>${escapeHtml(text.slice(cut))}`;
      };

      let html = '';
      let last = 0;
      for (const mark of kept) {
        html += plain(value.slice(last, mark.start), last);
        const body = value.slice(mark.start, mark.end);
        const marker = anchor === mark.start ? '<span data-anchor></span>' : '';
        if (mark.mark === 'mention') {
          const cls = `mention-pill${mark.speaker ? ' speaks' : ''}`;
          html += `${marker}<span class="${cls}" style="--pill:${mark.subject.color}">${escapeHtml(body)}</span>`;
        } else if (mark.mark === 'asset') {
          const cls = `mention-pill${mark.asset ? '' : ' unknown'}`;
          const colour = mark.asset ? mark.asset.color : 'transparent';
          html += `${marker}<span class="${cls}" style="--pill:${colour}">${escapeHtml(body)}</span>`;
        } else {
          const tag = escapeHtml(value.slice(mark.start, mark.tagEnd));
          const said = escapeHtml(value.slice(mark.tagEnd, mark.closeStart));
          const close = escapeHtml(value.slice(mark.closeStart, mark.end));
          html += `<span class="d-block"><span class="d-tag">${tag}</span>${said}<span class="d-tag">${close}</span></span>`;
        }
        last = mark.end;
      }
      html += plain(value.slice(last), last);

      // A trailing zero-width space keeps the last line open.
      mirror.innerHTML = `${html}&#8203;`;
      mirror.scrollTop = textarea.scrollTop;
    }

    // ---------------------------------------------------------- menu

    function placeMenu() {
      const anchor = mirror.querySelector('[data-anchor]');
      if (!anchor) return;
      const line = parseFloat(getComputedStyle(textarea).lineHeight) || 20;
      const left = Math.max(0, Math.min(anchor.offsetLeft, field.clientWidth - menu.offsetWidth));
      menu.style.left = `${left}px`;
      menu.style.top = `${anchor.offsetTop + line + 5 - textarea.scrollTop}px`;
      menu.classList.remove('above');

      // Flip above the caret rather than let the scroll container clip the list.
      const scroller = textarea.closest('.pane');
      const limit = scroller ? scroller.getBoundingClientRect().bottom : window.innerHeight;
      if (menu.getBoundingClientRect().bottom > limit) {
        menu.style.top = `${anchor.offsetTop - menu.offsetHeight - 5 - textarea.scrollTop}px`;
        menu.classList.add('above');
      }
    }

    function renderMenu(note) {
      if (!items.length) {
        menu.innerHTML = `<div class="mention-empty">${escapeHtml(note)}</div>`;
        return;
      }
      menu.innerHTML = items
          .map(
            (it, i) => `
          <div class="mention-item${i === cursor ? ' selected' : ''}" data-i="${i}">
            ${it.badge ? `<span class="mention-badge" style="--pill:${it.color}">${escapeHtml(it.badge)}</span>` : '<span></span>'}
            <span class="mention-name">${escapeHtml(it.name)}</span>
            <span class="mention-hint">${escapeHtml(it.hint || '')}</span>
          </div>`
          )
          .join('');
    }

    function closeMenu() {
      if (menu.hidden && !active) return;
      active = null;
      menu.hidden = true;
      paint();
    }

    function openMenu() {
      const found = activeToken(textarea.value, textarea.selectionStart);
      const collapsed = textarea.selectionStart === textarea.selectionEnd;
      if (!found || !collapsed) return closeMenu();

      const query = found.query.toLowerCase();
      let note = '';

      if (found.kind === MENTION) {
        const named = getSubjects().filter((s) => s.name);
        items = named
          .filter((s) => s.name.toLowerCase().startsWith(query))
          .map((s) => ({ badge: s.label, color: s.color, name: `${MENTION}${s.name}`, hint: s.hint }));
        // A query that matches nothing is ordinary prose, not a mention attempt.
        if (query && !items.length && named.length) return closeMenu();
        note = 'Name a subject to reference it here.';
      } else {
        const offers = [
          // Dialogue leads where it is allowed, then the assets in declared order.
          ...(allowDialogue
            ? [{
                name: skeleton(getLanguage()).text,
                hint: 'Spoken line — follows the EN / FR toggle',
                dialogue: true,
                match: 'd',
              }]
            : []),
          ...getAssets().map((a) => ({
            badge: a.kind,
            color: a.color,
            name: a.label,
            hint: a.description.trim(),
            match: `${a.kind} ${a.index}`.toLowerCase(),
          })),
        ];
        items = offers.filter((o) => o.match.startsWith(query));
        if (!items.length) return closeMenu();
      }
      active = found;

      cursor = 0;
      renderMenu(note);
      menu.hidden = false;
      paint();
      placeMenu();
    }

    function replace(start, end, text, caretOffset) {
      textarea.setRangeText(text, start, end, 'end');
      const caret = start + (caretOffset ?? text.length);
      textarea.setSelectionRange(caret, caret);
      closeMenu();
      textarea.dispatchEvent(new Event('input', { bubbles: true }));
      textarea.focus();
    }

    function accept(index) {
      const item = items[index];
      if (!item || !active) return;
      const caret = textarea.selectionStart;

      if (item.dialogue) {
        const built = skeleton(getLanguage());
        return replace(active.start, caret, built.text, built.caret);
      }

      const needsSpace = textarea.value[caret] !== ' ';
      replace(active.start, caret, `${item.name}${needsSpace ? ' ' : ''}`);
    }

    // ---------------------------------------------------------- events

    textarea.addEventListener('input', () => { paint(); openMenu(); });
    textarea.addEventListener('click', openMenu);
    textarea.addEventListener('blur', () => setTimeout(closeMenu, 120));
    textarea.addEventListener('scroll', () => { mirror.scrollTop = textarea.scrollTop; placeMenu(); });

    textarea.addEventListener('keydown', (e) => {
      if (menu.hidden) return;
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        if (!items.length) return;
        e.preventDefault();
        cursor = (cursor + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
        renderMenu('');
        placeMenu();
      } else if (e.key === 'Enter' || e.key === 'Tab') {
        if (!items.length) return closeMenu();
        e.preventDefault();
        accept(cursor);
      } else if (e.key === 'Escape') {
        e.preventDefault();
        closeMenu();
      } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') {
        setTimeout(openMenu, 0);
      }
    });

    menu.addEventListener('mousedown', (e) => {
      const row = e.target.closest('.mention-item');
      if (!row) return;
      e.preventDefault();
      accept(Number(row.dataset.i));
    });

    paint();
    // `typingAt` lets validation ignore the token currently under the caret,
    // so a half-typed "@t" is never reported as a broken reference.
    return { refresh: paint, typingAt: () => (active ? active.start : null) };
  }

  return { attach, resolve, canonical, findMentions, findAssets, findDialogue, danglingMentions, MENTION, ANGLE };
})();
