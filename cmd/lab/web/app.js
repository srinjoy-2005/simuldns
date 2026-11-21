'use strict';

const byId = id => document.getElementById(id);
const svg = byId('graph');
const ns = 'http://www.w3.org/2000/svg';
const width = 150;
const height = 66;
const storage = 'dns-lab-draft-v1';
const initial = {
  entry: 'main',
  host: {x: 26, y: 162},
  nodes: [
    {id:'main', label:'Main resolver', port:5353, upstream:'second', cache:true, delayMs:1000, timeoutMs:15000, x:200, y:162, records:"{\n  \"example.test\": {\n    \"A\": \"127.0.0.1\",\n    \"AAAA\": \"::1\"\n  },\n  \"1.0.0.127.in-addr.arpa\": {\n    \"PTR\": \"example.test.\"\n  },\n  \"10.2.0.192.in-addr.arpa\": {\n    \"PTR\": \"api.example.test.\"\n  },\n  \"api.example.test\": {\n    \"A\": \"192.0.2.10\",\n    \"AAAA\": \"2001:db8::10\"\n  }\n}"},
    {id:'second', label:'Resolver 2', port:5354, upstream:'third', delayMs:1000, timeoutMs:11000, x:374, y:162, records:"{\n  \"second.test\": {\n    \"A\": \"192.0.2.20\",\n    \"AAAA\": \"2001:db8::20\"\n  },\n  \"20.2.0.192.in-addr.arpa\": {\n    \"PTR\": \"second.test.\"\n  }\n}"},
    {id:'third', label:'Resolver 3', port:5355, upstream:'public', delayMs:1000, timeoutMs:7000, x:548, y:162, records:"{\n  \"third.test\": {\n    \"A\": \"192.0.2.30\",\n    \"AAAA\": \"2001:db8::30\"\n  },\n  \"30.2.0.192.in-addr.arpa\": {\n    \"PTR\": \"third.test.\"\n  }\n}"},
    {id:'public', label:'External upstream', address:'1.1.1.1:53', x:722, y:162},
  ],
};
let draft = structuredClone(initial);
try {
  const saved = JSON.parse(localStorage.getItem(storage));
  if (saved?.host && Array.isArray(saved.nodes)) draft = saved;
} catch { /* An unavailable browser store is optional. */ }
let selected = 'host', shownSpan = '', shownTrace = '';
let running = false, connected = false, busy = false, querying = false, closed = false, dirty = false;
let labels = {}, events = [], drag = null;
const hostQuery = {name:'second.test', type:1, timeoutMs:20000};
const rcodes = {0:'NOERROR', 1:'FORMERR', 2:'SERVFAIL', 3:'NXDOMAIN', 4:'NOTIMP', 5:'REFUSED'};
const types = {1:'A', 5:'CNAME', 12:'PTR', 28:'AAAA'};

