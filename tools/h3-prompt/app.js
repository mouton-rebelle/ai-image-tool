/* H3 Prompt Builder — Ref2VA (full-reference mode)
 * No dependencies. State -> pure serializer -> console.
 * The serializer is deliberately the only place that knows the prompt format,
 * so it can be ported as-is to the ComfyUI node later.
 */
(() => {
  'use strict';

  // ---------------------------------------------------------------- config

  const FPS = 24;
  const MAX_SUBJECTS = 5;

  // H3 accepts frame counts of the form 17k + 5. Full set between ~5 s and ~15 s.
  const FRAME_OPTIONS = [];
  for (let k = 7; k <= 21; k++) FRAME_OPTIONS.push(17 * k + 5);
  const DEFAULT_FRAMES = 192; // 8.00 s — the only whole-second value

  const SUBJECT_COLORS = ['#4ea1ff', '#f7b955', '#5fd39b', '#f472b6', '#a78bfa'];

  // ref-en.md §2 — the non-subject reference labels, each with its own colour.
  const ASSET_KINDS = [
    ['Picture', '#56c8e0'],
    ['Video', '#e08a5a'],
    ['Audio', '#b58cf0'],
  ];
  const kindColor = (kind) => ASSET_KINDS.find(([k]) => k === kind)?.[1] ?? '#8b95a3';

  // ref-en.md §3 — the task-type prefix of `summary`.
  const TASK_TYPES = [
    { id: 'keyframe completion', hint: 'An image is a concrete frame anchor: first frame, last frame, keyframe.' },
    { id: 'reference generation', hint: 'An asset guides a character, scene, style, action or camera — without being a concrete frame or an edited source.' },
    { id: 'video editing', hint: 'An existing source video is directly modified.' },
    { id: 'video continuation', hint: 'New content continues, extends or transitions from an existing source video.' },
    { id: 'audio reuse', hint: 'The same audio signal is reused, in full or in part.' },
    { id: 'audio reference', hint: 'Only the style, timbre, content or texture of an audio signal is referenced, not the signal itself.' },
  ];

  // ref-en.md §4.1 — the fixed relationship markers of `retention_analysis`.
  const MARKERS = [
    ['fully_preserved', 'The defined role of the referenced content is fully preserved.'],
    ['partially_preserved', 'Still used, but some defined characteristics are changed or only partially retained.'],
    ['attribute_transfer', 'Referenced characteristics are transferred to a different identifiable target subject.'],
    ['weak_reference', 'Only broad similarity in style, category, composition or atmosphere is retained.'],
  ];

  const EDIT_OPENER = 'The target video is an edited version of';
  const LANGUAGES = ['English', 'French'];

  // ---------------------------------------------------------------- state

  let uid = 0;
  const newSubject = () => ({
    id: ++uid,
    name: '',
    description: '',
    descriptionTouched: false,
    marker: MARKERS[0][0],
    reason: '',
  });
  const newBeat = () => ({ id: ++uid, text: '', frame: null, shot: false });
  const newAsset = (kind) => ({ id: ++uid, kind, description: '' });

  const state = {
    frames: DEFAULT_FRAMES,
    subjects: [newSubject()],
    assets: [],
    summary: { taskTypes: [], text: '' },
    style: '',
    beats: [newBeat()],
    soundscape: '',
    // Most clips carry no audience-only score, and the guide wants it said.
    music: 'N/A',
    language: 'English',
  };

  // ---------------------------------------------------------------- helpers

  const seconds = (frames) => frames / FPS;
  const timecode = (sec) => Timeline.format(sec);
  const stampOf = (frame) => timecode(frame / FPS);
  const colorOf = (index) => SUBJECT_COLORS[index % SUBJECT_COLORS.length];

  /** The subject list as the mention layer wants it. Labels follow the current
   *  position, so reordering or deleting a subject can never leave a stale one. */
  const baseRefs = (s) =>
    s.subjects.map((subject, i) => ({
      name: subject.name.trim(),
      label: `<Subject ${i + 1}>`,
      color: colorOf(i),
      hint: subject.description.trim(),
    }));

  /** Reference assets, numbered per kind by their position in the list. */
  function assetRefs(s) {
    const seen = { Picture: 0, Video: 0, Audio: 0 };
    return s.assets.map((asset) => {
      const index = (seen[asset.kind] += 1);
      return {
        id: asset.id,
        kind: asset.kind,
        index,
        label: `<${asset.kind} ${index}>`,
        color: kindColor(asset.kind),
        description: asset.description,
      };
    });
  }

  // Speaker IDs are the writer's business — "@zendaya(S1)" is only picked up so
  // it serialises as "<Subject 1> (S1)" with the spacing the guide expects.
  const refsOf = baseRefs;

  /** Shot 1 is implicit on the opening beat; later beats become shots on demand. */
  const isShot = (beat, i) => i === 0 || beat.shot;

  /** A beat carries weight if it has prose, a cut time, or opens a shot. */
  const liveBeats = (s) => s.beats.filter((b, i) => i === 0 || b.text.trim() || b.frame !== null || b.shot);

  /** Which shots each subject is mentioned in, in playback order. */
  function shotsBySubject(s) {
    const refs = baseRefs(s);
    const map = new Map();
    let shotNo = 0;
    liveBeats(s).forEach((beat, i) => {
      if (isShot(beat, i)) shotNo += 1;
      if (!shotNo) return;
      for (const m of Mentions.findMentions(beat.text, refs)) {
        const key = m.subject.name.toLowerCase();
        if (!map.has(key)) map.set(key, new Set());
        map.get(key).add(shotNo);
      }
    });
    return map;
  }

  const shotsOf = (s, subject, map = shotsBySubject(s)) =>
    [...(map.get(subject.name.trim().toLowerCase()) ?? [])].sort((a, b) => a - b);

  /** Frame window a beat's cut time may fall in, given its neighbours. */
  function boundsFor(index) {
    let min = 1;
    let max = state.frames - 1;
    for (let i = index - 1; i >= 0; i--) if (state.beats[i].frame !== null) { min = state.beats[i].frame + 1; break; }
    for (let i = index + 1; i < state.beats.length; i++) if (state.beats[i].frame !== null) { max = state.beats[i].frame - 1; break; }
    return { min, max };
  }

  // ---------------------------------------------------------------- prose fields

  // Every field that accepts @mentions, so rename and validation can walk them.
  const mentionHandles = new Map(); // textarea -> handle
  const beatInputs = new Map(); // beat id -> textarea
  const reasonInputs = new Map(); // subject id -> textarea
  const descInputs = new Map(); // subject id -> textarea
  const assetInputs = new Map(); // asset id -> textarea

  const proseFields = (s) => [
    ...s.subjects.map((subject, i) => ({
      key: `<Subject ${i + 1}>`,
      get: () => subject.description,
      set: (v) => { subject.description = v; },
      el: () => descInputs.get(subject.id),
    })),
    ...assetRefs(s).map((asset) => ({
      key: asset.label,
      get: () => asset.description,
      set: (v) => { state.assets.find((a) => a.id === asset.id).description = v; },
      el: () => assetInputs.get(asset.id),
    })),
    { key: 'summary', get: () => s.summary.text, set: (v) => { s.summary.text = v; }, el: () => els.summaryText },
    { key: 'style opening', get: () => s.style, set: (v) => { s.style = v; }, el: () => els.style },
    { key: 'overall_soundscape', get: () => s.soundscape, set: (v) => { s.soundscape = v; }, el: () => els.soundscape },
    { key: 'non_diegetic_music', get: () => s.music, set: (v) => { s.music = v; }, el: () => els.music },
    ...s.subjects.map((subject, i) => ({
      key: `retention ${i + 1}`,
      get: () => subject.reason,
      set: (v) => { subject.reason = v; },
      el: () => reasonInputs.get(subject.id),
    })),
    ...s.beats.map((b, i) => ({
      key: `beat ${i + 1}`,
      get: () => b.text,
      set: (v) => { b.text = v; },
      el: () => beatInputs.get(b.id),
    })),
  ];

  const refreshMentions = () => mentionHandles.forEach((h) => h.refresh());

  function attachMentions(textarea, { dialogue = false } = {}) {
    const handle = Mentions.attach(textarea, {
      getSubjects: () => refsOf(state),
      getAssets: () => assetRefs(state),
      getLanguage: () => state.language,
      // Complete spoken lines belong to detailed_description only.
      allowDialogue: dialogue,
    });
    mentionHandles.set(textarea, handle);
    return handle;
  }

  /** Rewrite "@old" as "@new" everywhere when a subject is renamed, so a
   *  reference never silently goes dangling under the user. */
  function renameMentions(previous, next) {
    if (!previous || !next || previous === next) return;
    const re = new RegExp(`@${previous.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}(?![\\w])`, 'gi');
    for (const field of proseFields(state)) {
      const updated = field.get().replace(re, `@${next}`);
      if (updated === field.get()) continue;
      field.set(updated);
      const el = field.el();
      if (!el) continue;
      const caret = el.selectionStart;
      el.value = updated;
      el.setSelectionRange(caret, caret);
    }
  }

  // ---------------------------------------------------------------- serializer

  const TODO = '…';

  /** Pure: state -> prompt text. Empty fields become a visible TODO marker. */
  function serialize(s) {
    const refs = refsOf(s);
    const say = (text) => Mentions.canonical(Mentions.resolve(text.trim(), refs));
    const sections = [];

    sections.push(
      [
        'subject_definitions:',
        ...s.subjects.map((subject, i) => `<Subject ${i + 1}> is ${say(subject.description) || TODO}`),
        // An asset only earns its own line when it is described; the guide lets
        // an undescribed one live inside whichever subject cites it.
        ...assetRefs(s)
          .filter((a) => a.description.trim())
          .map((a) => `${a.label} is ${say(a.description)}`),
      ].join('\n')
    );

    const shots = shotsBySubject(s);

    // Task types keep the order they were checked in, so the most prominent
    // relationship can lead — the guide's own examples do exactly that.
    const prefix = s.summary.taskTypes.length ? s.summary.taskTypes.join(' + ') : TODO;
    sections.push(`summary:\n[${prefix}] ${say(s.summary.text) || TODO}`);

    const retention = s.subjects.map((subject, i) => {
      const list = shotsOf(s, subject, shots);
      const where = list.length ? ` (appears in ${list.map((n) => `[Shot ${n}]`).join(', ')})` : '';
      return `<Subject ${i + 1}>${where}: ${subject.marker} - ${say(subject.reason) || TODO}`;
    });
    sections.push(`retention_analysis:\n${retention.join('\n')}`);

    // Each beat stands on its own, separated by a blank line.
    let shotNo = 0;
    const blocks = liveBeats(s).map((beat, i) => {
      const stamp = beat.frame === null ? '' : `At ${stampOf(beat.frame)}, `;
      const body = say(beat.text);
      return isShot(beat, i) ? `[Shot ${++shotNo}] ${stamp}${body || TODO}` : `${stamp}${body}`;
    });
    const detail = [say(s.style), ...blocks].filter(Boolean).join('\n\n');
    sections.push(`detailed_description:\n${detail}`);

    sections.push(`overall_soundscape:\n${say(s.soundscape) || TODO}`);
    sections.push(`non_diegetic_music:\n${say(s.music) || TODO}`);

    return sections.join('\n\n');
  }

  // ---------------------------------------------------------------- validation

  /** Beat ids whose cut time is unusable — surfaced on the chip as well. */
  function badStamps(s) {
    const bad = new Set();
    if (s.beats[0] && s.beats[0].frame !== null) bad.add(s.beats[0].id);
    let previous = null;
    s.beats.forEach((beat) => {
      if (beat.frame === null) return;
      if (beat.frame >= s.frames) bad.add(beat.id);
      if (previous !== null && beat.frame <= previous) bad.add(beat.id);
      previous = beat.frame;
    });
    return bad;
  }

  function issuesOf(s) {
    const out = [];
    const refs = refsOf(s);

    s.subjects.forEach((subject, i) => {
      if (!subject.description.trim()) out.push(`<Subject ${i + 1}> has no description yet.`);
    });

    const names = s.subjects.map((subject) => subject.name.trim().toLowerCase()).filter(Boolean);
    [...new Set(names.filter((n, i) => names.indexOf(n) !== i))].forEach((n) =>
      out.push(`Two subjects share the name “${n}” — mentions cannot tell them apart.`)
    );

    const shots = shotsBySubject(s);
    s.subjects.forEach((subject, i) => {
      if (!subject.reason.trim()) out.push(`retention_analysis: <Subject ${i + 1}> has no justification.`);
      if (subject.name.trim() && !shotsOf(s, subject, shots).length) {
        out.push(`<Subject ${i + 1}> is defined but never mentioned in a beat.`);
      }
    });

    const assets = assetRefs(s);
    for (const field of proseFields(s)) {
      [...new Set(
        Mentions.findAssets(field.get(), assets).filter((a) => !a.asset).map((a) => a.label)
      )].forEach((label) =>
        out.push(
          label.includes('?')
            ? `${field.key}: ${label} lost its reference — point it at another one or remove it.`
            : `${field.key}: ${label} is not declared in References.`
        )
      );
    }

    if (!s.summary.taskTypes.length) out.push('summary: pick at least one task type.');
    if (!s.summary.text.trim()) out.push('summary: the paragraph is empty.');
    if (s.summary.taskTypes.includes('video editing') && !s.summary.text.trim().startsWith(EDIT_OPENER)) {
      out.push(`video editing: the paragraph should start with “${EDIT_OPENER} <Video 1>.”`);
    }

    // Mentions and hand-typed labels, across every prose field.
    for (const field of proseFields(s)) {
      const text = field.get();
      const typing = mentionHandles.get(field.el())?.typingAt();
      const dangling = Mentions.danglingMentions(text, refs).filter((m) => m.start !== typing);
      [...new Set(dangling.map((m) => m.name))].forEach((name) =>
        out.push(`${field.key}: @${name} matches no subject name.`)
      );
      // The guide forbids introducing a label that subject_definitions never defined.
      [...new Set([...text.matchAll(/<Subject (\d+)>/gi)].map((m) => Number(m[1])))]
        .filter((n) => n < 1 || n > s.subjects.length)
        .forEach((n) => out.push(`${field.key}: <Subject ${n}> is not defined.`));
    }

    // Cut times: strictly increasing, inside the clip, and never on Shot 1.
    if (s.beats[0].frame !== null) {
      out.push('Beat 1 opens the video — Shot 1 carries no timestamp. Clear its cut time.');
    }
    let previous = null;
    let previousIndex = 0;
    s.beats.forEach((beat, i) => {
      if (beat.frame === null) return;
      if (beat.frame >= s.frames) {
        out.push(`Beat ${i + 1}: ${stampOf(beat.frame)} falls outside the ${stampOf(s.frames)} clip.`);
      }
      if (previous !== null && beat.frame <= previous && i > 0) {
        out.push(
          `Beat ${i + 1}: ${stampOf(beat.frame)} is not after beat ${previousIndex + 1} at ${stampOf(previous)}.`
        );
      }
      previous = beat.frame;
      previousIndex = i;
    });
    s.beats.forEach((beat, i) => {
      if (i > 0 && beat.shot && beat.frame === null) {
        out.push(`Beat ${i + 1} starts a new shot but has no cut time.`);
      }
    });

    // Spoken lines: the guide is strict about the tag, the content and the
    // closing punctuation. Who says it is left to the writer.
    s.beats.forEach((beat, i) => {
      Mentions.findDialogue(beat.text).forEach((d) => {
        if (!d.closed) out.push(`Beat ${i + 1}: a spoken line is not closed with </d>.`);
        if (!LANGUAGES.includes(d.lang)) {
          out.push(`Beat ${i + 1}: language tag [${d.lang}] should be [English] or [French].`);
        }
        const said = d.said.trim();
        if (!said) out.push(`Beat ${i + 1}: the spoken line is empty.`);
        // The guide asks for a closing . ? or ! — [unclear] spans are exempt.
        else if (!/[.?!\]]$/.test(said)) out.push(`Beat ${i + 1}: end the spoken line with . ? or ! before </d>.`);
      });
    });

    // Speaker IDs belong to detailed_description only (ref-en.md §5.4).
    [
      ['summary', s.summary.text],
      ['style opening', s.style],
      ['overall_soundscape', s.soundscape],
      ['non_diegetic_music', s.music],
      // The guide forbids speaker IDs in retention_analysis outright.
      ...s.subjects.map((subject, i) => [`retention_analysis <Subject ${i + 1}>`, subject.reason]),
    ].forEach(([key, text]) => {
      if (Mentions.findMentions(text, refs).some((m) => m.speaker)) {
        out.push(`${key}: speaker IDs belong in detailed_description, not here.`);
      }
    });

    if (!s.soundscape.trim()) out.push('overall_soundscape: describe the ambience and physical sounds.');
    if (!s.music.trim()) out.push('non_diegetic_music: describe the score, or write N/A.');
    // The guide keeps complete dialogue and lyrics inside <d> in the beats only.
    [['overall_soundscape', s.soundscape], ['non_diegetic_music', s.music]].forEach(([key, text]) => {
      if (Mentions.findDialogue(text).length) out.push(`${key}: spoken lines belong in the beats, not here.`);
    });

    return out;
  }

  // ---------------------------------------------------------------- DOM refs

  const $ = (sel) => document.querySelector(sel);
  const els = {
    duration: $('#duration'),
    durationHint: $('#duration-hint'),
    list: $('#subject-list'),
    count: $('#subject-count'),
    add: $('#add-subject'),
    taskTypes: $('#task-types'),
    summaryText: $('#summary-text'),
    summaryCount: $('#summary-count'),
    style: $('#style-opening'),
    lang: $('#lang'),
    assetList: $('#asset-list'),
    assetCount: $('#asset-count'),
    retentionList: $('#retention-list'),
    retentionCount: $('#retention-count'),
    beatList: $('#beat-list'),
    soundscape: $('#soundscape'),
    music: $('#music'),
    musicNA: $('#music-na'),
    beatCount: $('#beat-count'),
    addBeat: $('#add-beat'),
    importBtn: $('#import'),
    importPanel: $('#import-panel'),
    importText: $('#import-text'),
    importGo: $('#import-go'),
    importCancel: $('#import-cancel'),
    importNote: $('#import-note'),
    output: $('#output'),
    stats: $('#prompt-stats'),
    issues: $('#issues'),
    copy: $('#copy'),
  };

  function autoGrow(textarea) {
    textarea.style.height = 'auto';
    textarea.style.height = `${textarea.scrollHeight}px`;
  }

  const escapeHtml = (str) =>
    str.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' })[c]);

  // ---------------------------------------------------------------- subjects

  function subjectCard(subject, index) {
    const card = document.createElement('div');
    card.className = 'subject';
    card.style.setProperty('--subject-color', colorOf(index));

    card.innerHTML = `
      <div class="subject-head">
        <span class="subject-tag">&lt;Subject ${index + 1}&gt;</span>
        <input type="text" class="subject-name" data-name placeholder="alias" autocomplete="off" spellcheck="false"
               data-tip="Short alias, typed as @alias in the fields below. Never reaches the prompt.">
        <button type="button" class="btn-icon" data-remove data-tip="Remove subject">&times;</button>
      </div>
      <textarea rows="2" data-description
        placeholder="Timothée Chalamet, in his Paul Atreides outfit on the Dune set. Type &lt; to cite a reference."></textarea>`;

    const nameInput = card.querySelector('[data-name]');
    const descInput = card.querySelector('[data-description]');
    const removeBtn = card.querySelector('[data-remove]');

    nameInput.value = subject.name;
    descInput.value = subject.description;
    descInputs.set(subject.id, descInput);
    attachMentions(descInput);

    nameInput.addEventListener('input', () => {
      const previous = subject.name.trim();
      subject.name = nameInput.value;
      // The name pre-fills the description until the user writes their own.
      if (!subject.descriptionTouched) {
        subject.description = subject.name;
        descInput.value = subject.description;
        autoGrow(descInput);
      }
      renameMentions(previous, subject.name.trim());
      refreshMentions();
      renderConsole();
    });

    descInput.addEventListener('input', () => {
      subject.description = descInput.value;
      // Re-link when emptied, so the field can be reset by clearing it.
      subject.descriptionTouched = descInput.value.trim() !== '';
      autoGrow(descInput);
      renderConsole();
    });

    removeBtn.addEventListener('click', () => removeSubject(subject.id));
    removeBtn.disabled = state.subjects.length <= 1;

    requestAnimationFrame(() => autoGrow(descInput));
    return card;
  }

  function renderSubjects(focusId) {
    descInputs.forEach((el) => mentionHandles.delete(el));
    descInputs.clear();
    els.list.replaceChildren(...state.subjects.map(subjectCard));
    els.count.textContent = `${state.subjects.length} / ${MAX_SUBJECTS}`;
    els.add.disabled = state.subjects.length >= MAX_SUBJECTS;
    renderRetention();
    refreshMentions();

    if (focusId != null) {
      const i = state.subjects.findIndex((s) => s.id === focusId);
      if (i >= 0) els.list.children[i].querySelector('[data-name]').focus();
    }
  }

  function addSubject() {
    if (state.subjects.length >= MAX_SUBJECTS) return;
    const subject = newSubject();
    state.subjects.push(subject);
    renderSubjects(subject.id);
    renderConsole();
  }

  function removeSubject(id) {
    if (state.subjects.length <= 1) return;
    state.subjects = state.subjects.filter((s) => s.id !== id);
    renderSubjects();
    renderConsole();
  }

  // ---------------------------------------------------------------- assets

  function assetCard(ref) {
    const asset = state.assets.find((a) => a.id === ref.id);
    const card = document.createElement('div');
    card.className = 'subject';
    card.style.setProperty('--subject-color', ref.color);
    card.innerHTML = `
      <div class="subject-head">
        <span class="subject-tag">${escapeHtml(ref.label)}</span>
        <span class="shots">${ref.description.trim() ? 'own line in subject_definitions' : 'cited only, no line of its own'}</span>
        <button type="button" class="btn-icon" data-remove data-tip="Remove reference">&times;</button>
      </div>
      <textarea rows="1" data-desc
        placeholder="describe it only if it needs its own definition line"></textarea>`;

    const desc = card.querySelector('[data-desc]');
    desc.value = asset.description;
    assetInputs.set(asset.id, desc);
    attachMentions(desc);

    desc.addEventListener('input', () => {
      asset.description = desc.value;
      autoGrow(desc);
      card.querySelector('.shots').textContent = desc.value.trim()
        ? 'own line in subject_definitions'
        : 'cited only, no line of its own';
      renderConsole();
    });

    card.querySelector('[data-remove]').addEventListener('click', () => removeAsset(asset.id));
    requestAnimationFrame(() => autoGrow(desc));
    return card;
  }

  function renderAssets() {
    assetInputs.forEach((el) => mentionHandles.delete(el));
    assetInputs.clear();
    const refs = assetRefs(state);
    els.assetList.replaceChildren(...refs.map(assetCard));
    els.assetCount.textContent = refs.length ? `${refs.length}` : 'none';
    refreshMentions();
  }

  function addAsset(kind) {
    const asset = newAsset(kind);
    // Group by kind so the numbering reads in the order the list shows.
    const last = state.assets.map((a) => a.kind).lastIndexOf(kind);
    state.assets.splice(last < 0 ? state.assets.length : last + 1, 0, asset);
    renderAssets();
    renderConsole();
    assetInputs.get(asset.id)?.focus();
  }

  /** Deleting shifts every label above it, so all citations are rewritten in one
   *  pass. Citations of the deleted reference itself become "<Picture ?>": they
   *  must break loudly rather than quietly re-point at the next image along. */
  function removeAsset(id) {
    const before = assetRefs(state);
    const gone = before.find((a) => a.id === id);
    state.assets = state.assets.filter((a) => a.id !== id);
    const after = assetRefs(state);

    const moves = new Map([[gone.label, `<${gone.kind} ?>`]]);
    for (const old of before) {
      const now = after.find((a) => a.id === old.id);
      if (now && now.label !== old.label) moves.set(old.label, now.label);
    }

    const re = /<(?:Picture|Video|Audio) \d+>/g;
    for (const field of proseFields(state)) {
      const updated = field.get().replace(re, (label) => moves.get(label) ?? label);
      if (updated === field.get()) continue;
      field.set(updated);
      const el = field.el();
      if (el) el.value = updated;
    }

    renderAssets();
    renderSubjects();
    renderBeats();
    renderConsole();
  }

  // ---------------------------------------------------------------- retention

  function retentionCard(subject, index) {
    const card = document.createElement('div');
    card.className = 'subject';
    card.style.setProperty('--subject-color', colorOf(index));
    card.innerHTML = `
      <div class="subject-head">
        <span class="subject-tag">&lt;Subject ${index + 1}&gt;</span>
        <span class="shots" data-shots></span>
        <select data-marker>
          ${MARKERS.map(([id]) => `<option value="${id}">${id}</option>`).join('')}
        </select>
      </div>
      <textarea rows="1" data-reason
        placeholder="what of it is kept, changed or only echoed"></textarea>`;

    const marker = card.querySelector('[data-marker]');
    const reason = card.querySelector('[data-reason]');
    marker.value = subject.marker;
    reason.value = subject.reason;
    reasonInputs.set(subject.id, reason);
    attachMentions(reason);

    const describeMarker = () => {
      marker.dataset.tip = MARKERS.find(([id]) => id === subject.marker)?.[1] ?? '';
    };

    marker.addEventListener('change', () => {
      subject.marker = marker.value;
      describeMarker();
      renderConsole();
    });

    reason.addEventListener('input', () => {
      subject.reason = reason.value;
      autoGrow(reason);
      renderConsole();
    });

    describeMarker();
    requestAnimationFrame(() => autoGrow(reason));
    return card;
  }

  /** Shot membership shifts on every beat keystroke, so it is patched in place
   *  rather than redrawing the cards and tearing down their mention layers. */
  function renderShotLists() {
    const shots = shotsBySubject(state);
    state.subjects.forEach((subject, i) => {
      const el = els.retentionList.children[i]?.querySelector('[data-shots]');
      if (!el) return;
      const list = shotsOf(state, subject, shots);
      el.textContent = list.length ? list.map((n) => `[Shot ${n}]`).join(', ') : 'in no shot';
      el.classList.toggle('none', !list.length);
    });
  }

  function renderRetention() {
    reasonInputs.forEach((el) => mentionHandles.delete(el));
    reasonInputs.clear();
    els.retentionList.replaceChildren(...state.subjects.map(retentionCard));
    els.retentionCount.textContent = `${state.subjects.length} line${state.subjects.length > 1 ? 's' : ''}`;
    renderShotLists();
    refreshMentions();
  }

  // ---------------------------------------------------------------- beats

  let dragId = null;

  function beatCard(beat, index) {
    const shot = isShot(beat, index);
    const first = index === 0;
    const bounds = boundsFor(index);
    const noRoom = bounds.min > bounds.max;
    const invalid = badStamps(state).has(beat.id);

    const card = document.createElement('div');
    card.className = `beat${shot ? ' is-shot' : ''}${beat.id === dragId ? ' dragging' : ''}`;
    card.dataset.id = beat.id;

    card.innerHTML = `
      <div class="beat-grip" data-grip data-tip="Drag to reorder">⣿</div>
      <div class="beat-main">
        <div class="beat-bar">
          ${shot ? `<span class="beat-tag">Shot ${shotNumber(index)}</span>` : ''}
          <button type="button" class="chip${beat.frame === null ? ' empty' : ''}${invalid ? ' invalid' : ''}" data-time
            ${first || noRoom ? 'disabled' : ''}
            data-tip="${first
              ? 'Shot 1 opens the video, so it carries no timestamp.'
              : noRoom
                ? 'No frame left between the surrounding cut times.'
                : `Set a cut time between ${stampOf(bounds.min)} and ${stampOf(bounds.max)}`}">
            ${beat.frame === null ? 'T' : stampOf(beat.frame)}
          </button>
          <button type="button" class="chip icon${beat.shot ? ' on' : ''}" data-shot
            ${first || beat.frame === null ? 'disabled' : ''}
            data-tip="${first
              ? 'The opening beat is always Shot 1.'
              : beat.frame === null
                ? 'Set a cut time first — a new shot has to start on one.'
                : 'Cut here: turn this beat into a new shot'}">✂</button>
          <button type="button" class="btn-icon" data-remove data-tip="Remove beat">&times;</button>
        </div>
        <textarea rows="2" data-text placeholder="${first
          ? 'The scene opens on @tim standing at the crest of a dune, the camera pushes in with small amplitude at slow speed.'
          : 'He turns toward the horizon as the wind lifts the sand.'}"></textarea>
      </div>`;

    const text = card.querySelector('[data-text]');
    text.value = beat.text;
    beatInputs.set(beat.id, text);
    attachMentions(text, { dialogue: true });

    text.addEventListener('input', () => {
      beat.text = text.value;
      autoGrow(text);
      renderConsole();
    });

    // Leaving a filled last beat opens the next one, so the list keeps flowing.
    // The new card is appended rather than re-rendered: a full redraw here would
    // replace the very button the blur came from, swallowing the click.
    text.addEventListener('blur', () => {
      if (beat !== state.beats[state.beats.length - 1] || !beat.text.trim()) return;
      const next = newBeat();
      state.beats.push(next);
      els.beatList.append(beatCard(next, state.beats.length - 1));
      els.beatList.querySelectorAll('[data-remove]').forEach((b) => { b.disabled = false; });
      renderBeatCount();
      renderConsole();
    });

    const timeBtn = card.querySelector('[data-time]');
    timeBtn.addEventListener('click', () => {
      // Bounds are read at open time, not at card-build time, so they reflect
      // any cut moved since this card was drawn.
      const live = boundsFor(state.beats.indexOf(beat));
      Timeline.open({
        anchor: timeBtn,
        total: state.frames,
        fps: FPS,
        value: beat.frame,
        min: live.min,
        max: live.max,
        marks: state.beats
          .filter((b) => b !== beat && b.frame !== null)
          .map((b) => ({ frame: b.frame, shot: b.shot, label: stampOf(b.frame) })),
        // Scrubbing only touches this chip: a full redraw would detach the very
        // button the popover is anchored to.
        onPick: (frame) => {
          beat.frame = frame;
          timeBtn.textContent = stampOf(frame);
          timeBtn.classList.remove('empty');
          renderConsole();
        },
        onClear: () => {
          beat.frame = null;
          beat.shot = false;
          renderBeats();
          renderConsole();
        },
        // Neighbouring bounds and shot numbering catch up once the picker is gone.
        onClose: () => { renderBeats(); renderConsole(); },
      });
    });

    card.querySelector('[data-shot]').addEventListener('click', () => {
      beat.shot = !beat.shot;
      renderBeats();
      renderConsole();
    });

    const removeBtn = card.querySelector('[data-remove]');
    removeBtn.disabled = state.beats.length <= 1;
    removeBtn.addEventListener('click', () => {
      state.beats = state.beats.filter((b) => b !== beat);
      if (!state.beats.length) state.beats = [newBeat()];
      renderBeats();
      renderConsole();
    });

    // Dragging is armed by the grip only, so text selection stays untouched.
    const grip = card.querySelector('[data-grip]');
    grip.addEventListener('mousedown', () => { card.draggable = true; });
    card.addEventListener('dragstart', (e) => {
      dragId = beat.id;
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', String(beat.id));
      requestAnimationFrame(() => card.classList.add('dragging'));
    });
    card.addEventListener('dragend', () => {
      dragId = null;
      card.draggable = false;
      card.classList.remove('dragging');
      els.beatList.querySelector('.drop-line')?.remove();
    });

    requestAnimationFrame(() => autoGrow(text));
    return card;
  }

  function shotNumber(index) {
    let n = 0;
    for (let i = 0; i <= index; i++) if (isShot(state.beats[i], i)) n++;
    return n;
  }

  function dropIndexAt(y) {
    const rows = [...els.beatList.querySelectorAll('.beat')];
    for (let i = 0; i < rows.length; i++) {
      const r = rows[i].getBoundingClientRect();
      if (y < r.top + r.height / 2) return i;
    }
    return rows.length;
  }

  els.beatList.addEventListener('dragover', (e) => {
    if (dragId === null) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const index = dropIndexAt(e.clientY);
    let line = els.beatList.querySelector('.drop-line');
    if (!line) {
      line = document.createElement('div');
      line.className = 'drop-line';
      els.beatList.append(line);
    }
    const rows = [...els.beatList.querySelectorAll('.beat')];
    els.beatList.insertBefore(line, rows[index] ?? null);
  });

  els.beatList.addEventListener('drop', (e) => {
    if (dragId === null) return;
    e.preventDefault();
    const from = state.beats.findIndex((b) => b.id === dragId);
    let to = dropIndexAt(e.clientY);
    if (to > from) to -= 1;
    const [moved] = state.beats.splice(from, 1);
    state.beats.splice(to, 0, moved);
    dragId = null;
    renderBeats();
    renderConsole();
  });

  function renderBeatCount() {
    const shots = state.beats.filter((b, i) => isShot(b, i)).length;
    els.beatCount.textContent = `${shots} shot${shots > 1 ? 's' : ''} · ${state.beats.length} beat${state.beats.length > 1 ? 's' : ''}`;
  }

  function renderBeats() {
    // Drop the handles of the textareas about to be detached.
    beatInputs.forEach((el) => mentionHandles.delete(el));
    beatInputs.clear();
    els.beatList.replaceChildren(...state.beats.map(beatCard));
    renderBeatCount();
    refreshMentions();
  }

  function addBeat() {
    const beat = newBeat();
    state.beats.push(beat);
    renderBeats();
    renderConsole();
    beatInputs.get(beat.id)?.focus();
  }

  // ---------------------------------------------------------------- summary

  const ms = {
    root: els.taskTypes,
    trigger: els.taskTypes.querySelector('[data-trigger]'),
    value: els.taskTypes.querySelector('[data-value]'),
    panel: els.taskTypes.querySelector('[data-panel]'),
  };

  function buildTaskTypePanel() {
    ms.panel.replaceChildren(
      ...TASK_TYPES.map((type) => {
        const row = document.createElement('label');
        row.className = 'ms-option';
        row.innerHTML = `
          <input type="checkbox" value="${type.id}">
          <span>
            <span class="ms-option-label">${type.id}</span>
            <span class="ms-option-hint">${type.hint}</span>
          </span>`;
        const box = row.querySelector('input');
        box.addEventListener('change', () => {
          if (box.checked) {
            if (!state.summary.taskTypes.includes(type.id)) state.summary.taskTypes.push(type.id);
          } else {
            state.summary.taskTypes = state.summary.taskTypes.filter((t) => t !== type.id);
          }
          renderTaskTypes();
          renderConsole();
        });
        return row;
      })
    );
  }

  function renderTaskTypes() {
    const selected = state.summary.taskTypes;
    ms.value.textContent = selected.length ? `[${selected.join(' + ')}]` : 'Task types…';
    ms.value.classList.toggle('placeholder', selected.length === 0);
    ms.panel.querySelectorAll('input').forEach((box) => { box.checked = selected.includes(box.value); });
    els.summaryCount.textContent = selected.length
      ? `${selected.length} type${selected.length > 1 ? 's' : ''}`
      : 'no type';
  }

  function togglePanel(open) {
    const next = open ?? ms.panel.hidden;
    ms.panel.hidden = !next;
    ms.trigger.setAttribute('aria-expanded', String(next));
    ms.root.classList.toggle('open', next);
  }

  ms.trigger.addEventListener('click', (e) => { e.stopPropagation(); togglePanel(); });
  document.addEventListener('click', (e) => {
    if (!ms.panel.hidden && !ms.root.contains(e.target)) togglePanel(false);
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !ms.panel.hidden) { togglePanel(false); ms.trigger.focus(); }
  });

  // ---------------------------------------------------------------- console

  function highlight(text) {
    return escapeHtml(text)
      .replace(/^([a-z_]+):/gm, '<span class="key">$1:</span>')
      .replace(/^\[(?!Shot )([^\]\n]*)\]/gm, '<span class="task">[$1]</span>')
      .replace(/\[Shot (\d+)\]/g, '<span class="shot">[Shot $1]</span>')
      .replace(/At (\d{2}:\d{2}\.\d{3}),/g, 'At <span class="stamp">$1</span>,')
      .replace(
        /&lt;d&gt;\[([^\]\n]*)\]([\s\S]*?)&lt;\/d&gt;/g,
        (_, lang, said) =>
          `<span class="said"><span class="dtag">&lt;d&gt;[${lang}]</span>${said}<span class="dtag">&lt;/d&gt;</span></span>`
      )
      // The speaker ID rides along with the label so it takes the same colour.
      .replace(/&lt;Subject (\d+)&gt;( \(S\d+\))?/g, (_, n, sid) => {
        const color = colorOf(Number(n) - 1);
        const speaker = sid ? `<span class="sid">${sid}</span>` : '';
        return `<span class="ref" style="color:${color}">&lt;Subject ${n}&gt;${speaker}</span>`;
      })
      .replace(new RegExp(TODO, 'g'), `<span class="todo">${TODO}</span>`);
  }

  function renderConsole() {
    renderShotLists();
    const text = serialize(state);
    els.output.innerHTML = highlight(text);
    els.stats.textContent = `${text.length} chars`;

    const issues = issuesOf(state);
    els.issues.classList.toggle('visible', issues.length > 0);
    els.issues.innerHTML = issues.length
      ? `<ul>${issues.map((i) => `<li>${escapeHtml(i)}</li>`).join('')}</ul>`
      : '';
  }

  // ---------------------------------------------------------------- duration

  function renderDurationOptions() {
    els.duration.replaceChildren(
      ...FRAME_OPTIONS.map((frames) => {
        const opt = document.createElement('option');
        opt.value = String(frames);
        opt.textContent = `${frames}f · ${seconds(frames).toFixed(2)}s`;
        return opt;
      })
    );
    els.duration.value = String(state.frames);
  }

  const renderDurationHint = () => {
    els.durationHint.textContent = `${FPS} fps · 00:00.000 → ${stampOf(state.frames)}`;
  };

  // ---------------------------------------------------------------- wiring

  els.add.addEventListener('click', addSubject);
  els.addBeat.addEventListener('click', addBeat);

  els.assetList.closest('.block').addEventListener('click', (e) => {
    const btn = e.target.closest('[data-add-asset]');
    if (btn) addAsset(btn.dataset.addAsset);
  });

  /** The toggle is the language of the video, not just of the next insertion:
   *  switching it retags the lines already written. Toggling back restores them. */
  function applyLanguage(next) {
    state.language = next;
    for (const beat of state.beats) {
      const out = beat.text.replace(/(<d>\[)(English|French)(\])/g, `$1${next}$3`);
      if (out === beat.text) continue;
      beat.text = out;
      const el = beatInputs.get(beat.id);
      if (!el) continue;
      const caret = Math.min(el.selectionStart, out.length);
      el.value = out;
      el.setSelectionRange(caret, caret);
    }
  }

  els.lang.addEventListener('click', (e) => {
    const btn = e.target.closest('[data-lang]');
    if (!btn || btn.classList.contains('on')) return;
    applyLanguage(btn.dataset.lang);
    els.lang.querySelectorAll('[data-lang]').forEach((b) => b.classList.toggle('on', b === btn));
    refreshMentions();
    renderConsole();
  });

  els.duration.addEventListener('change', () => {
    state.frames = Number(els.duration.value);
    renderDurationHint();
    renderBeats();
    renderConsole();
  });

  // Mention layers are attached before the state listeners, so their caret
  // tracking is already current when validation runs on the same keystroke.
  attachMentions(els.summaryText);
  attachMentions(els.style);

  els.summaryText.addEventListener('input', () => {
    state.summary.text = els.summaryText.value;
    autoGrow(els.summaryText);
    renderConsole();
  });

  els.style.addEventListener('input', () => {
    state.style = els.style.value;
    autoGrow(els.style);
    renderConsole();
  });

  attachMentions(els.soundscape);
  attachMentions(els.music);

  [['soundscape', els.soundscape], ['music', els.music]].forEach(([key, el]) => {
    el.addEventListener('input', () => {
      state[key] = el.value;
      autoGrow(el);
      renderConsole();
    });
  });

  els.musicNA.addEventListener('click', () => {
    state.music = 'N/A';
    els.music.value = 'N/A';
    autoGrow(els.music);
    refreshMentions();
    renderConsole();
  });

  // ---------------------------------------------------------------- import

  function showImport(on) {
    els.importPanel.hidden = !on;
    els.output.hidden = on;
    els.importBtn.textContent = on ? 'Close' : 'Import';
    if (on) els.importText.focus();
  }

  function reportImport(notes) {
    els.importNote.hidden = !notes.length;
    els.importNote.innerHTML = notes.length
      ? `<strong>Imported.</strong><ul>${notes.map((n) => `<li>${escapeHtml(n)}</li>`).join('')}</ul>`
      : '';
  }

  function applyImport(data) {
    state.subjects = data.subjects.map((sub, i) => ({
      ...newSubject(),
      name: sub.name,
      description: sub.description,
      descriptionTouched: true,
      ...(data.retention[i + 1] ?? {}),
    }));
    state.assets = data.assets.map((a) => ({ ...newAsset(a.kind), description: a.description }));
    state.summary = { taskTypes: data.summary.taskTypes, text: data.summary.text };
    state.style = data.style;
    state.soundscape = data.soundscape;
    state.music = data.music;
    state.beats = data.beats.map((beat) => ({ ...newBeat(), ...beat }));

    // The prompt carries no length, only cut times — grow the clip if one of
    // them lands past the current one.
    const latest = Math.max(0, ...state.beats.map((b) => b.frame ?? 0));
    if (latest >= state.frames) {
      state.frames = FRAME_OPTIONS.find((f) => f > latest) ?? FRAME_OPTIONS[FRAME_OPTIONS.length - 1];
      els.duration.value = String(state.frames);
      renderDurationHint();
    }

    els.summaryText.value = state.summary.text;
    els.style.value = state.style;
    els.soundscape.value = state.soundscape;
    els.music.value = state.music;
    renderAssets();
    renderSubjects();
    renderTaskTypes();
    renderBeats();
    [els.summaryText, els.style, els.soundscape, els.music].forEach(autoGrow);
    refreshMentions();
    renderConsole();
  }

  els.importBtn.addEventListener('click', async () => {
    if (!els.importPanel.hidden) return showImport(false);
    showImport(true);
    // Best effort: pre-fill from the clipboard, but the textarea is the contract.
    try {
      const pasted = await navigator.clipboard.readText();
      if (pasted.trim() && !els.importText.value.trim()) els.importText.value = pasted;
    } catch {
      /* no clipboard permission — the user pastes by hand */
    }
  });

  els.importCancel.addEventListener('click', () => showImport(false));

  els.importGo.addEventListener('click', () => {
    const result = Parser.parse(els.importText.value, {
      fps: FPS,
      maxSubjects: MAX_SUBJECTS,
      taskTypes: TASK_TYPES.map((t) => t.id),
      markers: MARKERS.map(([id]) => id),
    });
    if (!result.ok) return reportImport(result.notes);
    applyImport(result.data);
    els.importText.value = '';
    showImport(false);
    reportImport(result.notes.length ? result.notes : ['Everything was read back.']);
  });

  // The Clipboard API only exists in a secure context (HTTPS or localhost);
  // the tool is also served over plain HTTP on the local network, where the
  // hidden-textarea execCommand fallback is the only way to copy.
  function copyToClipboard(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).catch(() => copyWithLegacyFallback(text));
    }
    return copyWithLegacyFallback(text);
  }

  function copyWithLegacyFallback(text) {
    return new Promise((resolve, reject) => {
      const textArea = document.createElement('textarea');
      textArea.value = text;
      textArea.style.position = 'fixed';
      textArea.style.left = '-999999px';
      textArea.style.top = '-999999px';
      document.body.appendChild(textArea);
      textArea.focus();
      textArea.select();
      try {
        const copied = document.execCommand('copy');
        copied ? resolve() : reject(new Error('execCommand failed'));
      } catch (err) {
        reject(err);
      } finally {
        document.body.removeChild(textArea);
      }
    });
  }

  els.copy.addEventListener('click', async () => {
    try {
      await copyToClipboard(serialize(state));
      els.copy.textContent = 'Copied ✓';
    } catch {
      els.copy.textContent = 'Failed';
    }
    setTimeout(() => { els.copy.textContent = 'Copy'; }, 1300);
  });

  els.music.value = state.music;

  renderDurationOptions();
  renderDurationHint();
  renderAssets();
  renderSubjects();
  buildTaskTypePanel();
  renderTaskTypes();
  renderBeats();
  renderConsole();
})();
