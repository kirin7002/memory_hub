// 真正的聊天客户端：浏览器只持有设备凭证，模型 Key 留在 Go 服务。
const $ = id => document.getElementById(id);
const clientMode = document.body.dataset.clientMode === 'single';
// 专用入口固定角色，云端模拟页沿用 web 同步身份；执行编排仍在 Go 模型代理中。
const clientRole = document.body.dataset.clientRole || '';
const fixedDevice = ({phone: 'phone', car: 'car', cloud: 'web'})[clientRole];
const preferences = clientMode ? sessionStorage : localStorage;
const parameters = new URLSearchParams(location.search);
let binding = {};
try { binding = JSON.parse(sessionStorage.getItem('memoryhub.client.binding') || '{}'); } catch { /* 损坏配置使用默认值。 */ }
const initialDevice = clientMode ? fixedDevice || parameters.get('device') || binding.device || 'phone' : localStorage.getItem('memoryhub.device');
let devices = clientMode ? [ ['phone', 'car', 'web'].includes(initialDevice) ? initialDevice : 'phone' ] : ['phone', 'car'];
const labels = {phone: '手机端', car: '车端', web: '网页端'};
if (clientRole === 'cloud') labels.web = '云端 Agent';
const state = {
  account: clientMode ? parameters.get('account') || binding.account || 'demo-user' : 'demo-user',
  selected: clientMode ? '' : localStorage.getItem('memoryhub.selected') || '',
  device: clientMode ? devices[0] : initialDevice === 'car' ? 'car' : 'phone',
  clientId: `page_${crypto.randomUUID()}`, connected: true, connecting: false, epoch: 0, requests: 0,
  busy: false, loading: false, polling: false, ready: false, modelReady: false, modelName: '',
  generation: 0, pending: null, drafts: {phone: '', car: ''}, error: '',
};
function newReplica() { return {
  token: '', expires: 0, cursor: '', online: false,
  conversations: new Map(), contexts: new Map(), events: new Map(),
}; }
let replicas = Object.fromEntries(devices.map(device => [device, newReplica()]));
let network = new AbortController();
function selectedKey() { return clientMode ? `memoryhub.client.selected.${state.account}.${state.device}` : 'memoryhub.selected'; }
if (clientMode) state.selected = parameters.get('conversation') || preferences.getItem(selectedKey()) || '';
function persistSelected() {
  if (state.selected) preferences.setItem(selectedKey(), state.selected);
  else preferences.removeItem(selectedKey());
  if (clientMode) {
    const url = new URL(location.href);
    url.searchParams.set('account', state.account); url.searchParams.set('device', state.device);
    if (state.selected) url.searchParams.set('conversation', state.selected);
    else url.searchParams.delete('conversation');
    history.replaceState(null, '', url);
  }
}
function cacheKey() { return `memoryhub.client.cache.${state.account}.${state.device}`; }
function saveCache() {
  if (!clientMode) return;
  const r = replicas[state.device];
  try {
    sessionStorage.setItem(cacheKey(), JSON.stringify({version: 1, account: state.account, device: state.device,
      conversations: [...r.conversations.values()], contexts: [...r.contexts.values()], events: [...r.events.values()], cursor: r.cursor}));
  } catch { /* 存储配额不足时继续使用内存；云端已保存的数据不受影响。 */ }
}
function restoreCache() {
  if (!clientMode) return;
  try {
    const data = JSON.parse(sessionStorage.getItem(cacheKey()) || 'null');
    if (!data || data.version !== 1 || data.account !== state.account || data.device !== state.device) return;
    const r = replicas[state.device];
    r.conversations = new Map(data.conversations.map(c => [c.conversation_id, c]));
    r.contexts = new Map(data.contexts.map(c => [c.context_id, c]));
    r.events = new Map(data.events.map(e => [e.event_id, e]));
    r.cursor = data.cursor || '';
  } catch { /* 缓存损坏时从云端重新安装快照。 */ }
}
let toastTimer, messageSignature = '', listSignature = '', contextSignature = '';