function el(tag, text, className) {
  const element = document.createElement(tag);
  if (text !== undefined) element.textContent = text;
  if (className) element.className = className;
  return element;
}
function svgEl(tag, attrs, text) {
  const element = document.createElementNS(ns, tag);
  for (const [key,value] of Object.entries(attrs)) element.setAttribute(key,value);
  if (text !== undefined) element.textContent = text;
  return element;
}
function save(changesConfig = true) {
  if (running && changesConfig) dirty = true;
  try { localStorage.setItem(storage, JSON.stringify(draft)); } catch { /* Optional. */ }
  controls();
}
function error(message = '') {
  byId('error').textContent = message;
  byId('error').hidden = !message;
}
function controls() {
  for (const id of ['run','remake','stop','quit']) byId(id).disabled = !connected || busy || closed;
  byId('run').disabled ||= running;
  byId('stop').disabled ||= !running;
  const button = byId('submit-query');
  if (button) button.disabled = !connected || !running || busy || querying || closed;
  byId('status').textContent = closed ? 'Controller stopped' : !connected ? 'Connecting…' :
    busy ? 'Applying topology…' : running ? (dirty ? 'Running · edits pending Remake' : 'Running') : 'Stopped';
}
function graph() {
  const host = {id:'host', label:'Host', upstream:draft.entry, ...draft.host};
  const nodes = [host,...draft.nodes];
  const byNode = new Map(nodes.map(n => [n.id,n]));
  const traced = shownSpan.split('|')[0];
  const lines = byId('edges');
  lines.replaceChildren();
  for (const n of nodes) {
    const next = byNode.get(n.upstream);
    if (next) drawLine(lines,n.x+width,n.y+height/2,next.x,next.y+height/2);
  }
  if (drag?.link) {
    const n = byNode.get(drag.id);
    drawLine(lines,n.x+width,n.y+height/2,drag.end.x,drag.end.y,true);
  }
  const container = byId('nodes');
  container.replaceChildren();
  for (const n of nodes) {
    const status = [...events].reverse().find(e => e.node === n.id && (e.kind === 'ready' || e.kind === 'exit'))?.kind;
    const endpoint = n.id === 'host' ? 'DNS client' : n.address || `127.0.0.1:${n.port}`;
    const sub = running && status === 'exit' ? 'Process exited' : endpoint;
    const g = svgEl('g', {transform:`translate(${n.x} ${n.y})`, 'data-node':n.id,
      tabindex:0, role:'button', 'aria-label':`${n.label}, ${sub}`,
      class:['node',n.id===selected?'is-selected':'',n.id===traced?'is-traced':'',n.id==='host'?'is-host':'',n.address!==undefined?'is-external':''].join(' ')});
    g.append(svgEl('rect',{width,height,rx:8}),svgEl('text',{x:12,y:26},short(n.label,19)),
      svgEl('text',{x:12,y:48,class:'node-address'},short(sub,19)));
    if (n.address === undefined) g.append(svgEl('circle',{cx:width,cy:height/2,r:7,'data-link':n.id}));
    if (running && status === 'ready') g.append(svgEl('circle',{cx:width-12,cy:12,r:4,class:'ready-dot'}));
    container.append(g);
  }
}
function short(text,max) { return text.length > max ? text.slice(0,max-1)+'…' : text; }
function drawLine(parent,x1,y1,x2,y2,preview=false) {
  const curve = Math.max(24,Math.abs(x2-x1)/2);
  parent.append(svgEl('path',{class:preview?'edge preview':'edge', d:`M${x1} ${y1} C${x1+curve} ${y1},${x2-curve} ${y2},${x2} ${y2}`}));
}
function point(e) {
  const p = svg.createSVGPoint();
  p.x = e.clientX; p.y = e.clientY;
  return p.matrixTransform(svg.getScreenCTM().inverse());
}
svg.addEventListener('pointerdown', e => {
  const target = e.target.closest('[data-node]');
  if (!target || e.button !== 0) return;
  e.preventDefault();
  const id = target.dataset.node;
  const n = id === 'host' ? draft.host : draft.nodes.find(n => n.id === id);
  const p = point(e);
  drag = {id, start:p, end:p, x:n.x, y:n.y, link:!!e.target.dataset.link, moved:false};
  svg.setPointerCapture(e.pointerId);
  selected = id;
  graph();
  properties();
});
svg.addEventListener('pointermove', e => {
  if (!drag) return;
  const p = point(e);
  drag.end = p;
  const dx = p.x-drag.start.x, dy = p.y-drag.start.y;
  if (Math.hypot(dx,dy) > 3) drag.moved = true;
  if (drag.moved && !drag.link) {
    const n = drag.id === 'host' ? draft.host : draft.nodes.find(n => n.id === drag.id);
    n.x = Math.max(8,Math.min(742,drag.x+dx));
    n.y = Math.max(8,Math.min(315,drag.y+dy));
  }
  graph();
});
svg.addEventListener('pointerup', e => {
  if (!drag) return;
  if (drag.link) {
    const target = document.elementFromPoint(e.clientX,e.clientY)?.closest('[data-node]')?.dataset.node;
    if (target && target !== 'host' && target !== drag.id &&
        (drag.id !== 'host' || draft.nodes.find(n=>n.id===target).address === undefined)) {
      if (drag.id === 'host') draft.entry = target;
      else draft.nodes.find(n => n.id === drag.id).upstream = target;
      save();
    }
  } else if (drag.moved) save(false);
  drag = null;
  graph();
  properties();
});
svg.addEventListener('pointercancel', () => { drag = null; graph(); });
svg.addEventListener('keydown', e => {
  const node = e.target.closest('[data-node]');
  if (node && (e.key === 'Enter' || e.key === ' ')) {
    e.preventDefault();
    selected = node.dataset.node;
    graph();
    properties();
    byId('properties').querySelector('input,select,button')?.focus();
  }
});

