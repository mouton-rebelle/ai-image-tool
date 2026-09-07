/* Reads a Ref2VA prompt back into builder state.
 *
 * Deliberately forgiving: it accepts both the airy format this builder emits
 * (one beat per block, blank line between) and the compact one the guide shows
 * (one line per shot). Anything it cannot place is reported rather than
 * dropped silently — the caller decides what to tell the user.
 *
 * Subject aliases are lost in the round trip, since the prompt only carries
 * numbered labels. Imported subjects come back as s1, s2, … and renaming one
 * rewrites every mention that points at it.
 */
window.Parser = (() => {
  'use strict';

  // Everything below matches case-insensitively: a pasted prompt may well come
  // back from a model with "<subject 1>" or "[shot 2]" in the wrong casing.
  const HEADING = /^([A-Za-z_]+):[ \t]*(.*)$/;
  const KNOWN = [
    'subject_definitions',
    'summary',
    'retention_analysis',
    'detailed_description',
    'overall_soundscape',
    'non_diegetic_music',
  ];

  /** "subject_definitions:\n<Subject 1> is …" -> { subject_definitions: "…" } */
  function splitSections(text) {
    const out = {};
    const order = [];
    let key = null;
    let buf = [];
    for (const line of text.replace(/\r\n?/g, '\n').split('\n')) {
      const m = HEADING.exec(line.trimEnd());
      if (m) {
        if (key) out[key] = buf.join('\n');
        key = m[1].toLowerCase();
        order.push(key);
        buf = m[2] ? [m[2]] : [];
      } else if (key) {
        buf.push(line);
      }
    }
    if (key) out[key] = buf.join('\n');
    return { sections: out, order };
  }

  const stampToFrame = (mm, ss, ms, fps) => Math.round(((+mm * 60 + +ss) * 1000 + +ms) * (fps / 1000));

  /** "<Subject 2> (S1)" -> "@s2(S1)" — the editable form. */
  const unresolve = (text) =>
    text.replace(/<Subject (\d+)>(?: \((S\d+(?:,\s*S\d+)*)\))?/gi, (_, n, ids) =>
      `@s${n}${ids ? `(${ids.replace(/\s+/g, '')})` : ''}`
    );

  function parseSubjects(block, notes) {
    const subjects = [];
    const assets = [];
    for (const line of block.split('\n')) {
      const text = line.trim();
      if (!text) continue;
      const subject = /^<Subject (\d+)>\s+is\s+([\s\S]+)$/i.exec(text);
      if (subject) {
        subjects.push({ index: Number(subject[1]), description: unresolve(subject[2].trim()) });
        continue;
      }
      const asset = /^<(Picture|Video|Audio) (\d+)>\s+is\s+([\s\S]+)$/i.exec(text);
      if (asset) {
        const kind = asset[1][0].toUpperCase() + asset[1].slice(1).toLowerCase();
        assets.push({ kind, index: Number(asset[2]), description: unresolve(asset[3].trim()) });
        continue;
      }
      notes.push(`subject_definitions: could not read “${text.slice(0, 48)}…”.`);
    }
    subjects.sort((a, b) => a.index - b.index);
    return { descriptions: subjects.map((s) => s.description), assets };
  }

  /** Labels cited in the prose but never defined still need a slot, or the
   *  citation would come back pointing at nothing. */
  function collectAssets(declared, texts, notes) {
    const kinds = ['Picture', 'Video', 'Audio'];
    const highest = new Map();
    for (const { kind, index } of declared) highest.set(kind, Math.max(highest.get(kind) ?? 0, index));
    for (const text of texts) {
      const re = /<(Picture|Video|Audio) (\d+)>/gi;
      let m;
      while ((m = re.exec(text)) !== null) {
        const kind = m[1][0].toUpperCase() + m[1].slice(1).toLowerCase();
        highest.set(kind, Math.max(highest.get(kind) ?? 0, Number(m[2])));
      }
    }
    const out = [];
    for (const kind of kinds) {
      const top = highest.get(kind) ?? 0;
      for (let i = 1; i <= top; i++) {
        const found = declared.find((a) => a.kind === kind && a.index === i);
        out.push({ kind, description: found ? found.description : '' });
        if (!found && declared.some((a) => a.kind === kind)) {
          notes.push(`<${kind} ${i}> is cited but never defined — kept as an undescribed reference.`);
        }
      }
    }
    return out;
  }

  /** "<Subject 1> (appears in [Shot 1]): fully_preserved - reason" keyed by index.
   *  The shot list is dropped: the builder derives it from the beats. */
  function parseRetention(block, markers, notes) {
    const out = {};
    for (const line of block.split('\n')) {
      const text = line.trim();
      if (!text) continue;
      const m = /^<Subject (\d+)>(?:\s*\([^)]*\))?:\s*([a-z_]+)\s*-\s*([\s\S]+)$/i.exec(text);
      if (!m) {
        const other = /^<(Picture|Video|Audio) \d+>/i.exec(text);
        notes.push(
          other
            ? `retention_analysis: the ${other[0]} line was dropped — only subjects carry a retention row here.`
            : `retention_analysis: could not read “${text.slice(0, 48)}…”.`
        );
        continue;
      }
      const marker = m[2].toLowerCase();
      if (!markers.includes(marker)) {
        notes.push(`retention_analysis: unknown marker “${m[2]}” — fell back to ${markers[0]}.`);
      }
      out[Number(m[1])] = {
        marker: markers.includes(marker) ? marker : markers[0],
        reason: unresolve(m[3].trim()),
      };
    }
    return out;
  }

  function parseSummary(block, knownTypes, notes) {
    const raw = block.trim();
    const m = /^\[([^\]]*)\]\s*([\s\S]*)$/.exec(raw);
    if (!m) return { taskTypes: [], text: unresolve(raw) };
    const wanted = m[1].split('+').map((t) => t.trim().toLowerCase()).filter(Boolean);
    const taskTypes = wanted.filter((t) => knownTypes.includes(t));
    wanted
      .filter((t) => !knownTypes.includes(t))
      .forEach((t) => notes.push(`summary: unknown task type “${t}” dropped.`));
    return { taskTypes, text: unresolve(m[2].trim()) };
  }

  function parseBeats(block, fps, notes) {
    // One line, one beat. Blank lines are just spacing, whether the prompt uses
    // the airy layout this builder emits or the guide's compact one.
    const lines = block.split('\n').map((line) => line.trim()).filter(Boolean);

    // The style opening is defined as the sentences *before* [Shot 1]. With no
    // shot marker anywhere there is no "before", so every line is a beat.
    const hasShots = lines.some((line) => /^\[Shot \d+\]/i.test(line));

    let style = '';
    const beats = [];
    for (const line of lines) {
      let text = line;
      const shotMatch = /^\[Shot (\d+)\]\s*/i.exec(text);
      if (shotMatch) text = text.slice(shotMatch[0].length);
      const stamp = /^At (\d{2}):(\d{2})\.(\d{3}),\s*/i.exec(text);
      const frame = stamp ? stampToFrame(stamp[1], stamp[2], stamp[3], fps) : null;
      if (stamp) text = text.slice(stamp[0].length);

      if (hasShots && !shotMatch && frame === null && !beats.length) {
        style = style ? `${style} ${text}` : text;
        continue;
      }
      beats.push({ text: unresolve(text), shot: Boolean(shotMatch), frame });
    }

    if (beats.length) {
      if (beats[0].frame !== null) {
        notes.push(`Shot 1 carried ${beats[0].frame} frames of offset — cleared, it opens the video.`);
        beats[0].frame = null;
      }
      beats[0].shot = true;
    }
    return { style: unresolve(style), beats };
  }

  /** @returns {{ok: boolean, notes: string[], data?: object}} */
  function parse(text, { fps = 24, maxSubjects = 5, taskTypes = [], markers = [] } = {}) {
    const notes = [];
    const { sections, order } = splitSections(text || '');

    if (!order.some((k) => KNOWN.includes(k))) {
      return { ok: false, notes: ['Nothing recognisable — expected a prompt starting with subject_definitions:.'] };
    }

    order
      .filter((k) => !KNOWN.includes(k))
      .forEach((k) => notes.push(`${k}: kept out — the builder does not model this section yet.`));

    const parsed = parseSubjects(sections.subject_definitions || '', notes);
    let descriptions = parsed.descriptions;
    if (descriptions.length > maxSubjects) {
      notes.push(`${descriptions.length} subjects found — only the first ${maxSubjects} were kept.`);
      descriptions = descriptions.slice(0, maxSubjects);
    }
    if (!descriptions.length) descriptions = [''];

    const summary = parseSummary(sections.summary || '', taskTypes, notes);
    const retention = parseRetention(sections.retention_analysis || '', markers, notes);
    const { style, beats } = parseBeats(sections.detailed_description || '', fps, notes);
    const soundscape = unresolve((sections.overall_soundscape || '').trim());
    const music = unresolve((sections.non_diegetic_music || '').trim()) || 'N/A';

    const assets = collectAssets(
      parsed.assets,
      [
        ...descriptions,
        summary.text,
        style,
        soundscape,
        music,
        ...beats.map((b) => b.text),
        ...Object.values(retention).map((r) => r.reason),
      ],
      notes
    );

    return {
      ok: true,
      notes,
      data: {
        // Aliases are not in the prompt; s1…sn keeps every mention resolvable.
        subjects: descriptions.map((description, i) => ({ name: `s${i + 1}`, description })),
        summary,
        retention,
        style,
        beats: beats.length ? beats : [{ text: '', shot: true, frame: null }],
        assets,
        soundscape,
        music,
      },
    };
  }

  return { parse, unresolve, splitSections };
})();