function notify(message) {
  $('toast').textContent = message;
  $('toast').hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { $('toast').hidden = true; }, 5500);
}
function id(prefix) { return `${prefix}_${crypto.randomUUID()}`; }
function conversation(device = state.device) { return replicas[device].conversations.get(state.selected); }
function currentContext(device = state.device) {
  const conv = conversation(device);
  return conv && replicas[device].contexts.get(conv.context_id);
}
function eventsFor(device = state.device) {
  return [...replicas[device].events.values()]
    .filter(event => event.conversation_id === state.selected)
    .sort((a, b) => BigInt(a.conv_seq) < BigInt(b.conv_seq) ? -1 : BigInt(a.conv_seq) > BigInt(b.conv_seq) ? 1 : 0);
}
function friendlyError(code) {
  return ({
    unauthorized: '设备凭证已过期，请刷新页面',
    model_not_configured: '服务端尚未配置模型，请检查配置并重启服务',
    context_version_conflict: '另一端已更新任务，请刷新后继续',
    stale_run: '另一条输入更新了会话，本次回复未保存',
    run_busy: '任务正在处理中，请稍后继续',
    snapshot_too_large: '当前数据超过快照上限',
    context_budget_exceeded: '当前上下文超过模型输入预算',
    cross_origin_denied: '请从本机服务地址打开页面',
    request_in_progress: '此消息仍在处理中，请等待',
    conversation_unavailable: '会话不可用，请刷新列表',
    account_invalid: '账户 ID 需为 1–64 位字母、数字、下划线、点或短横线',
    client_invalid: '客户端标识无效',
    not_found: '账户或设备不可用，设备可能已被撤销',
  })[code] || code || '请求未完成';
}
async function credential(device) {
  const r = replicas[device];
  if (r.token && r.expires > Date.now() / 1000 + 60) return r.token;
  const {response, data} = await transport('/v1/demo/session', {
    method: 'POST', headers: {'Content-Type': 'application/json'},
    body: JSON.stringify(clientMode ? {device_id: device, user_id: state.account, client_id: state.clientId} : {device_id: device}),
  });
  if (!response.ok) throw new Error(friendlyError(data.error));
  r.token = data.token;
  r.expires = data.expires_at;
  state.modelReady = data.model_ready;
  state.modelName = data.model;
  $('model-name').textContent = data.model_ready ? data.model : '模型尚未配置';
  return r.token;
}
// 所有云请求都经过此入口。断开时取消已发请求，账户切换后拒绝旧响应写入新副本。
async function transport(path, options) {
  if (!state.connected) throw new Error('本页已断开连接');
  const epoch = state.epoch;
  state.requests++;
  const response = await fetch(path, {...options, signal: network.signal});
  let data;
  try { data = await response.json(); }
  catch { throw new Error('云服务响应异常，请检查服务连接'); }
  if (epoch !== state.epoch || !state.connected) throw new Error('客户端连接已切换');
  return {response, data};
}
async function api(device, path, method = 'GET', body, headers = {}) {
  const token = await credential(device);
  const {response, data} = await transport(path, {
    method, headers: {Authorization: `Bearer ${token}`, 'Content-Type': 'application/json', ...headers},
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    const error = new Error(data.message || friendlyError(data.error));
    error.code = data.error;
    error.status = response.status;
    throw error;
  }
  return data;
}

// POST snapshot → 安装副本 → POST ack；每个设备独立游标，禁止同端同步并发。
function syncSerial(device, fn) {
  const r = replicas[device];
  const epoch = state.epoch;
  const next = (r.syncTask || Promise.resolve()).catch(() => {}).then(() => {
    if (epoch !== state.epoch) throw new Error('客户端连接已切换');
    return fn();
  });
  r.syncTask = next;
  return next;
}
function putContext(device, value) {
  const prior = replicas[device].contexts.get(value.context_id);
  // 历史读取与增量同步可能交错；旧 GET 响应不能覆盖更高版本的状态。
  if (!prior || (BigInt(value.context_version) >= BigInt(prior.context_version)
      && BigInt(value.source_revision) >= BigInt(prior.source_revision))) {
    replicas[device].contexts.set(value.context_id, value);
  }
}
function snapshot(device) { return syncSerial(device, () => snapshotNow(device)); }
async function snapshotNow(device) {
  const data = await api(device, '/v1/sync/snapshot', 'POST');
  const r = replicas[device];
  r.conversations = new Map(data.conversations.map(item => [item.conversation_id, item]));
  r.contexts = new Map(data.contexts.map(item => [item.context_id, item]));
  r.events = new Map(data.events.map(item => [item.event_id, item]));
  r.cursor = data.cursor;
  await api(device, '/v1/sync/ack', 'POST', {cursor: r.cursor});
  r.online = true;
  saveCache();
}
function applyChange(device, change) {
  const r = replicas[device];
  for (const op of change.operations || []) {
    const value = op.value;
    if (op.op === 'append_event') r.events.set(value.event_id, value);
    if (op.op === 'upsert_conversation') r.conversations.set(value.conversation_id, {...r.conversations.get(value.conversation_id), ...value});
    if (op.op === 'upsert_context') putContext(device, value);
  }
}
function changes(device) { return syncSerial(device, () => changesNow(device)); }
async function changesNow(device) {
  const r = replicas[device];
  if (!r.cursor) { await snapshotNow(device); return; }
  try {
    let more;
    do {
      const data = await api(device, `/v1/sync/changes?cursor=${encodeURIComponent(r.cursor)}`);
      for (const change of data.changes) applyChange(device, change);
      r.cursor = data.next_cursor;
      more = data.has_more;
      await api(device, '/v1/sync/ack', 'POST', {cursor: r.cursor});
    } while (more);
    r.online = true;
    saveCache();
  } catch (error) {
    if (error.status === 410) {
      await snapshotNow(device);
      // 快照仅有尾部；游标失效后补齐当前会话，避免长历史突然被截断。
      await loadConversation(device, state.selected, state.generation);
      return;
    }
    throw error;
  }
}
async function poll() {
  if (state.polling || !state.connected || state.connecting) return;
  const epoch = state.epoch;
  state.polling = true;
  const wasReady = state.ready;
  try {
    await Promise.all(devices.map(async device => {
      try { await changes(device); }
      catch { if (epoch === state.epoch) replicas[device].online = false; }
    }));
    if (epoch !== state.epoch) return;
    if (devices.every(device => replicas[device].online)) state.ready = true;
    if (!wasReady && state.ready && state.selected && conversation()) await selectConversation(state.selected);
    render();
  } finally { if (epoch === state.epoch) state.polling = false; }
}
// GET Context + events 分页，补齐快照之外的历史；切换会话后丢弃旧请求结果。
async function loadConversation(device, selected, generation) {
  const conv = replicas[device].conversations.get(selected);
  if (!conv) return;
  const ctx = await api(device, `/v1/contexts/${encodeURIComponent(conv.context_id)}`);
  if (generation !== state.generation) return;
  putContext(device, ctx);
  let after = '0', more;
  do {
    const page = await api(device, `/v1/conversations/${encodeURIComponent(selected)}/events?after_conv_seq=${after}`);
    if (generation !== state.generation) return;
    for (const event of page.events) replicas[device].events.set(event.event_id, event);
    after = page.next_after_conv_seq;
    more = page.has_more;
  } while (more);
  saveCache();
}
async function selectConversation(selected) {
  if (state.busy || state.connecting) return;
  const epoch = state.epoch;
  if (selected !== state.selected) {
    state.drafts = {phone: '', car: ''};
    $('chat-input').value = '';
    resizeInput();
  }
  state.selected = selected;
  state.error = '';
  state.loading = state.connected;
  persistSelected();
  const generation = ++state.generation;
  messageSignature = '';
  render();
  if (!state.connected) { $('sidebar').classList.remove('open'); return; }
  try { await Promise.all(devices.map(device => loadConversation(device, selected, generation))); }
  catch (error) { if (generation === state.generation) state.error = `读取历史失败：${error.message}`; }
  finally {
    if (generation === state.generation) { state.loading = false; render(); }
  }
  if (epoch === state.epoch) $('sidebar').classList.remove('open');
}

// 安全展示常用 Markdown；只创建 DOM/textContent，不执行模型生成的 HTML。
function inline(parent, text) {
  const pattern = /(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^\s)]+\))/g;
  let offset = 0;
  for (const match of text.matchAll(pattern)) {
    parent.append(document.createTextNode(text.slice(offset, match.index)));
    const token = match[0];
    if (token.startsWith('`')) {
      const code = document.createElement('code'); code.textContent = token.slice(1, -1); parent.append(code);
    } else if (token.startsWith('**')) {
      const strong = document.createElement('strong'); strong.textContent = token.slice(2, -2); parent.append(strong);
    } else {
      const parts = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(token);
      let url;
      try { url = new URL(parts[2]); } catch { /* 非绝对 URL 以普通文本展示。 */ }
      if (url && ['https:', 'http:'].includes(url.protocol) && !url.username && !url.password) {
        const link = document.createElement('a'); link.href = url.href; link.textContent = parts[1];
        link.target = '_blank'; link.rel = 'noopener noreferrer'; parent.append(link);
      } else { parent.append(document.createTextNode(token)); }
    }
    offset = match.index + token.length;
  }
  parent.append(document.createTextNode(text.slice(offset)));
}
function markdown(target, text) {
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  let paragraph = [], list = null;
  function flush() {
    if (paragraph.length) { const p = document.createElement('p'); inline(p, paragraph.join('\n')); target.append(p); paragraph = []; }
  }
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (/^\s*```/.test(line)) {
      flush(); list = null;
      const codeLines = [];
      while (++i < lines.length && !/^\s*```/.test(lines[i])) codeLines.push(lines[i]);
      const pre = document.createElement('pre'), code = document.createElement('code');
      code.textContent = codeLines.join('\n'); pre.append(code); target.append(pre); continue;
    }
    const heading = /^(#{1,6})\s+(.+)$/.exec(line);
    const bullet = /^\s*(?:([-*])|\d+[.)])\s+(.+)$/.exec(line);
    if (heading) {
      flush(); list = null;
      const h = document.createElement(`h${Math.min(4, heading[1].length + 1)}`); inline(h, heading[2]); target.append(h);
    } else if (bullet) {
      flush(); const kind = bullet[1] ? 'UL' : 'OL';
      if (!list || list.tagName !== kind) { list = document.createElement(kind.toLowerCase()); target.append(list); }
      const li = document.createElement('li'); inline(li, bullet[2]); list.append(li);
    } else if (!line.trim()) { flush(); list = null; }
    else { list = null; paragraph.push(line); }
  }
  flush();
}
function messageElement(event) {
  const user = event.type === 'user_message';
  const wrap = document.createElement('article');
  wrap.className = 'message' + (user ? ' mine' : '');
  wrap.dataset.eventId = event.event_id;
  const meta = document.createElement('div'); meta.className = 'message-meta';
  const author = document.createElement('strong'); author.textContent = user ? '你' : event.type === 'assistant_message' ? '随行助手' : '工具报告';
  const source = document.createElement('span'); source.textContent = labels[event.device_id] || '云端';
  meta.append(author, source);
  if (event.accepted_at) {
    const time = document.createElement('time'); time.dateTime = event.accepted_at;
    time.textContent = new Date(event.accepted_at).toLocaleTimeString('zh-CN', {hour: '2-digit', minute: '2-digit'}); meta.append(time);
  }
  if (event.status === 'reported') {
    const badge = document.createElement('span'); badge.className = 'reported-label'; badge.textContent = '离线报告'; meta.append(badge);
  }
  if (event.status === 'sending') { const badge = document.createElement('span'); badge.textContent = '正在提交'; meta.append(badge); }
  const row = document.createElement('div'); row.className = 'message-row';
  if (!user) { const avatar = document.createElement('span'); avatar.className = 'avatar'; avatar.textContent = '✦'; row.append(avatar); }
  const bubble = document.createElement('div'); bubble.className = 'message-bubble';
  if (user) bubble.textContent = event.text;
  else markdown(bubble, event.text || '');
  row.append(bubble); wrap.append(meta, row);
  if (!user) {
    const actions = document.createElement('div'); actions.className = 'message-actions';
    const copy = document.createElement('button'); copy.className = 'copy-message'; copy.textContent = '复制回复';
    copy.addEventListener('click', async () => {
      try { await navigator.clipboard.writeText(event.text); notify('回复已复制'); }
      catch { notify('浏览器未允许复制，请选中文字复制'); }
    });
    actions.append(copy); wrap.append(actions);
  }
  return wrap;
}
function renderMessages() {
  const events = eventsFor();
  const signature = JSON.stringify([state.selected, state.device, state.busy, state.loading, state.pending, events.map(e => [e.event_id, e.status])]);
  if (signature === messageSignature) return;
  const target = $('messages');
  const atBottom = target.scrollHeight - target.scrollTop - target.clientHeight < 80;
  const scrollTop = target.scrollTop;
  const changedConversation = target.dataset.conversation !== state.selected;
  target.dataset.conversation = state.selected;
  messageSignature = signature;
  target.replaceChildren();
  if (!events.length && !state.busy) {
    const box = document.createElement('div'); box.className = 'empty-state';
    const symbol = document.createElement('div'); symbol.className = 'empty-emblem'; symbol.textContent = '✦';
    const title = document.createElement('h2'); title.textContent = state.loading ? '正在读取你的对话…' : '今天，想聊些什么？';
    const note = document.createElement('p'); note.textContent = state.selected ? '从这里继续，助手会读取这个会话的历史。' : clientMode ? '直接发送消息开始。其他页面连接同一账户后，可以接续对话。' : '直接发送消息开始。切换手机和车端，对话会随你接续。';
    box.append(symbol, title, note); target.append(box);
  }
  for (const event of events) {
    if (event.type === 'context_command') {
      const note = document.createElement('div'); note.className = 'system-message'; note.textContent = event.text === 'confirm_slot' ? '你已确认任务信息' : '任务状态已更新'; target.append(note);
    } else { target.append(messageElement(event)); }
  }
  const pending = state.pending;
  if (pending && !events.some(e => e.type === 'user_message' && e.device_id === pending.device && e.text === pending.text && BigInt(e.conv_seq) > BigInt(pending.after))) {
    target.append(messageElement({event_id: pending.id, type: 'user_message', text: pending.text, device_id: pending.device, status: 'sending'}));
  }
  if (state.busy) {
    const typing = document.createElement('div'); typing.className = 'typing';
    const dots = document.createElement('span'); dots.className = 'typing-dots';
    for (let i = 0; i < 3; i++) dots.append(document.createElement('i'));
    const text = document.createElement('span'); text.textContent = pending ? '助手正在回复…' : '正在更新上下文…';
    typing.append(dots, text); target.append(typing);
  }
  target.scrollTop = changedConversation || atBottom || state.busy ? target.scrollHeight : scrollTop;
}
function renderList() {
  const replica = replicas[state.device];
  const latest = new Map();
  for (const event of replica.events.values()) {
    const seq = BigInt(event.user_seq);
    if (seq > (latest.get(event.conversation_id) || 0n)) latest.set(event.conversation_id, seq);
  }
  // 会话 ID 是随机值，最近对话按已同步的用户提交水位排序。
  const convs = [...replica.conversations.values()].sort((a, b) => {
    const left = latest.get(a.conversation_id) || 0n, right = latest.get(b.conversation_id) || 0n;
    return left > right ? -1 : left < right ? 1 : 0;
  });
  const signature = JSON.stringify([state.account, state.selected, state.busy, state.connecting, state.ready, convs.map(c => [c.conversation_id, c.title])]);
  if (signature === listSignature) return;
  listSignature = signature;
  const list = $('conversation-list'); list.replaceChildren();
  for (const conv of convs) {
    const button = document.createElement('button'); button.className = 'conversation-button' + (conv.conversation_id === state.selected ? ' active' : ''); button.disabled = state.busy || state.connecting;
    const icon = document.createElement('span'); icon.textContent = '◌';
    const title = document.createElement('span'); title.textContent = conv.title || '未命名会话'; button.append(icon, title);
    button.addEventListener('click', () => selectConversation(conv.conversation_id)); list.append(button);
  }
  if (!convs.length) { const note = document.createElement('p'); note.className = 'list-empty'; note.textContent = state.ready ? '开始对话后，记录会自动保存在这里。' : '正在读取你的会话…'; list.append(note); }
}
function renderContext() {
  const conv = conversation(), ctx = currentContext();
  $('session-title').textContent = conv?.title || '新对话';
  $('session-kind').textContent = ctx?.state.task_type === 'trip_planning' ? '行程规划 · 接续同一个任务' : conv ? '日常对话 · 历史自动保存' : '和助手聊聊你的想法';
  $('detail-conversation').textContent = conv?.conversation_id || '—';
  $('detail-context').textContent = ctx?.context_id || '—';
  $('detail-version').textContent = ctx?.context_version || '—';
  $('history-count').textContent = `${eventsFor().filter(e => e.type !== 'context_command').length} 条消息`;
  const signature = JSON.stringify([ctx, state.busy, state.connected, state.connecting]);
  if (signature === contextSignature) return;
  contextSignature = signature;
  $('context-status').textContent = ctx ? ({active: '进行中', awaiting_confirmation: '待确认', cancelled: '已取消', completed: '已完成'})[ctx.state.task_status] || ctx.state.task_status : '尚未开始';
  const values = $('context-values'); values.replaceChildren();
  for (const [field, slot] of Object.entries(ctx?.state.slots || {})) {
    if (slot.status !== 'confirmed') continue;
    const item = document.createElement('div'); item.className = 'context-value'; item.textContent = ({destination: '目的地', departure_time: '出发时间'})[field] || field;
    const value = document.createElement('b'); value.textContent = slot.value; item.append(value); values.append(item);
  }
  if (!values.childElementCount) { const note = document.createElement('span'); note.className = 'muted'; note.textContent = ctx?.state.task_type === 'chat' ? '日常对话使用会话历史作为上下文。' : '确认后的任务信息会显示在这里。'; values.append(note); }
  const proposals = $('proposals'); proposals.replaceChildren();
  for (const proposal of ctx?.state.pending_proposals || []) {
    const item = document.createElement('div'); item.className = 'proposal';
    const label = document.createElement('span'); label.textContent = `待确认 · ${({destination: '目的地', departure_time: '出发时间'})[proposal.field] || proposal.field}：${proposal.value}`;
    const button = document.createElement('button'); button.textContent = '确认'; button.disabled = state.busy || !state.connected || state.connecting; button.addEventListener('click', () => confirmProposal(proposal.proposal_id));
    item.append(label, button); proposals.append(item);
  }
  proposals.hidden = !proposals.childElementCount;
}
function render() {
  renderList(); renderContext(); renderMessages();
  const online = replicas[state.device].online;
  const bothOnline = devices.every(device => replicas[device].online);
  $('global-dot').classList.toggle('ready', bothOnline);
  $('global-status').textContent = !state.connected ? '本页已断开连接' : state.connecting ? '正在连接账户…' : bothOnline ? 'MemoryHub 已连接' : '正在重新连接';
  $('sync-summary').textContent = !state.connected ? '显示本页缓存 · 草稿尚未发送' : !state.modelReady ? '服务端尚未配置模型，暂时无法对话' : bothOnline ? '对话自动保存 · 同一账户跨端共享历史和上下文' : '正在等待云端连接…';
  $('device-description').textContent = `从${labels[state.device]}发送`;
  $('turn-status').textContent = state.busy ? '正在处理' : state.loading ? '正在读取历史' : '';
  $('task-label').hidden = Boolean(state.selected);
  $('task-type').disabled = state.busy;
  for (const device of devices) {
    if ($(`device-${device}`)) {
      $(`device-${device}`).setAttribute('aria-pressed', String(device === state.device));
      $(`device-${device}`).disabled = state.busy;
    }
    if ($(`${device}-status`)) $(`${device}-status`).textContent = `${labels[device]} · ${replicas[device].online ? '已连接' : '等待连接'}`;
  }
  // 无需先创建会话：第一条消息在发送流程中自动创建。
  $('chat-input').disabled = state.busy || state.loading || (state.connected && (!state.ready || !state.modelReady || !online));
  $('chat-input').placeholder = !state.connected ? '连接已断开，可以先写下草稿…' : state.busy ? '助手正在回复…' : state.modelReady ? `从${labels[state.device]}发送消息…` : '请先在服务端配置模型';
  $('send').disabled = !state.connected || $('chat-input').disabled || !$('chat-input').value.trim();
  $('new-chat').disabled = state.busy || state.connecting;
  $('refresh').disabled = state.busy || state.connecting || !state.connected;
  $('chat-error').hidden = !state.error;
  $('error-text').textContent = state.error;
  if (clientMode) renderClient();
}
function renderClient() {
  document.title = `${labels[state.device]} · ${state.account} — MemoryHub`;
  $('model-name').textContent = !state.connected ? '离线缓存模式' : state.connecting ? '正在连接服务' : state.modelReady ? state.modelName : '模型尚未配置';
  $('active-account').textContent = state.account;
  $('active-device').textContent = labels[state.device];
  $('network-toggle').textContent = state.connected ? '断开连接' : '重新连接';
  $('network-toggle').classList.toggle('disconnected', !state.connected);
  $('network-toggle').disabled = false;
  $('connect-account').disabled = state.busy || state.connecting;
  $('account-input').disabled = state.busy || state.connecting;
  $('client-device').disabled = Boolean(fixedDevice) || state.busy || state.connecting;
  $('connect-account').textContent = state.connecting ? '正在连接…' : '连接 / 切换账户';
  $('offline-banner').hidden = state.connected;
  $('client-status').textContent = `${labels[state.device]} · ${replicas[state.device].online ? '已连接' : '未连接'}`;
  $('detail-account').textContent = `demo / ${state.account}`;
  $('detail-client').textContent = state.clientId;
  const cursor = replicas[state.device].cursor;
  $('detail-cursor').textContent = cursor ? `已保存（${cursor.length} 字符）` : '尚未获取';
  $('detail-requests').textContent = String(state.requests);
  for (const role of ['phone', 'car', 'cloud']) {
    const url = new URL(`/${role}`, location.origin);
    url.searchParams.set('account', state.account);
    if (state.selected) url.searchParams.set('conversation', state.selected);
    $(`open-${role}`).href = url.href;
  }
}
function resizeInput() {
  const input = $('chat-input'); input.style.height = 'auto'; input.style.height = Math.min(input.scrollHeight, 170) + 'px';
}
function switchDevice(device) {
  if (state.busy) return;
  state.drafts[state.device] = $('chat-input').value;
  state.device = device;
  localStorage.setItem('memoryhub.device', device);
  $('chat-input').value = state.drafts[device];
  resizeInput(); render();
  if (!$('chat-input').disabled) $('chat-input').focus();
}
async function send(event) {
  event.preventDefault();
  if ($('send').disabled || state.busy) return;
  const epoch = state.epoch;
  const text = $('chat-input').value.trim();
  if (new TextEncoder().encode(text).length > 32768) { state.error = '消息超过 32 KiB，请缩短后再发送。'; render(); return; }
  const device = state.device, requestId = id('turn');
  const events = eventsFor(device);
  state.pending = {id: requestId, device, text, after: events.at(-1)?.conv_seq || '0'};
  state.busy = true; state.error = '';
  $('chat-input').value = ''; state.drafts[device] = ''; resizeInput(); render();
  let saved = false;
  try {
    if (!state.selected) {
      // POST /v1/conversations：用第一条消息生成标题，得到实际 conversation/context ID。
      const title = Array.from(text.replace(/\s+/g, ' ')).slice(0, 26).join('');
      const receipt = await api(device, '/v1/conversations', 'POST', {
        title, task_type: $('task-type').value,
      }, {'Idempotency-Key': id('create')});
      // 创建回执有意省略标题，页面沿用自己提交的标题，随后由同步结果更新。
      const conv = {...receipt.value, title};
      state.selected = conv.conversation_id;
      persistSelected();
      replicas[device].conversations.set(conv.conversation_id, conv);
      ++state.generation;
      render();
    }
    // POST /v1/demo/chat：Go 服务读取云端上下文、调用真实模型、保存回复后返回。
    await api(device, '/v1/demo/chat', 'POST', {request_id: requestId, conversation_id: state.selected, text});
    saved = true;
    await Promise.all(devices.map(item => changes(item)));
  } catch (error) {
    if (epoch !== state.epoch) return;
    state.error = saved ? `回复已保存，但页面同步失败：${error.message}。请刷新会话。` : `本轮未完成：${error.message}。已提交的内容可能已保存，请查看历史后继续。`;
  } finally {
    if (epoch !== state.epoch) return;
    state.busy = false; state.pending = null;
    await poll(); render();
    if (!$('chat-input').disabled) $('chat-input').focus();
  }
}
async function confirmProposal(proposalId) {
  if (state.busy || !state.connected) return;
  const epoch = state.epoch;
  const ctx = currentContext(); if (!ctx) return;
  state.busy = true; state.error = ''; render();
  try {
    // POST commands：以当前设备身份和最新 Context 版本提交用户显式确认。
    await api(state.device, `/v1/contexts/${encodeURIComponent(ctx.context_id)}/commands`, 'POST', {
      command_id: id('confirm'), expected_context_version: ctx.context_version, command: 'confirm_slot', proposal_id: proposalId,
    });
    await Promise.all(devices.map(device => changes(device)));
  } catch (error) { if (epoch === state.epoch) state.error = error.message; }
  finally { if (epoch === state.epoch) { state.busy = false; await poll(); render(); } }
}
$('new-chat').addEventListener('click', () => {
  if (state.busy || state.connecting) return;
  ++state.generation; state.selected = ''; state.loading = false; state.error = '';
  state.drafts = {phone: '', car: '', web: ''}; $('chat-input').value = ''; persistSelected();
  $('sidebar').classList.remove('open'); render(); resizeInput(); $('chat-input').focus();
});
$('refresh').addEventListener('click', async () => {
  if (!state.connected || state.connecting) return;
  const epoch = state.epoch;
  try {
    await Promise.all(devices.map(device => snapshot(device)));
    if (state.selected) await selectConversation(state.selected);
    render(); notify('已从 MemoryHub 重新读取会话');
  } catch (error) { if (epoch === state.epoch) { state.error = error.message; render(); } }
});
$('chat-form').addEventListener('submit', send);
$('chat-input').addEventListener('input', () => {
  state.drafts[state.device] = $('chat-input').value;
  resizeInput(); $('send').disabled = !state.connected || $('chat-input').disabled || !$('chat-input').value.trim();
});
$('chat-input').addEventListener('keydown', event => {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) { event.preventDefault(); $('chat-form').requestSubmit(); }
});
for (const device of devices) $(`device-${device}`)?.addEventListener('click', () => switchDevice(device));
function showContext(show) { $('context-panel').hidden = !show; $('toggle-context').setAttribute('aria-expanded', String(show)); }
$('toggle-context').addEventListener('click', () => showContext($('context-panel').hidden));
$('close-context').addEventListener('click', () => showContext(false));
$('dismiss-error').addEventListener('click', () => { state.error = ''; render(); });
$('show-chats').addEventListener('click', () => $('sidebar').classList.toggle('open'));
$('close-chats').addEventListener('click', () => $('sidebar').classList.remove('open'));