function field(parent,label,control,callback) {
  const wrapper = el('label',undefined,control.type==='checkbox'?'field checkbox':'field');
  wrapper.append(el('span',label),control);
  control.addEventListener('input',() => callback(control.type==='checkbox' ? control.checked : control.value));
  parent.append(wrapper);
}
function input(value,type='text') {
  const control = el('input');
  control.type = type;
  if (type === 'checkbox') control.checked = !!value;
  else control.value = value ?? '';
  if (type === 'number') { control.min = '0'; control.step = '1'; }
  return control;
}
function select(options,value) {
  const control = el('select');
  for (const [id,label] of options) {
    const option = el('option',label);
    option.value = id;
    control.append(option);
  }
  control.value = value;
  return control;
}
function properties() {
  const panel = byId('properties');
  panel.replaceChildren();
  const options = draft.nodes.map(n => [n.id,n.label]);
  if (selected === 'host') {
    panel.append(el('h2','Host'));
    const form = el('form');
    field(form,'Resolver',select([['','Choose a resolver'],...options.filter(([id]) => draft.nodes.find(n=>n.id===id).address === undefined)],draft.entry),v=>{draft.entry=v;save();graph();});
    const name = input(hostQuery.name);
    name.required = true;
    field(form,'DNS name',name,v=>hostQuery.name=v);
    field(form,'Record type',select([[1,'A'],[28,'AAAA'],[12,'PTR']],hostQuery.type),v=>hostQuery.type=Number(v));
    field(form,'Host timeout (ms)',input(hostQuery.timeoutMs,'number'),v=>hostQuery.timeoutMs=Number(v));
    const button = el('button','Query','primary');
    button.id = 'submit-query';
    form.append(button);
    form.addEventListener('submit',query);
    panel.append(form);
    controls();
    return;
  }
  const node = draft.nodes.find(n => n.id === selected);
  if (!node) { selected = 'host'; properties(); return; }
  panel.append(el('h2',node.address !== undefined ? 'External upstream' : 'Resolver'));
  const update = key => value => {
    node[key] = ['port','delayMs','timeoutMs'].includes(key) ? Number(value) : value;
    save();
    graph();
  };
  field(panel,'Label',input(node.label),update('label'));
  if (node.address !== undefined) field(panel,'Address (IP:port)',input(node.address),update('address'));
  else {
    field(panel,'UDP port',input(node.port,'number'),update('port'));
    field(panel,'Upstream',select([['','None'],...options.filter(([id])=>id!==node.id)],node.upstream),update('upstream'));
    field(panel,'Forwarding delay (ms)',input(node.delayMs,'number'),update('delayMs'));
    field(panel,'Upstream timeout (ms)',input(node.timeoutMs,'number'),update('timeoutMs'));
    field(panel,'Enable cache',input(node.cache,'checkbox'),update('cache'));
    const records = el('textarea');
    records.spellcheck = false;
    records.value = node.records;
    field(panel,'Records (JSON)',records,update('records'));
  }
  const remove = el('button','Remove node','danger');
  remove.addEventListener('click',()=>{
    draft.nodes = draft.nodes.filter(n=>n.id!==node.id);
    if (draft.entry===node.id) draft.entry='';
    for (const n of draft.nodes) if (n.upstream===node.id) n.upstream='';
    selected='host'; save(); graph(); properties();
  });
  panel.append(remove);
}
function add(external) {
  if (draft.nodes.length >= 20) { error('The lab supports up to 20 nodes.'); return; }
  let port = 5353;
  while (draft.nodes.some(n=>n.port===port)) port++;
  const n = {id:crypto.randomUUID(),label:external?'External upstream':`Resolver ${port}`,
    x:35+(draft.nodes.length%4)*185,y:45+Math.floor(draft.nodes.length%8/4)*80};
  Object.assign(n,external?{address:'1.1.1.1:53'}:{port,upstream:'',delayMs:1000,timeoutMs:10000,records:'{}'});
  draft.nodes.push(n);
  selected=n.id; save(); graph(); properties();
}
byId('add-resolver').onclick=()=>add(false);
byId('add-external').onclick=()=>add(true);

