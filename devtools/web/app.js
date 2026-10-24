(() => {
  'use strict';
  const byId = id => document.getElementById(id);
  const token = location.hash.slice(1);
  let data = { stores: [], events: [] }, storeId = null, sequence = null, loading = false, editorKey = '', editorDirty = false;
  const format = value => JSON.stringify(value, null, 2);
  const message = text => { byId('message').textContent = text; byId('message').hidden = !text; };

  async function api(path, body) {
    const response = await fetch(path, {
      method: body === undefined ? 'GET' : 'POST',
      headers: { Authorization: `Bearer ${token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      ...(body === undefined ? {} : { body: JSON.stringify(body) })
    });
    if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
    return response.status === 204 ? null : response.json();
  }

  function differences(before, after, path = '$', output = []) {
    if (output.length >= 500 || JSON.stringify(before) === JSON.stringify(after)) return output;
    if (before && after && typeof before === 'object' && typeof after === 'object' && Array.isArray(before) === Array.isArray(after)) {
      for (const key of new Set([...Object.keys(before), ...Object.keys(after)])) {
        differences(before[key], after[key], `${path}[${JSON.stringify(key)}]`, output);
      }
    } else {
      output.push(`${path}\n− ${JSON.stringify(before) ?? '(absent)'}\n+ ${JSON.stringify(after) ?? '(absent)'}`);
    }
    return output;
  }

  function render() {
    if (!data.stores.some(store => store.id === storeId)) storeId = data.stores[0]?.id ?? null;
    if (!data.events.some(event => event.sequence === sequence)) sequence = null;
    const store = data.stores.find(store => store.id === storeId);
    const selected = data.events.find(event => event.sequence === sequence && event.storeId === storeId);
    byId('store-count').textContent = data.stores.length;
    byId('stores').replaceChildren(...data.stores.map(item => {
      const button = document.createElement('button');
      button.className = 'store'; button.setAttribute('aria-pressed', String(item.id === storeId));
      const title = document.createElement('span'); title.textContent = item.id;
      const meta = document.createElement('small'); meta.textContent = `v${item.version} · ${item.restorable ? 'restore enabled' : 'read-only'}`;
      button.append(title, meta);
      button.onclick = () => { storeId = item.id; sequence = null; render(); };
      return button;
    }));
    const filter = byId('filter').value.toLowerCase();
    const events = data.events.filter(event => event.storeId === storeId && `${event.kind} ${event.mutation ?? ''} ${event.name ?? ''} ${event.error ?? ''}`.toLowerCase().includes(filter));
    byId('event-count').textContent = events.length;
    const rows = events.toReversed().map(event => {
      const button = document.createElement('button');
      button.className = `event${event.error ? ' error' : ''}`;
      button.setAttribute('aria-pressed', String(event.sequence === sequence));
      button.dataset.sequence = event.sequence;
      const badge = document.createElement('span'); badge.className = 'badge'; badge.textContent = event.kind;
      const title = document.createElement('strong'); title.textContent = event.name || event.mutation || 'Attached state';
      const meta = document.createElement('small');
      meta.textContent = `#${event.sequence} · v${event.version} · ${new Date(event.at).toLocaleTimeString()}${event.duration ? ` · ${(event.duration / 1e6).toFixed(2)} ms` : ''}${event.error ? ' · error' : ''}`;
      button.append(badge, title, meta);
      button.onclick = () => { sequence = event.sequence; render(); };
      return button;
    });
    byId('events').replaceChildren(...rows);
    if (!rows.length) {
      const empty = document.createElement('p'); empty.className = 'empty'; empty.textContent = 'No events match this store and filter.'; byId('events').append(empty);
    }
    byId('detail-title').textContent = store?.id ?? 'Select a store';
    byId('detail-meta').textContent = store ? `${selected ? selected.kind === 'action' ? `Action #${selected.sequence} · state pane is live v${store.version}` : `Snapshot #${selected.sequence} · v${selected.version}` : `Live state · v${store.version}`} · ${store.restorable ? 'Restore enabled' : 'Read-only'}` : 'Attach stores in your application to inspect them.';
    const state = selected?.state ?? store?.state;
    byId('state').textContent = state === undefined ? (store?.error || 'State is unavailable.') : format(state);
    byId('event').textContent = selected ? format(selected) : 'Select an event to inspect its metadata, timing and errors.';
    byId('changes').textContent = selected?.previous !== undefined && selected?.state !== undefined
      ? differences(selected.previous, selected.state).join('\n\n') || 'No state changes.' : 'Select a mutation to compare before and after.';
    byId('restore').disabled = !store?.restorable || selected?.state === undefined || selected?.generation !== store?.generation;
    byId('apply').disabled = !store?.restorable;
    byId('mode').textContent = store?.restorable ? 'Typed JSON validation runs before Set.' : 'Restoration is disabled for this store.';
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
      const next = await api('/api/snapshot');
      const changed = format(next.stores) !== format(data.stores) || format(next.events) !== format(data.events);
      data = next;
      byId('connection').textContent = 'Connected · local session'; message('');
      if (changed) render();
    } catch (error) {
      byId('connection').textContent = 'Disconnected'; message(error.message);
    } finally { loading = false; }
  }

  async function restore(body) {
    if (!confirm(`Replace ${storeId} state? Its subscriptions will run; external action effects are not reversed.`)) return;
    try { await api('/api/restore', body); sequence = null; editorDirty = false; await refresh(); }
    catch (error) { message(error.message); }
  }

  byId('refresh').onclick = refresh;
  byId('json-input').oninput = () => { editorDirty = true; };
  byId('filter').oninput = render;
  byId('live').onclick = () => { sequence = null; render(); };
  byId('restore').onclick = () => restore({ storeId, sequence });
  byId('apply').onclick = () => {
    try { return restore({ storeId, state: JSON.parse(byId('json-input').value) }); }
    catch (error) { message(error.message); }
  };
  byId('clear').onclick = async () => {
    try { await api('/api/clear', {}); sequence = null; await refresh(); }
    catch (error) { message(error.message); }
  };
  byId('export').onclick = () => {
    const url = URL.createObjectURL(new Blob([format(data)], { type: 'application/json' }));
    const link = document.createElement('a'); link.href = url; link.download = 'nodra-snapshot.json'; link.click(); URL.revokeObjectURL(url);
  };
  for (const name of ['state', 'changes', 'event']) {
    byId(`tab-${name}`).onclick = () => {
      for (const tab of ['state', 'changes', 'event']) {
        byId(`tab-${tab}`).setAttribute('aria-selected', String(tab === name));
        byId(`panel-${tab}`).hidden = tab !== name;
      }
    };
  }
  refresh();
  setInterval(refresh, 1000);
})();
