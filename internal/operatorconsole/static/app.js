'use strict';
const $ = id => document.getElementById(id);
const endpoints = [
  ['health', 'Process health', '/healthz'],
  ['ready', 'Market readiness', '/readyz'],
  ['runtime', 'SHADOW runtime', '/api/v1/shadow/runtime'],
  ['sessions', 'Session scorecards', '/api/v1/shadow/scorecards'],
  ['zerodha', 'Zerodha integration', '/api/v1/integrations/zerodha/status'],
  ['telegram', 'Telegram delivery', '/api/v1/notifications/health']
];
let mock = false, busy = false, timer, snapshot = null, exportURL;
function node(tag, text, className) {
  const n = document.createElement(tag);
  if (text !== undefined) n.textContent = String(text);
  if (className) n.className = className;
  return n;
}
function text(value) { return typeof value === 'string' && value.length ? value : 'Unknown'; }
function count(value) { return Number.isSafeInteger(value) && value >= 0 ? String(value) : '—'; }
function setStatus(id, label, good) { $(id).textContent = label; $(id).className = good ? 'ok' : 'warn'; }
async function request(path) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetch(path, { signal: controller.signal, cache: 'no-store', credentials: 'same-origin' });
    const body = await response.json();
    if (body === null || typeof body !== 'object') throw new Error('Invalid response');
    return { ok: response.ok, code: response.status, body };
  } catch (_) { return { ok: false, code: 0, body: null }; }
  finally { clearTimeout(timeout); }
}
function card(status) {
  const box = node('article', undefined, 'underlying');
  const head = node('div', undefined, 'underlying-head');
  head.append(node('h3', text(status.underlying)), node('span', text(status.strategy), 'pill'));
  const warm = node('div', undefined, 'warmup');
  warm.append(node('span', 'Completed candle warmup'), node('span', `${count(status.warmup_samples)} / ${count(status.warmup_required)}`));
  const progress = node('progress');
  progress.max = Number.isSafeInteger(status.warmup_required) && status.warmup_required > 0 ? status.warmup_required : 50;
  progress.value = Number.isSafeInteger(status.warmup_samples) && status.warmup_samples >= 0 ? Math.min(status.warmup_samples, progress.max) : 0;
  progress.setAttribute('aria-label', `${text(status.underlying)} completed candle warmup`);
  const dl = node('dl');
  for (const [key, label] of [['market_data','Market data'],['future','Forward reference'],['option_universe','Option universe'],['selected_option','Selected option'],['last_risk','Last risk decision'],['last_risk_reason','Risk reason']]) {
    dl.append(node('dt',label), node('dd',text(status[key])));
  }
  box.append(head,warm,progress,dl); return box;
}
function render(results) {
  const health = results.health, ready = results.ready, runtime = results.runtime;
  const data = runtime.ok && runtime.body && !Array.isArray(runtime.body) ? runtime.body : {};
  setStatus('health', health.ok && health.body?.status === 'ok' ? 'Healthy' : 'Unavailable', health.ok && health.body?.status === 'ok');
  setStatus('ready', ready.ok && ready.body?.status === 'ready' ? 'Ready' : ready.body ? 'Not ready' : 'Unknown', ready.ok && ready.body?.status === 'ready');
  $('ready-detail').textContent = text(ready.body?.market_data_state);
  $('mode').textContent = text(data.Mode);
  $('qualification').textContent = text(data.Qualification);
  setStatus('orders', text(data.BrokerOrders), data.Mode === 'SHADOW' && data.BrokerOrders === 'DISABLED');
  const statuses = Array.isArray(data.Status) ? data.Status.filter(s => s && typeof s === 'object') : [];
  for (const id of ['underlyings','strategy-details']) {
    $(id).replaceChildren(...(statuses.length ? statuses.map(card) : [node('div', 'No underlying status available. Do not infer readiness.', 'empty')]));
  }
  const expanded = new Set(Array.from($('services').querySelectorAll('details[open]')).map(n => n.dataset.service));
  $('services').replaceChildren();
  for (const [key,label,path] of endpoints) {
    const result = results[key]; const row = node('div',undefined,'service');
    const name = node('div',label); name.append(node('small', `  ${path}`));
    row.append(name,node('span',result.ok ? 'RESPONSE OK' : result.code ? `HTTP ${result.code}` : 'UNAVAILABLE',result.ok ? 'ok' : 'bad'));
    const details = node('details'); details.dataset.service=key; details.open=expanded.has(key);
    details.append(node('summary','Inspect response'),node('pre',result.body ? JSON.stringify(result.body,null,2) : 'No valid response. Data is unavailable.'));
    row.append(details); $('services').append(row);
  }
  const sessions = results.sessions.ok && Array.isArray(results.sessions.body) ? results.sessions.body.filter(s => s && typeof s === 'object') : [];
  $('session-table').replaceChildren();
  if (!sessions.length) $('session-table').append(node('div','No session scorecards available.','empty'));
  else {
    const table=node('table'), head=node('thead'), hr=node('tr'), body=node('tbody');
    for (const label of ['UNDERLYING','DATE','QUALITY','SIGNALS','ACCEPTED','RISK REJECTED','DATA GAPS','NET P&L','REASONS']) hr.append(node('th',label));
    head.append(hr);
    for (const s of sessions) {
      const row=node('tr');
      for (const value of [text(s.underlying),text(s.trading_date),text(s.quality),count(s.signals),count(s.accepted_signals),count(s.risk_rejected_signals),count(s.data_gaps),text(s.net_pnl),Array.isArray(s.reasons) ? s.reasons.filter(v => typeof v === 'string').join(', ') || 'None reported' : 'Unknown']) row.append(node('td',value));
      body.append(row);
    }
    table.append(head,body); $('session-table').append(table);
  }
  const failed=endpoints.filter(([key]) => !results[key].ok).length;
  $('connection').textContent = failed ? `${failed} endpoint(s) unavailable or not ready. Unknown or degraded state must be investigated; no mock fallback is used.` : 'All endpoint responses received. Process health and warmup do not grant trading authority.';
  const at=new Date().toISOString();
  $('updated').textContent=`Last refresh attempt ${at} · every 5s`;
  snapshot={schema_version:'operator-console-diagnostic/v1', mock, real_market_evidence:false, captured_at:at, responses:results};
  $('export').disabled=false;
}
async function refresh() {
  if (busy) return;
  clearTimeout(timer); busy=true; $('refresh').disabled=true; $('scenario').disabled=true; $('export').disabled=true;
  $('connection').textContent='Refreshing endpoints… Displayed values belong to the previous refresh until this completes.';
  try {
    const suffix=mock ? `?scenario=${encodeURIComponent($('scenario').value)}` : '';
    const values=await Promise.all(endpoints.map(async ([key,,path]) => [key,await request(path+suffix)]));
    render(Object.fromEntries(values));
  } finally { busy=false; $('refresh').disabled=false; $('scenario').disabled=false; timer=setTimeout(refresh,5000); }
}
function navigate() {
  const names={overview:'Session overview',strategies:'Strategy readiness',sessions:'Session evidence',preparation:'Daily preparation'};
  const key=Object.hasOwn(names,location.hash.slice(1)) ? location.hash.slice(1) : 'overview';
  for (const name of Object.keys(names)) $(name).hidden=name!==key;
  for (const a of document.querySelectorAll('nav a')) { if(a.hash===`#${key}`) a.setAttribute('aria-current','page'); else a.removeAttribute('aria-current'); }
  $('page-title').textContent=names[key];
}
$('refresh').addEventListener('click',refresh);
$('scenario').addEventListener('change',refresh);
$('export').addEventListener('click',() => {
  if (!snapshot) return;
  if (exportURL) URL.revokeObjectURL(exportURL);
  const raw=JSON.stringify(snapshot,null,2);
  exportURL=URL.createObjectURL(new Blob([raw],{type:'application/json'}));
  let preview=$('export-preview');
  if (!preview) { preview=node('div'); preview.id='export-preview'; $('session-table').after(preview); }
  const a=node('a','Download JSON'); a.href=exportURL; a.download=mock ? 'mock-console-diagnostic.json' : 'console-diagnostic.json';
  const copy=node('textarea'); copy.readOnly=true; copy.value=raw; copy.rows=10; copy.setAttribute('aria-label','Prepared diagnostic JSON');
  preview.replaceChildren(node('h3','Diagnostic snapshot prepared'),node('p','This frozen snapshot is not real-market evidence. Download it, or select and copy the JSON below if your browser blocks downloads.'),a,copy);
});
window.addEventListener('hashchange',navigate); navigate();
(async () => {
  const result=await request('/console/config.json');
  if (!result.ok || typeof result.body?.mock !== 'boolean') {
    $('connection').textContent='Console source could not be verified. Reload before using this view.';
    $('refresh').disabled=true; return;
  }
  mock=result.body.mock;
  $('source').textContent=mock ? 'MOCK / OFFLINE' : 'RUNTIME / READ ONLY';
  $('mock-banner').hidden=!mock; $('mock-controls').hidden=!mock;
  await refresh();
})();