async function post(action,data) {
  const response = await fetch('/api/'+action,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(data)});
  if (!response.ok) throw new Error((await response.text()).trim());
  if (response.status !== 204) return response.json();
}
function config() {
  return {entry:draft.entry,nodes:draft.nodes.map(({x,y,records,...n}) => {
    if (n.address === '') throw new Error(`${n.label}: enter an external address.`);
    if (records !== undefined) {
      try { n.records=JSON.parse(records); }
      catch { throw new Error(`${n.label}: invalid records JSON.`); }
    }
    return n;
  })};
}
for (const action of ['run','stop','remake','quit']) byId(action).onclick=async()=>{
  busy=true; error(); controls();
  try {
    const starting = action==='run' || action==='remake';
    const payload = starting ? config() : {};
    await post(action,payload);
    if (starting) {
      labels=Object.fromEntries(payload.nodes.map(n=>[n.id,n.label]));
      running=true; dirty=false;
    } else {
      running=false;
      if (action==='quit') { closed=true; source.close(); }
    }
  } catch (err) { error(err.message); }
  finally { busy=false; controls(); }
};
async function query(e) {
  e.preventDefault();
  if (!running || querying) return;
  querying=true; error(); controls();
  try {
    const result=await post('query',hostQuery);
    shownTrace=result.span;
    traces();
  } catch (err) { error(err.message); }
  finally { querying=false; controls(); }
}

const keyOf = wire => JSON.stringify([wire.source,wire.target,wire.id]);
const spanOf = (node,id) => node+'|'+id;
const typeName = type => types[type] ?? `TYPE${type}`;
const outcome = event => event?.rcode !== undefined ? rcodes[event.rcode] ?? `RCODE ${event.rcode}` :
  event?.status ? event.status : 'Pending';