function resetNetwork() {
  ++state.epoch; ++state.generation;
  network.abort(); network = new AbortController();
  state.busy = false; state.pending = null; state.loading = false; state.polling = false;
}
function disconnectClient() {
  const interrupted = state.busy;
  saveCache(); resetNetwork();
  state.connected = false; state.connecting = false;
  for (const r of Object.values(replicas)) r.online = false;
  sessionStorage.setItem('memoryhub.client.disconnected', 'true');
  if (interrupted) state.error = '正在执行的请求已中断，部分内容可能已保存。重连后先检查历史，不会自动重发。';
  render();
}
async function connectClient(initial = false, reconnect = false) {
  const account = reconnect ? state.account : $('account-input').value.trim();
  const device = fixedDevice || (reconnect ? state.device : $('client-device').value);
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$/.test(account) || !['phone', 'car', 'web'].includes(device)) {
    state.error = '请填写合法账户 ID，并选择设备。'; render(); return;
  }
  const changed = account !== state.account || device !== state.device;
  const priorError = reconnect ? state.error : '';
  resetNetwork();
  state.account = account; state.device = device; devices = [device];
  if (changed) {
    replicas = {[device]: newReplica()};
    state.selected = preferences.getItem(selectedKey()) || '';
    state.drafts = {phone: '', car: '', web: ''}; $('chat-input').value = '';
    state.modelReady = false; state.ready = false;
    messageSignature = ''; listSignature = ''; contextSignature = '';
    restoreCache();
  } else if (initial) { restoreCache(); }
  state.connected = true; state.connecting = true; state.loading = true; state.error = '';
  sessionStorage.setItem('memoryhub.client.binding', JSON.stringify({account, device}));
  sessionStorage.setItem('memoryhub.client.disconnected', 'false');
  $('account-input').value = account; $('client-device').value = device;
  persistSelected();
  const epoch = state.epoch;
  render();
  try {
    // 每页只同步自己连接的设备；重连沿用原游标，失效时安装新快照。
    await changes(device);
    if (state.selected && conversation()) await loadConversation(device, state.selected, state.generation);
    else if (state.selected) { state.selected = ''; persistSelected(); }
    state.ready = true; state.error = priorError;
  } catch (error) {
    if (epoch === state.epoch) {
      replicas[device].online = false;
      state.error = `连接失败：${error.message}`;
    }
  } finally {
    if (epoch === state.epoch) { state.connecting = false; state.loading = false; render(); }
  }
}
if (clientMode) {
  $('connection-form').addEventListener('submit', event => {
    event.preventDefault(); if (!state.busy && !state.connecting) connectClient();
  });
  $('network-toggle').addEventListener('click', () => {
    if (state.connected) disconnectClient(); else connectClient(false, true);
  });
}
async function init() {
  if (clientMode) {
    if (fixedDevice) {
      $('client-role-heading').textContent = labels[fixedDevice];
      if (clientRole === 'cloud') {
        $('client-device').querySelector('option[value="web"]').textContent = '云端 Agent（模拟）';
        $('role-description').textContent = '云端 Agent 模拟：发送消息后生成回复，尚未支持后台自动执行。账户和连接状态仅作用于本页。';
      }
    }
    $('account-input').value = state.account;
    $('client-device').value = state.device;
    const sameBinding = binding.account === state.account && binding.device === state.device;
    if (sameBinding && sessionStorage.getItem('memoryhub.client.disconnected') === 'true') { restoreCache(); disconnectClient(); }
    else await connectClient(true);
    setInterval(poll, 3000);
    return;
  }
  render();
  try {
    await Promise.all(devices.map(device => snapshot(device)));
    state.ready = true;
    if (state.selected && conversation()) await selectConversation(state.selected);
    else { state.selected = ''; localStorage.removeItem('memoryhub.selected'); }
    render();
  } catch (error) { state.error = `连接失败：${error.message}`; render(); }
  setInterval(poll, 3000);
}
init();
