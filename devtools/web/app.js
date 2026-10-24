(() => {
  'use strict';
  const byId = id => document.getElementById(id);
  const token = location.hash.slice(1);
  let data = { stores: [], events: [] }, storeId = null, sequence = null, loading = false, editorKey = '', editorDirty = false, kind = 'all', cursor = 0, oldest = 0, actionFilter = null;
  const format = value => JSON.stringify(value, null, 2);
  const message = text => { byId('message').textContent = text; byId('message').hidden = !text; };
  const element = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };
  function glyph(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.classList.add('icon'); svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS(svg.namespaceURI, 'use');
    use.setAttribute('href', `#i-${name}`); svg.append(use);
    return svg;
  }
  function empty(name, title, subtitle) {
    const node = element('div', 'empty');
    node.append(glyph(name), element('strong', '', title), element('p', '', subtitle));
    return node;
  }

  async function api(path, body) {
    const response = await fetch(path, {
      method: body === undefined ? 'GET' : 'POST',
      headers: { Authorization: `Bearer ${token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      ...(body === undefined ? {} : { body: JSON.stringify(body) })
    });
    if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
    return response.status === 204 ? null : response.json();
  }

  const codeLimits = { state: 1000, event: 1000 };
  let currentState, stateFallback = '', stateContext = '', stateFormat = 'json';
  const expandedPaths = new Set(['$']), childLimits = new Map();
  function differences(before, after) {
    const output = [], pending = [{ before, after, path: '$' }];
    let visited = 0;
    while (pending.length && output.length < 500 && visited++ < 20000) {
      const pair = pending.pop(), { before, after, path } = pair;
      if (before === after) continue;
      if (before && after && typeof before === 'object' && typeof after === 'object' && Array.isArray(before) === Array.isArray(after)) {
        const keys = [...new Set([...Object.keys(before), ...Object.keys(after)])];
        for (const key of keys.toReversed()) pending.push({
          before: Object.hasOwn(before, key) ? before[key] : undefined,
          after: Object.hasOwn(after, key) ? after[key] : undefined,
          path: `${path}[${JSON.stringify(key)}]`
        });
      } else output.push(pair);
    }
    output.truncated = pending.length > 0;
    return output;
  }

  function preview(value) {
    if (value && typeof value === 'object') return Array.isArray(value) ? `Array(${value.length})` : `{ ${Object.keys(value).length} properties }`;
    const raw = format(value);
    return raw.length > 300 ? `${raw.slice(0, 300)}…` : raw;
  }
  function treeNode(label, value, path, depth = 0) {
    const container = value !== null && typeof value === 'object';
    const row = element(container ? 'details' : 'div', `tree-node${container ? ' tree-branch' : ' tree-leaf'}`);
    const heading = element(container ? 'summary' : 'div', 'tree-heading');
    const key = element('span', 'json-key', label), summary = element('span', `tree-preview ${container ? '' : `json-${typeof value}`}`, preview(value));
    heading.append(key, element('span', 'tree-colon', ':'), summary); row.append(heading);
    row.dataset.path = path;
    if (!container) return row;
    const children = element('div', 'tree-children'); row.append(children);
    const populate = () => {
      const keys = Object.keys(value), limit = childLimits.get(path) || 100;
      children.replaceChildren();
      if (depth >= 40) { children.append(element('p', 'tree-note', 'Further nesting is available in JSON or search.')); return; }
      for (const key of keys.slice(0, limit)) children.append(treeNode(Array.isArray(value) ? key : JSON.stringify(key), value[key], `${path}[${JSON.stringify(key)}]`, depth + 1));
      if (keys.length > limit) {
        const more = element('button', 'tree-more', `Show next ${Math.min(100, keys.length - limit)} · ${keys.length - limit} remaining`);
        more.onclick = () => { childLimits.set(path, limit + 100); populate(); };
        children.append(more);
      }
    };
    row.open = expandedPaths.has(path);
    if (row.open) populate();
    row.ontoggle = () => {
      if (!row.isConnected) return;
      if (row.open) { expandedPaths.add(path); if (!children.childNodes.length) populate(); }
      else expandedPaths.delete(path);
    };
    return row;
  }
  function renderState() {
    const query = byId('state-search').value.trim().toLowerCase();
    const tree = stateFormat === 'tree' || Boolean(query);
    byId('state').hidden = tree;
    byId('state-tree').hidden = !tree;
    byId('state-more').hidden = true;
    byId('collapse-state').hidden = !tree || Boolean(query);
    byId('state-search-status').hidden = !query;
    byId('view-tree').setAttribute('aria-pressed', String(tree));
    byId('view-json').setAttribute('aria-pressed', String(!tree));
    if (!tree) { renderCode(byId('state'), currentState, stateFallback); return; }
    const host = byId('state-tree');
    if (currentState === undefined) { host.textContent = stateFallback; return; }
    if (!query) { host.replaceChildren(treeNode('$', currentState, '$')); return; }
    const pending = [{ value: currentState, path: '$' }], matches = [];
    let visited = 0;
    while (pending.length && matches.length < 200 && visited++ < 50000) {
      const { value, path } = pending.pop();
      const container = value !== null && typeof value === 'object';
      if (path.toLowerCase().includes(query) || (!container && format(value).toLowerCase().includes(query))) matches.push({ path, value });
      if (container) for (const key of Object.keys(value).toReversed()) pending.push({ value: value[key], path: `${path}[${JSON.stringify(key)}]` });
    }
    host.replaceChildren(...matches.map(({ path, value }) => {
      const hit = element('div', 'search-hit');
      hit.append(element('span', 'search-path', path), element('code', '', preview(value))); return hit;
    }));
    if (!matches.length) host.append(empty('search', 'No state matches', 'Try a different key or value.'));
    byId('state-search-status').textContent = `${matches.length} matches${pending.length ? ' · Search limit reached; refine your query.' : ''}`;
  }

  function renderCode(node, value, fallback) {
    if (value === undefined) { node.textContent = fallback; const more = byId(`${node.id}-more`); if (more) more.hidden = true; return; }
    const lines = format(value).split('\n'), limit = codeLimits[node.id] || 1000;
    const more = byId(`${node.id}-more`);
    if (more) { more.hidden = lines.length <= limit; more.textContent = `Show next ${Math.min(1000, lines.length - limit)} lines · ${lines.length} total`; }
    node.replaceChildren(...lines.slice(0, limit).map((sourceLine, index) => {
      const line = sourceLine.length > 2000 ? `${sourceLine.slice(0, 2000)}… [line shortened; Copy JSON includes the full value]` : sourceLine;
      const row = element('span', 'code-line');
      const number = element('span', 'line-number', index + 1); number.setAttribute('aria-hidden', 'true');
      const text = element('span', 'code-text');
      const tokens = /"(?:\\.|[^"\\])*"|\b(?:true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g;
      let end = 0;
      for (const match of line.matchAll(tokens)) {
        text.append(document.createTextNode(line.slice(end, match.index)));
        const word = match[0];
        const type = word.startsWith('"') ? line.slice(match.index + word.length).trimStart().startsWith(':') ? 'key' : 'string'
          : word === 'null' ? 'null' : word === 'true' || word === 'false' ? 'boolean' : 'number';
        text.append(element('span', `json-${type}`, word));
        end = match.index + word.length;
      }
      text.append(document.createTextNode(line.slice(end)));
      row.append(number, text);
      return row;
    }));
  }

  function renderDiff(changes, selected) {
    const available = selected?.previous !== undefined && selected?.state !== undefined;
    byId('change-count').hidden = !available;
    byId('change-count').textContent = changes.length;
    byId('diff-summary').textContent = available ? `${changes.truncated ? 'Comparison limited · showing ' : ''}${changes.length} changed ${changes.length === 1 ? 'property' : 'properties'}${changes.truncated ? '. More changes may exist.' : ' in this snapshot.'}` : 'Compare a mutation’s previous and next state.';
    if (!available || changes.length === 0) {
      byId('changes').replaceChildren(empty('diff', available ? changes.truncated ? 'Comparison limit reached' : 'No state changes' : 'Select a mutation', available ? changes.truncated ? 'No differences found within the comparison limit. More changes may exist.' : 'The previous and next state are identical.' : 'Choose an event in the timeline to see what changed.'));
      return;
    }
    byId('changes').replaceChildren(...changes.map(change => {
      const block = element('section', 'diff-block');
      const heading = element('div', 'diff-path');
      heading.append(glyph('code'), element('span', '', change.path), element('span', 'diff-label', change.before === undefined ? 'Added' : change.after === undefined ? 'Removed' : 'Modified'));
      const columns = element('div', 'diff-columns');
      for (const [label, sign, value] of [['Before', '−', change.before], ['After', '+', change.after]]) {
        const side = element('div', 'diff-side');
        const code = element('pre', 'diff-value');
        code.append(element('span', 'diff-sign', sign), document.createTextNode(value === undefined ? '(absent)' : format(value)));
        side.append(element('span', '', label), code); columns.append(side);
      }
      block.append(heading, columns); return block;
    }));
  }

  function renderTasks(store) {
    const tasks = (data.tasks || []).filter(task => task.storeId === storeId);
    const active = tasks.filter(task => ['queued', 'running'].includes(task.phase)).length;
    byId('task-count').textContent = active;
    byId('task-summary').textContent = `${active} active · ${tasks.length} retained${store?.tasksDropped ? ` · ${store.tasksDropped} summaries omitted` : ''}${store?.taskProgressCoalesced ? ` · ${store.taskProgressCoalesced} progress updates coalesced` : ''}${data.capture?.paused ? ' · capture paused' : ''}`;
    byId('task-clear-filter').hidden = actionFilter === null;
    const rows = tasks.map(task => {
      const button = element('button', 'task-card');
      button.dataset.actionId = task.actionId;
      button.setAttribute('aria-pressed', String(actionFilter?.id === task.actionId && actionFilter?.generation === task.generation));
      const heading = element('span', 'task-card-heading');
      heading.append(element('strong', '', task.name), element('span', `task-phase ${task.phase}`, task.phase));
      const progress = element('progress'); progress.max = 1; progress.value = task.progress || 0;
      progress.setAttribute('aria-label', `${task.name} progress`);
      const meta = element('span', 'task-meta', `Action #${task.actionId} · Run #${task.runId}${task.key ? ` · ${task.key}` : ''} · ${Math.round((task.progress || 0) * 100)}%${task.committed ? ` · committed v${task.version}` : ''}`);
      button.append(heading, progress, meta);
      if (task.metrics) {
        const m = task.metrics;
        const phases = [['Debounce', m.debounce], ['Queue', m.queue], ['Work', m.work], ['Commit', m.commit]];
        const timing = element('span', 'task-meta task-timing', phases.map(([label, value]) => `${label} ${((value || 0) / 1e6).toFixed(2)} ms`).join(' · '));
        button.append(timing, element('span', 'task-meta', `${m.queueDepth || 0} waiting · ${m.activeWorkers || 0} active workers`));
      }
      if (task.phase === 'unobserved') button.append(element('span', 'task-note', 'Task ended outside capture; outcome unavailable.'));
      else if (task.error) button.append(element('span', 'task-note', task.error));
      button.onclick = () => { actionFilter = { id: task.actionId, generation: task.generation }; sequence = null; render(); };
      return button;
    });
    byId('tasks').replaceChildren(...rows);
    if (!rows.length) byId('tasks').append(empty('bolt', 'No tasks captured', 'Async action progress and outcomes appear here.'));
  }

  function render() {
    if (!data.stores.some(store => store.id === storeId)) storeId = data.stores[0]?.id ?? null;
    if (!data.events.some(event => event.sequence === sequence)) sequence = null;
    const store = data.stores.find(store => store.id === storeId);
    const selected = data.events.find(event => event.sequence === sequence && event.storeId === storeId);
    byId('store-count').textContent = data.stores.length;
    byId('stores').replaceChildren(...data.stores.map(item => {
      const button = element('button', 'store'); button.setAttribute('aria-pressed', String(item.id === storeId));
      const label = element('span', 'store-label');
      label.append(element('span', 'store-name', item.id), element('small', '', item.closed ? 'Closed source' : item.restorable ? 'Editable state' : 'Read-only state'));
      button.append(glyph('cube'), label, element('span', 'store-version', `v${item.version}`));
      button.onclick = () => { storeId = item.id; sequence = null; actionFilter = null; render(); };
      return button;
    }));
    if (!data.stores.length) byId('stores').append(empty('cube', 'No stores attached', 'Attach a store to begin.'));
    const filter = byId('filter').value.toLowerCase();
    const allEvents = data.events.filter(event => event.storeId === storeId);
    const events = allEvents.filter(event => (actionFilter === null || (event.actionId === actionFilter.id && event.generation === actionFilter.generation)) && (kind === 'all' || (kind === 'error' ? Boolean(event.error) : kind === 'action' ? ['action', 'task'].includes(event.kind) : event.kind === kind)) && `${event.kind} ${event.mutation ?? ''} ${event.name ?? ''} ${event.error ?? ''}`.toLowerCase().includes(filter));
    byId('event-count').textContent = events.length;
    const rows = events.toReversed().map(event => {
      const button = element('button', `event ${event.kind}${event.error ? ' error' : ''}`);
      button.setAttribute('aria-pressed', String(event.sequence === sequence)); button.dataset.sequence = event.sequence;
      const icon = element('span', 'event-icon'); icon.append(glyph(event.error ? 'error' : ['action', 'task'].includes(event.kind) ? 'bolt' : event.kind === 'mutation' ? 'diff' : 'cube'));
      const body = element('span', 'event-body');
      const title = event.name || ({ direct: 'Set state', patch: 'Update state', reset: 'Reset state' }[event.mutation]) || (event.sequence === event.generation ? 'Initial snapshot' : 'Captured snapshot');
      const meta = element('small', '', `${event.phase || event.kind[0].toUpperCase() + event.kind.slice(1)}${event.actionId ? ` · action #${event.actionId}` : ''}${event.kind === 'task' ? event.committed ? ` · committed v${event.version}` : '' : ` · v${event.version ?? '—'}`}`);
      if (event.kind === 'action') meta.append(document.createTextNode(` · ${((event.duration || 0) / 1e6).toFixed(2)} ms`));
      if (event.error) meta.append(element('span', 'error-tag', 'Error'));
      body.append(element('strong', '', title), meta);
      const tail = element('span', 'event-tail');
      const time = element('time', '', new Date(event.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })); time.dateTime = event.at;
      tail.append(element('span', '', `#${event.sequence}`), time);
      button.append(icon, body, tail); button.onclick = () => { sequence = event.sequence; render(); };
      return button;
    });
    byId('events').replaceChildren(...rows);
    if (!rows.length) byId('events').append(empty('search', 'No events found', filter || kind !== 'all' ? 'Try a different filter.' : 'New state changes will appear here.'));
    renderTasks(store);
    const retention = data.retention;
    byId('retention-status').textContent = retention ? `${(retention.bytes / 1048576).toFixed(1)} / ${(retention.maxBytes / 1048576).toFixed(0)} MB${retention.dropped ? ` · ${retention.dropped} evicted` : ''}` : '';
    byId('session-events').textContent = data.events.length;
    byId('session-errors').textContent = data.events.filter(event => event.error).length;
    byId('timeline-status').textContent = `${allEvents.length} captured ${allEvents.length === 1 ? 'event' : 'events'}`;
    byId('footer-stats').textContent = `${data.stores.length} ${data.stores.length === 1 ? 'store' : 'stores'} · ${data.events.length} events`;
    byId('live').setAttribute('aria-pressed', String(!selected));
    byId('detail-title').textContent = store?.id ?? 'Select a store';
    byId('breadcrumb-store').textContent = store?.id ?? '—';
    byId('detail-meta').textContent = store ? selected ? ['action', 'task'].includes(selected.kind) ? `${selected.kind === 'task' ? 'Task' : 'Action'} #${selected.actionId || selected.sequence}${selected.phase ? ` · ${selected.phase}` : ''} · state pane is live v${store.version}` : `Snapshot #${selected.sequence} · Version ${selected.version} · ${new Date(selected.at).toLocaleTimeString()}` : `Live state · Version ${store.version}` : 'Attach a store to start inspecting its state.';
    byId('action-error').hidden = !selected?.error;
    byId('action-error-text').textContent = selected?.error ?? '';
    const state = selected?.state !== undefined ? selected.state : store?.state;
    const context = `${store?.id}:${store?.generation}:${sequence}`;
    if (context !== stateContext) {
      stateContext = context; expandedPaths.clear(); expandedPaths.add('$'); childLimits.clear();
      codeLimits.state = 1000; byId('state-search').value = '';
    }
    currentState = state; stateFallback = store?.error || 'No state available.';
    renderState();
    renderCode(byId('event'), selected, 'Select an event to inspect its metadata.');
    const changes = selected?.previous !== undefined && selected?.state !== undefined ? differences(selected.previous, selected.state) : [];
    renderDiff(changes, selected);
    byId('state-file').textContent = `${store?.id ?? 'state'}.json`;
    byId('payload-size').textContent = state === undefined ? 'JSON' : `${new TextEncoder().encode(format(state)).length} B`;
    const properties = state && typeof state === 'object' ? Object.keys(state).length : 0;
    byId('state-properties').textContent = `${properties} ${Array.isArray(state) ? 'items' : properties === 1 ? 'property' : 'properties'}`;
    byId('copy-state').disabled = state === undefined;
    byId('restore').disabled = !store?.restorable || selected?.state === undefined || selected?.generation !== store?.generation;
    byId('apply').disabled = !store?.restorable;
    byId('view-mode').textContent = selected?.state !== undefined ? `SNAPSHOT #${selected.sequence}` : 'LIVE STATE';
    byId('editor-mode').textContent = store?.restorable ? 'Editable' : 'Read-only';
    byId('mode').textContent = store?.restorable ? 'Replaces the current state after validation.' : 'State editing is disabled for this store.';
    const nextEditorKey = `${storeId}:${sequence}`;
    if (editorKey !== nextEditorKey || !editorDirty) {
      byId('json-input').value = state === undefined ? '' : format(state);
      editorKey = nextEditorKey; editorDirty = false;
    }
  }

  async function refresh() {
    if (loading) return;
    loading = true;
    try {
      if (!token) throw new Error('Open the full session URL printed by your application, including its fragment token.');
      const next = await api(`/api/changes?revision=${data.revision || 0}&cursor=${cursor}&limit=50`);

      const changed = next.revision !== data.revision || next.events.length > 0;
      if (next.reset) { data = { stores: [], events: [], tasks: [] }; actionFilter = null; }
      const stores = new Map(data.stores.filter(store => next.storeIds.includes(store.id)).map(store => [store.id, store]));
      for (const store of next.stores) stores.set(store.id, store);
      const events = new Map(data.events.filter(event => event.sequence >= next.oldest).map(event => [event.sequence, event]));
      for (const event of next.events) events.set(event.sequence, event);
      cursor = next.cursor; oldest = next.oldest;
      data = { tasks: next.tasksChanged ? next.tasks || [] : data.tasks || [], format: 1, stores: [...stores.values()].sort((a, b) => a.id.localeCompare(b.id)), events: [...events.values()].sort((a, b) => a.sequence - b.sequence), revision: next.revision, epoch: next.epoch, retention: next.retention, capture: next.capture };
      if (changed) render();
      byId('history-more').hidden = !data.events.length || data.events[0].sequence <= oldest;
      byId('capture-pause').textContent = next.capture.paused ? 'Resume capture' : 'Pause capture';
      byId('capture-pause').setAttribute('aria-pressed', String(next.capture.paused));
      const rate = byId('capture-rate'), value = String(next.capture.sampleIntervalMS);
      if (![...rate.options].some(option => option.value === value)) rate.add(new Option(`${value} ms`, value));
      rate.value = value;
      byId('capture-status').textContent = `${next.capture.paused ? 'Capture paused' : next.capture.sampleIntervalMS ? 'Sampled capture' : 'Recording events'}${next.capture.skipped ? ` · ${next.capture.skipped} skipped` : ''}`;
      if (next.more) setTimeout(refresh, 0);
      document.body.dataset.connection = 'connected';
      byId('connection').textContent = 'Connected'; message('');
    } catch (error) { document.body.dataset.connection = 'disconnected'; byId('connection').textContent = 'Disconnected'; message(error.message); }
    finally { loading = false; }
  }

  async function restore(body) {
    if (!confirm(`Replace ${storeId} state? Its subscriptions will run; external action effects are not reversed.`)) return;
    try { await api('/api/restore', body); sequence = null; editorDirty = false; await refresh(); }
    catch (error) { message(error.message); }
  }

  function setView(name) {
    byId('editor').hidden = name === 'tasks';
    for (const tab of ['state', 'changes', 'tasks', 'event']) {
      const selected = tab === name;
      byId(`tab-${tab}`).setAttribute('aria-selected', String(selected));
      byId(`tab-${tab}`).tabIndex = selected ? 0 : -1;
      byId(`panel-${tab}`).hidden = !selected;
    }
  }
  function setTheme(theme) {
    document.documentElement.dataset.theme = theme;
    const label = `Switch to ${theme === 'dark' ? 'light' : 'dark'} theme`;
    byId('theme').setAttribute('aria-label', label); byId('theme').title = label;
    byId('theme-icon').setAttribute('href', theme === 'dark' ? '#i-sun' : '#i-moon');
  }
  let initialTheme;
  try { initialTheme = localStorage.getItem('nodra.devtools.theme'); } catch { /* Storage is optional. */ }
  setTheme(initialTheme === 'dark' ? 'dark' : 'light');
  byId('theme').onclick = () => {
    const theme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    setTheme(theme);
    try { localStorage.setItem('nodra.devtools.theme', theme); } catch { /* Storage is optional. */ }
  };
  byId('refresh').onclick = refresh;
  byId('task-clear-filter').onclick = () => { actionFilter = null; render(); };
  byId('history-more').onclick = async () => {
    const button = byId('history-more'); button.disabled = true;
    try {
      const page = await api(`/api/events?before=${data.events[0]?.sequence || 0}&limit=50`);
      if (page.epoch !== data.epoch) { await refresh(); return; }
      const events = new Map([...page.events, ...data.events].filter(event => event.sequence >= oldest).map(event => [event.sequence, event]));
      data.events = [...events.values()].sort((a, b) => a.sequence - b.sequence);
      render(); button.hidden = !page.more;
    } catch (error) { message(error.message); }
    finally { button.disabled = false; }
  };
  async function capturePolicy(paused) {
    try {
      await api('/api/capture', { paused, sampleIntervalMS: Number(byId('capture-rate').value) });
      await refresh();
    } catch (error) { message(error.message); }
  }
  byId('capture-pause').onclick = () => capturePolicy(!data.capture?.paused);
  byId('capture-rate').onchange = () => capturePolicy(Boolean(data.capture?.paused));
  byId('state-search').oninput = renderState;
  byId('view-tree').onclick = () => { stateFormat = 'tree'; renderState(); };
  byId('view-json').onclick = () => { stateFormat = 'json'; byId('state-search').value = ''; renderState(); };
  byId('collapse-state').onclick = () => { expandedPaths.clear(); renderState(); };
  byId('state-more').onclick = () => { codeLimits.state += 1000; renderState(); };
  byId('event-more').onclick = () => {
    codeLimits.event += 1000;
    renderCode(byId('event'), data.events.find(event => event.sequence === sequence && event.storeId === storeId), 'Select an event.');
  };
  byId('json-input').oninput = () => { editorDirty = true; };
  byId('filter').oninput = render;
  for (const button of document.querySelectorAll('[data-kind]')) button.onclick = () => {
    kind = button.dataset.kind;
    for (const filter of document.querySelectorAll('[data-kind]')) filter.setAttribute('aria-pressed', String(filter === button));
    render();
  };
  byId('live').onclick = () => { sequence = null; render(); };
  byId('restore').onclick = () => restore({ storeId, sequence });
  byId('apply').onclick = () => {
    try { return restore({ storeId, state: JSON.parse(byId('json-input').value) }); }
    catch (error) { message(error.message); }
  };
  byId('copy-state').onclick = async () => {
    const store = data.stores.find(item => item.id === storeId);
    const selected = data.events.find(event => event.sequence === sequence && event.storeId === storeId);
    try {
      await navigator.clipboard.writeText(format(selected?.state !== undefined ? selected.state : store?.state));
      byId('copy-state').replaceChildren(glyph('check'));
      setTimeout(() => byId('copy-state').replaceChildren(glyph('copy')), 1500);
    } catch (error) { message(error.message); }
  };
  byId('clear').onclick = async () => {
    try { await api('/api/clear', {}); sequence = null; await refresh(); }
    catch (error) { message(error.message); }
  };
  byId('export').onclick = async () => {
    try {
      const snapshot = await api('/api/snapshot');
      const url = URL.createObjectURL(new Blob([format(snapshot)], { type: 'application/json' }));
      const link = document.createElement('a'); link.href = url; link.download = 'nodra-snapshot.json'; link.click(); URL.revokeObjectURL(url);
    } catch (error) { message(error.message); }
  };
  for (const name of ['state', 'changes', 'tasks', 'event']) {
    byId(`tab-${name}`).onclick = () => setView(name);
    byId(`tab-${name}`).onkeydown = event => {
      const tabs = ['state', 'changes', 'tasks', 'event'];
      const offset = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
      const next = event.key === 'Home' ? 'state' : event.key === 'End' ? 'event' : offset ? tabs[(tabs.indexOf(name) + offset + tabs.length) % tabs.length] : null;
      if (next) { event.preventDefault(); setView(next); byId(`tab-${next}`).focus(); }
    };
  }
  setView('state');
  render();
  refresh();
  setInterval(refresh, 1000);
})();