const ms = value => `${Math.max(0,Math.round(value)).toLocaleString()} ms`;
function traces() {
  const spans = new Map(), sends = new Map(), roots = [];
  for (const event of events) {
    if (!event.span) continue;
    const id = spanOf(event.node,event.span);
    let span = spans.get(id);
    if (!span) { span={id,children:[],notes:[]}; spans.set(id,span); }
    if (event.kind==='host' || event.kind==='receive' || event.kind==='forward') {
      span.start=event;
      if (event.kind==='host') {
        roots.push(span);
        sends.set(keyOf(event.wire),span);
      }
    } else if (event.kind==='sent') {
      span.sent=event;
      sends.set(keyOf(event.wire),span);
    } else if (event.kind==='finish') span.finish=event;
    else span.notes.push(event);
  }
  for (const span of spans.values()) {
    const start = span.start;
    if (!start) continue;
    const parent = start.parent ? spans.get(spanOf(start.node,start.parent)) :
      start.kind==='receive' ? sends.get(keyOf(start.wire)) : undefined;
    if (parent && parent!==span) parent.children.push(span);
  }
  roots.sort((a,b)=>Date.parse(b.start.at)-Date.parse(a.start.at));
  const list = byId('trace-list'), body = byId('waterfall');
  list.replaceChildren(); body.replaceChildren();
  byId('trace-count').textContent = roots.length ? `${roots.length} lookups` : '';
  if (!roots.length) {
    list.append(el('p','Queries from Host appear here.','subtle'));
    return;
  }
  let root = roots.find(s=>s.start.span===shownTrace);
  if (!root) { root=roots[0]; shownTrace=root.start.span; }
  for (const item of roots) {
    const button=el('button');
    button.setAttribute('aria-pressed',item===root);
    button.append(el('strong',`${typeName(item.start.type)} ${item.start.name}`),
      el('span',outcome(item.finish)+(item.finish?' · '+ms(Date.parse(item.finish.at)-Date.parse(item.start.at)):''),'subtle'));
    button.onclick=()=>{shownTrace=item.start.span;shownSpan='';traces();graph();};
    list.append(button);
  }

  const flat=[], visited=new Set();
  function visit(span,depth) {
    if (visited.has(span.id)) return;
    visited.add(span.id);
    flat.push({span,depth});
    if (span.start.kind==='forward' && span.sent && Date.parse(span.sent.at)>Date.parse(span.start.at)) {
      flat.push({span:{id:span.id+'/delay',start:{...span.start,kind:'delay'},finish:span.sent,notes:[]},depth:depth+1});
    }
    for (const child of span.children) visit(child,depth+1);
  }
  visit(root,0);
  const origin=Date.parse(root.start.at);
  const end=Math.max(origin+1,...flat.map(({span})=>Date.parse(span.finish?.at ?? span.sent?.at ?? span.start.at)));
  const duration=end-origin;
  body.append(el('h3',root.start.name+' · '+outcome(root.finish),'trace-title'));
  const axis=el('div',undefined,'axis');
  const ticks=el('div',undefined,'ticks');
  for (const fraction of [0,.25,.5,.75,1]) ticks.append(el('span',ms(duration*fraction)));
  axis.append(el('span'),ticks);
  body.append(axis);
  for (const {span,depth} of flat) {
    const {start,finish,sent}=span;
    const begin=Date.parse(sent?.at ?? start.at);
    const finishAt=Date.parse(finish?.at ?? sent?.at ?? start.at);
    const title=start.kind==='delay'?'Forwarding delay':start.kind==='forward'?('Upstream '+start.detail):
      start.kind==='host'?'Host query':(labels[start.node] ?? draft.nodes.find(n=>n.id===start.node)?.label ?? start.node);
    const row=el('button',undefined,'span-row');
    row.setAttribute('aria-pressed',shownSpan===span.id);
    const label=el('span',title,'span-label');
    label.style.setProperty('--depth',Math.min(depth,5)*.65+'rem');
    const track=el('span',undefined,'track'), bar=el('span',undefined,'bar');
    if (start.kind==='delay') bar.classList.add('delay');
    else if (finish && (finish.status!=='reply' || finish.rcode===2)) bar.classList.add('failed');
    bar.style.left=Math.max(0,(begin-origin)/duration*100)+'%';
    bar.style.width=Math.max(0,(finishAt-begin)/duration*100)+'%';
    track.append(bar);
    row.append(label,track);
    const summary=start.kind==='delay'?ms(finishAt-begin):outcome(finish)+(finish?' · '+ms(finishAt-begin):'');
    row.setAttribute('aria-label',title+' · '+summary);
    label.append(el('small',' · '+summary,'subtle'));
    row.onclick=()=>{shownSpan=span.id;traces();graph();};
    body.append(row);
    if (shownSpan===span.id) {
      const notes=span.notes.map(e=>`${e.kind} ${e.status}`);
      const wire=start.wire ?? sent?.wire;
      if (wire) notes.push(`${wire.source} → ${wire.target} · DNS ID ${wire.id}`);
      if (finish?.detail) notes.push(finish.detail);
      for (const answer of finish?.answers ?? []) {
        notes.push(`${answer.name}  ${typeName(answer.type)}  ${answer.value}  TTL ${answer.ttl}s`);
      }
      if (notes.length) body.append(el('pre',notes.join('\n'),'detail'));
    }
  }
}

const source = new EventSource('/api/events');
source.onmessage = e=>{
  const data=JSON.parse(e.data);
  connected=true; running=data.running;
  if (data.reset) { events=[]; shownSpan=''; }
  const incoming = data.events ?? [];
  if (querying) {
    const latest = incoming.findLast(event => event.kind === 'host');
    if (latest) { shownTrace = latest.span; shownSpan = ''; }
  }
  events.push(...incoming);
  if (events.length>4096) events=events.slice(-4096);
  if (!drag) graph();
  traces(); controls();
};
source.onerror = ()=>{ connected=false; controls(); };
graph(); properties(); traces(); controls();
