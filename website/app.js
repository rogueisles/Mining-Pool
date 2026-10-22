/* Rogue Network Pool v1. Read-only views, observed data, no third-party dependencies. */
(() => {
  'use strict';
  const fmt = {
    escape: value => String(value ?? '').replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch])),
    number: (value, digits = 0) => value === null || value === undefined || !Number.isFinite(Number(value)) ? '—' : Number(value).toLocaleString('en-US', {maximumFractionDigits: digits}),
    units(value, decimals = 9, places = 3) {
      if (value === null || value === undefined || value === '') return '—';
      try {
        const n = BigInt(value), d = 10n ** BigInt(decimals), sign = n < 0n ? '−' : '', a = n < 0n ? -n : n;
        const whole = (a / d).toLocaleString('en-US');
        const fraction = (a % d).toString().padStart(decimals,'0').slice(0,places).replace(/0+$/,'');
        return sign + whole + (fraction ? '.' + fraction : '');
      } catch { return '—'; }
    },
    hashrate(value) {
      if (value === null || value === undefined || !Number.isFinite(Number(value)) || Number(value) < 0) return '—';
      let n = Number(value), i = 0; const u = ['H/s','KH/s','MH/s','GH/s','TH/s','PH/s'];
      while (n >= 1000 && i < u.length - 1) { n /= 1000; i++; }
      return fmt.number(n, n >= 100 ? 1 : 2) + ' ' + u[i];
    },
    short: (v, n = 6) => typeof v === 'string' && v.length > n * 2 + 3 ? v.slice(0,n) + '…' + v.slice(-n) : String(v ?? '—'),
    age(value) {
      if (!Number.isFinite(Number(value)) || !Number(value)) return '—';
      const seconds = Math.max(0, Date.now() / 1000 - Number(value));
      if (seconds < 5) return 'Just now';
      if (seconds < 60) return Math.floor(seconds) + 's ago';
      if (seconds < 3600) return Math.floor(seconds / 60) + 'm ago';
      if (seconds < 86400) return Math.floor(seconds / 3600) + 'h ago';
      return Math.floor(seconds / 86400) + 'd ago';
    },
    date(value) { if (!Number(value)) return '—'; return new Date(Number(value)*1000).toLocaleString('en-GB',{day:'2-digit',month:'short',hour:'2-digit',minute:'2-digit',second:'2-digit',timeZone:'UTC',hour12:false})+' UTC'; },
    validAddress: value => /^0x[0-9a-fA-F]{40}$/.test(value),
    validHash: value => /^0x[0-9a-fA-F]{64}$/.test(value),
    probability: (hashrate, difficulty, hours) => -Math.expm1(-hashrate*hours*3600/(difficulty*4294967296)),
    freshHealth: (data, now=Date.now()/1000) => Boolean(data && !data._gatewayStale && Number(data.updated)<=now && now-Number(data.updated)<40)
  };
  if (typeof module !== 'undefined') module.exports = fmt;
  if (typeof document === 'undefined') return;
  const $ = id => document.getElementById(id), page = document.body.dataset.page, E = fmt.escape;
  let cfg, store = {}, historyHours = 24, blockTab = new URLSearchParams(location.search).get('tab') || 'matured';
  let blockPage = 0, paymentPage = 0, busy = false, timer, toastTimer, oceanController;
  const put = (id, html) => { if ($(id)) $(id).innerHTML = html; };
  const text = (id, value) => { if ($(id)) $(id).textContent = value; };
  const metric = (key, value) => document.querySelectorAll('[data-metric="'+key+'"]').forEach(el => el.textContent = value);
  const pill = (label, state='good') => '<span class="pill '+(state === 'good' ? '' : state === 'bad' ? 'bad' : 'warn')+'">'+E(label)+'</span>';
  const missing = (columns, label='No records in this API window yet.') => '<tr><td class="empty" colspan="'+columns+'">'+E(label)+'</td></tr>';
  const addressLink = (address, full=false) => fmt.validAddress(address) ? '<a class="mono" href="/account/'+E(address)+'" title="'+E(address)+'">'+E(full ? address : fmt.short(address))+'</a>' : E(address || '—');
  const txLink = hash => fmt.validHash(hash) ? '<a class="mono" href="/tx/'+E(hash)+'" title="'+E(hash)+'">'+E(fmt.short(hash))+' ↗</a>' : '—';
  const blockLink = value => '<a class="mono" href="/block/'+E(value)+'">#'+E(fmt.number(value))+'</a>';
  const time = value => '<time datetime="'+(Number(value) ? new Date(Number(value)*1000).toISOString() : '')+'" title="'+E(fmt.date(value))+'">'+E(fmt.age(value))+'</time>';
  const dl = (id, rows) => put(id, rows.map(([k,v]) => '<div><dt>'+E(k)+'</dt><dd>'+v+'</dd></div>').join(''));
  const yn = value => value === true ? pill('Yes') : value === false ? pill('No','warn') : '—';
  function toast(message) { text('toast', message); $('toast').hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(() => $('toast').hidden = true, 3500); }
  function safeStorage(key, value) { try { if (value === undefined) return localStorage.getItem(key); localStorage.setItem(key,value); } catch {} }
  function createOceanAnimation() {
    const canvas=$('ocean-canvas');
    if(!canvas)return null;
    const gl=canvas.getContext('webgl',{alpha:false,antialias:false,depth:false,stencil:false,powerPreference:'low-power'});
    if(!gl){canvas.dataset.renderer='css-fallback';return null;}
    let frame=0,request=0,timeValue=0,lastDraw=0,enabled=!document.documentElement.classList.contains('motion-off'),ready=false;
    const reduced=matchMedia('(prefers-reduced-motion: reduce)');
    const vertex=`attribute vec2 position; varying vec2 uv; void main(){uv=position*0.5+0.5;gl_Position=vec4(position,0.0,1.0);}`;
    const precision=gl.getShaderPrecisionFormat(gl.FRAGMENT_SHADER,gl.HIGH_FLOAT).precision>0?'highp':'mediump';
    const fragment=`precision ${precision} float;
      varying vec2 uv;
      uniform sampler2D ocean;
      uniform vec2 viewport;
      uniform vec4 scene;
      uniform float clock;
      void main(){
        vec2 p=vec2(uv.x,1.0-uv.y)*viewport;
        float t=clock;
        float roll=sin(p.y*0.035+sin(p.x*0.008+t*0.3)*1.8-t*1.12);
        float cross=sin(p.x*0.019-p.y*0.014+t*0.72);
        vec2 displacement=vec2(roll*13.0+cross*5.0,sin(p.x*0.013+p.y*0.024-t*0.81)*10.0);
        vec2 waterUV=fract((p+displacement+vec2(sin(t*0.10)*18.0,cos(t*0.10)*15.0))/scene.w);
        vec3 water=texture2D(ocean,waterUV).rgb;
        float crest=pow(max(0.0,roll),22.0)*smoothstep(-0.3,0.7,sin(p.x*0.009+p.y*0.016+t*0.2));
        float glint=pow(max(0.0,cross),34.0)*0.025;
        water=mix(vec3(0.018,0.105,0.16),water,0.67);
        water+=vec3(0.09,0.36,0.40)*crest*0.26+vec3(glint);
        gl_FragColor=vec4(water,1.0);
      }`;
    const compile=(type,source)=>{const shader=gl.createShader(type);gl.shaderSource(shader,source);gl.compileShader(shader);if(!gl.getShaderParameter(shader,gl.COMPILE_STATUS))throw new Error('Water shader unavailable');return shader;};
    const fail=()=>{ready=false;cancelAnimationFrame(request);document.documentElement.classList.remove('ocean-rendered');canvas.dataset.renderer='css-fallback';};
    try{
      const program=gl.createProgram();gl.attachShader(program,compile(gl.VERTEX_SHADER,vertex));gl.attachShader(program,compile(gl.FRAGMENT_SHADER,fragment));gl.linkProgram(program);
      if(!gl.getProgramParameter(program,gl.LINK_STATUS))throw new Error('Water shader unavailable');
      gl.useProgram(program);
      const buffer=gl.createBuffer();gl.bindBuffer(gl.ARRAY_BUFFER,buffer);gl.bufferData(gl.ARRAY_BUFFER,new Float32Array([-1,-1,1,-1,-1,1,-1,1,1,-1,1,1]),gl.STATIC_DRAW);
      const position=gl.getAttribLocation(program,'position');gl.enableVertexAttribArray(position);gl.vertexAttribPointer(position,2,gl.FLOAT,false,0,0);
      const uniforms=Object.fromEntries(['viewport','scene','clock'].map(name=>[name,gl.getUniformLocation(program,name)]));
      const resize=()=>{
        const w=innerWidth,h=innerHeight,scale=Math.min(devicePixelRatio||1,1.15,Math.sqrt(1400000/(w*h)));
        canvas.width=Math.max(1,Math.round(w*scale));canvas.height=Math.max(1,Math.round(h*scale));gl.viewport(0,0,canvas.width,canvas.height);
        gl.uniform2f(uniforms.viewport,w,h);
        gl.uniform4f(uniforms.scene,0,0,0,w<=650?480:650);
        if(ready)draw();
      };
      const draw=()=>{gl.uniform1f(uniforms.clock,timeValue);gl.drawArrays(gl.TRIANGLES,0,6);};
      const loop=stamp=>{
        if(!ready||!enabled||document.hidden||reduced.matches){request=0;lastDraw=0;return;}
        request=requestAnimationFrame(loop);
        const interval=innerWidth<=650?1000/24:1000/30;
        if(lastDraw&&stamp-lastDraw<interval)return;
        timeValue=(timeValue+(lastDraw?Math.min((stamp-lastDraw)/1000,0.1):0))%(200*Math.PI);lastDraw=stamp;draw();
        frame++;if(frame%15===0)canvas.dataset.frame=String(frame);
      };
      const setMotion=on=>{
        enabled=on;cancelAnimationFrame(request);request=0;lastDraw=0;
        canvas.dataset.motion=enabled&&!document.hidden&&!reduced.matches?'running':'paused';
        if(ready&&enabled&&!document.hidden&&!reduced.matches)request=requestAnimationFrame(loop);
      };
      const image=src=>new Promise((resolve,reject)=>{const i=new Image();i.onload=()=>resolve(i);i.onerror=reject;i.src=src;});
      Promise.all([image('/assets/ocean.webp')]).then(images=>{
        images.forEach((i,index)=>{const texture=gl.createTexture();gl.activeTexture(gl.TEXTURE0+index);gl.bindTexture(gl.TEXTURE_2D,texture);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_WRAP_S,gl.CLAMP_TO_EDGE);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_WRAP_T,gl.CLAMP_TO_EDGE);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MIN_FILTER,gl.LINEAR);gl.texParameteri(gl.TEXTURE_2D,gl.TEXTURE_MAG_FILTER,gl.LINEAR);gl.texImage2D(gl.TEXTURE_2D,0,gl.RGB,gl.RGB,gl.UNSIGNED_BYTE,i);gl.uniform1i(gl.getUniformLocation(program,'ocean'),index);});
        ready=true;resize();draw();canvas.dataset.renderer='webgl';document.documentElement.classList.add('ocean-rendered');setMotion(enabled);
      }).catch(fail);
      addEventListener('resize',resize,{passive:true});canvas.addEventListener('webglcontextlost',fail);reduced.addEventListener('change',()=>setMotion(enabled));
      return {setMotion};
    }catch{fail();return null;}
  }
  // The ocean runs by default; a previous preview toggle cannot leave it paused.
  document.documentElement.classList.remove('motion-off');
  oceanController = createOceanAnimation();
  $('menu-toggle').addEventListener('click', () => { const open = document.body.classList.toggle('nav-open'); $('menu-toggle').setAttribute('aria-expanded',String(open)); if(open)$('navigation').querySelector('a').focus(); });
  document.addEventListener('keydown', e => {
    if(!document.body.classList.contains('nav-open'))return;
    if(e.key==='Escape'){document.body.classList.remove('nav-open');$('menu-toggle').setAttribute('aria-expanded','false');$('menu-toggle').focus();}
    if(e.key==='Tab'){
      const links=[...$('navigation').querySelectorAll('a,button')],first=links[0],last=links.at(-1);
      if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus();}
      else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus();}
    }
  });
  document.addEventListener('click', async e => {
    if (document.body.classList.contains('nav-open') && !e.target.closest('.sidebar, #menu-toggle')) { document.body.classList.remove('nav-open'); $('menu-toggle').setAttribute('aria-expanded','false'); }
    const copy = e.target.closest('[data-copy]');
    if (copy) await copyText(copy.dataset.copy);
  });
  async function copyText(value) {
    try { await navigator.clipboard.writeText(value); toast('Copied to clipboard'); }
    catch { const box = document.createElement('textarea'); box.value = value; box.setAttribute('aria-label','Text to copy'); document.body.append(box); box.select(); const ok = document.execCommand('copy'); box.remove(); toast(ok ? 'Copied to clipboard' : 'Select the address and copy it manually.'); }
  }
  const copyButton = (value,label='Copy') => '<button type="button" class="copy icon-button" data-copy="'+E(value)+'">'+E(label)+'</button>';
  document.querySelectorAll('form.lookup').forEach(form => {
    const input = form.querySelector('input'), remembered = safeStorage('rogue-pool-address');
    if (fmt.validAddress(remembered)) input.value = remembered;
    form.addEventListener('submit', event => { event.preventDefault(); const address = input.value.trim(); if (!fmt.validAddress(address)) { text(form.id+'-error','Enter a full RGN address: 0x followed by 40 hexadecimal characters.'); return; } safeStorage('rogue-pool-address',address); location.assign('/account/'+address); });
  });
  async function get(path) {
    const response = await fetch(path,{cache:'no-store',signal:AbortSignal.timeout(12000)});
    if (!response.ok) { const error = new Error(response.status === 404 ? 'No pool records for this address yet.' : 'Live data unavailable'); error.status = response.status; throw error; }
    const data = await response.json();
    if (data === null || typeof data !== 'object') throw new Error('Invalid pool response');
    return {data, stale:response.headers.get('X-Data-Stale') === 'true', at:Number(response.headers.get('X-Data-As-Of'))};
  }
  function dataStatus(failed, stale) {
    const health=store.health, stats=store.stats;
    const healthFresh=fmt.freshHealth(health);
    const statsFresh=!stats || Number(stats.now) > Date.now()-60000;
    const isDataPage = ['overview','miners','blocks','payments','account','status','admin','detail'].includes(page);
    const old = stale || !statsFresh || (health && !healthFresh);
    const label = failed ? 'Some live data unavailable · retrying' : old ? 'Cached data · waiting for a fresh update' : healthFresh && health.miningState === 'awaiting-fork-height' ? 'Pool armed · Scrypt mining opens at block 1,100,100' : healthFresh && health.readyForNiceHash === false ? 'Pool work paused · view status' : 'Updated '+new Date().toLocaleTimeString('en-GB',{hour12:false})+' · '+(isDataPage ? 'live pool data' : 'network settings');
    text('data-status-label',label); $('data-status').className='data-status '+(failed || old ? 'stale' : 'live');
  }
  function renderMetrics() {
    const s=store.stats, h=store.health;
    if (s) { metric('hashrate',fmt.hashrate(s.hashrate)); metric('miners',fmt.number(s.minersTotal)); metric('matured',fmt.number(s.maturedTotal)); metric('immature',fmt.number(s.immatureTotal)); metric('candidates',fmt.number(s.candidatesTotal)); metric('lastBlock',fmt.age(s.stats?.lastBlockFound)); }
    if (h) metric('height',fmt.number(h.ogg?.height));
    if (store.payments) { metric('payments',fmt.number(store.payments.paymentsTotal)); metric('latestPayout',fmt.age(store.payments.payments?.[0]?.timestamp)); }
    if (cfg) metric('depth',cfg.maturityDepth === null ? 'Not published' : fmt.number(cfg.maturityDepth)+' blocks');
  }
  function chainState(coin) {
    const h=store.health;
    if (!fmt.freshHealth(h)) return ['Unavailable','bad'];
    if (coin === 'ogg') return h.ogg?.syncing === false && h.peerCanonicalCheck?.healthy ? [h.peerCanonicalCheck.convergencePending ? 'Converging' : 'Synced',h.peerCanonicalCheck.convergencePending ? 'warn' : 'good'] : ['Needs attention','warn'];
    const node=h.external?.[coin];
    return node?.healthy === true && node.chain === 'main' && node.initialblockdownload === false && node.networkactive !== false && node.connections > 0 ? ['Synced','good'] : ['Needs attention','warn'];
  }
  function renderChains() {
    if (!store.health) return;
    const h=store.health, coins=[['ogg','RGN','Mainnet'],['digibyte','DigiByte','Mainnet'],['dogecoin','Dogecoin','Mainnet']];
    put('chain-list',coins.map(([key,name,network]) => { const [label,state]=chainState(key); const height=key==='ogg' ? h.ogg?.height : h.external?.[key]?.blocks; return '<div class="chain-row"><span class="coin '+(key==='digibyte'?'dgb':key==='dogecoin'?'doge':'')+'">'+(key==='ogg'?'<img src="/assets/rogue-mark.svg" width="18" height="19" alt="">':key==='digibyte'?'<img src="/assets/digibyte.svg" width="28" height="28" alt="DigiByte logo">':'<img src="/assets/dogecoin.png" width="28" height="28" alt="Dogecoin logo">')+'</span><div><strong>'+name+'</strong><small>'+network+'</small></div><div class="right">'+pill(label,state)+'<small>Block '+fmt.number(height)+'</small></div></div>'; }).join(''));
    put('status-chains',coins.map(([key,name,network])=> {const [label,state]=chainState(key), n=key==='ogg'?h.ogg:h.external?.[key];return '<article class="panel feature"><p class="eyebrow">'+network.toUpperCase()+'</p><h2>'+name+'</h2><p>'+pill(label,state)+'</p><dl class="detail-list"><div><dt>Height</dt><dd>'+fmt.number(n?.height ?? n?.blocks)+'</dd></div><div><dt>Peers</dt><dd>'+fmt.number(n?.peers ?? n?.connections)+'</dd></div><div><dt>Headers</dt><dd>'+fmt.number(n?.headers ?? n?.height)+'</dd></div><div><dt>Last check</dt><dd>'+E(fmt.date(h.updated))+'</dd></div></dl></article>';}).join(''));
    const summary=$('network-summary');
    if (summary) { const fresh=fmt.freshHealth(h); summary.className='notice '+(fresh&&h.readyForNiceHash?'good':'bad'); summary.textContent=fresh&&h.miningState==='awaiting-fork-height'?'Pool prepared for mainnet. Scrypt work opens at block 1,100,100; current blocks use the existing mining rules.':fresh&&h.readyForNiceHash?'RGN merged mining is accepting work. Fresh jobs and readiness checks are passing.':!fresh?'Network data is stale. Current mining readiness is unverified.':'Mining readiness needs attention. Review the checks below.'; }
    dl('readiness-details',[['Pool armed for fork',yn(h.miningArmed)],['Activation height',fmt.number(h.activationHeight)],['Automatic payments enabled',yn(h.payoutsEnabled)],['Payment state',E(h.payouts?.state || 'Unavailable')],['Payment interval',E(h.payouts?.interval || 'Unavailable')],['Accepting mining work',yn(h.readyForNiceHash)],['Fresh combined job',yn(h.freshCombinedJob)],['DigiByte work included',yn(Number.isInteger(h.activeExternalMask)?Boolean(h.activeExternalMask&1):null)],['Dogecoin work included',yn(Number.isInteger(h.activeExternalMask)?Boolean(h.activeExternalMask&2):null)],['Peer check healthy',yn(h.peerCanonicalCheck?.healthy)],['Same canonical head',yn(h.peerCanonicalCheck?.sameHead)],['Convergence pending',yn(h.peerCanonicalCheck?.convergencePending)],['Accounting admits work',yn(h.accountingAcceptsWork)],['Pool services admit work',yn(h.poolServicesAcceptsWork)],['Peer lag',fmt.number(h.peerCanonicalCheck?.lagBlocks)+' blocks'],['Peer state',E(h.peerCanonicalCheck?.state || 'Unavailable')]]);
    dl('identity-details',[['Network',E(cfg.network)],['Chain ID',E(h.chainId)],['Genesis', '<code>'+E(h.genesisHash)+'</code>'],['Current RGN head','<code>'+E(h.ogg?.hash || 'Unavailable')+'</code>'],['Source update',E(fmt.date(h.updated))]]);
  }
  function renderTreasury() {
    if (!cfg) return;
    put('treasury-cards',['digibyte','dogecoin'].map(coin=>{ const name=coin==='digibyte'?'DigiByte':'Dogecoin',addr=cfg.treasury[coin],url=coin==='digibyte'?'https://digiexplorer.info/address/':'https://blockchair.com/dogecoin/address/'; return '<article class="panel"><div class="panel-heading"><h2>'+name+' treasury</h2>'+pill('Mainnet','warn')+'</div><div class="treasury-address"><code>'+E(addr)+'</code>'+copyButton(addr)+'</div><div class="treasury-state"><span>Indexed balance</span><strong>Unavailable</strong></div><div class="treasury-state"><span>Confirmed pool wins</span><strong>Not indexed</strong></div><p class="table-note">A receiving destination, not proof of a reward. Verify transactions and maturity on the external chain.</p><a class="subtle-link" target="_blank" rel="noopener" href="'+url+encodeURIComponent(addr)+'">Inspect public address ↗</a></article>';}).join(''));
  }
  function blockState(b, category) { return b.orphan ? pill('Orphan','bad') : b.uncle ? pill('Uncle','warn') : pill(category==='matured'?'Matured':category==='immature'?'Immature':'Candidate',category==='matured'?'good':'warn'); }
  function allBlocks() {const b=store.blocks || {};return ['candidates','immature','matured'].flatMap(k=>(b[k] || []).map(v=>({...v,category:k}))).sort((a,b)=>b.timestamp-a.timestamp);}
  function renderRecent() {
    if (store.blocks) put('recent-blocks',allBlocks().slice(0,5).map(b=>'<tr><td>'+blockLink(b.height)+'</td><td>'+blockState(b,b.category)+'</td><td>'+time(b.timestamp)+'</td><td>'+fmt.units(b.reward,18,2)+(b.reward?' RGN':'')+'</td></tr>').join('') || missing(4));
    if (store.payments) put('recent-payments',(store.payments.payments || []).slice(0,5).map(p=>'<tr><td>'+addressLink(p.address)+'</td><td>'+fmt.units(p.amount,9,2)+' <small>RGN</small></td><td>'+time(p.timestamp)+'</td></tr>').join('') || missing(3));
  }
  function paginate(id,total,current,kind) {
    const pages=Math.max(1,Math.ceil(total/20));
    put(id,'<span>'+fmt.number(total)+' records in API window · '+(current+1)+' / '+pages+'</span><button data-page-kind="'+kind+'" data-direction="-1" '+(current===0?'disabled':'')+'>Previous</button><button data-page-kind="'+kind+'" data-direction="1" '+(current>=pages-1?'disabled':'')+'>Next</button>');
  }
  function blockRows() {
    if (!store.blocks || !$('block-content')) return;
    document.querySelectorAll('[data-tab]').forEach(b=>{b.classList.toggle('selected',b.dataset.tab===blockTab);b.setAttribute('aria-pressed',String(b.dataset.tab===blockTab));});
    if (blockTab==='luck') {
      put('block-content','<div class="notice">The inherited pool API returns legacy effort values whose difficulty units are not comparable to FORGE Scrypt shares. Displaying them as accurate luck percentages would be misleading. FORGE-normalized luck is not published yet.</div><p class="muted">Orphan and uncle rates below are the observed rates returned by the pool, not future probabilities.</p><div class="table-scroll"><table><thead><tr><th>Window</th><th>Orphan rate</th><th>Uncle rate</th><th>Normalized luck</th></tr></thead><tbody>'+Object.entries(store.blocks.luck || {}).map(([window,v])=>'<tr><td>Last '+fmt.number(window)+' blocks</td><td>'+fmt.number(v.orphanRate*100,2)+'%</td><td>'+fmt.number(v.uncleRate*100,2)+'%</td><td>Unavailable</td></tr>').join('')+'</tbody></table></div>');put('block-pagination','');return;
    }
    const data=blockTab==='orphans'?allBlocks().filter(b=>b.orphan): (store.blocks[blockTab] || []).map(v=>({...v,category:blockTab}));
    const list=data.slice(blockPage*20,(blockPage+1)*20);
    put('block-content','<div class="table-scroll"><table><thead><tr><th>Height</th><th>State</th><th>Found at</th><th>Shares</th><th>Reward RGN</th><th>Block hash</th></tr></thead><tbody>'+ (list.map(b=>'<tr><td>'+blockLink(b.height)+'</td><td>'+blockState(b,b.category)+'</td><td>'+E(fmt.date(b.timestamp))+'</td><td>'+fmt.number(b.shares)+'</td><td>'+fmt.units(b.reward,18,4)+'</td><td>'+(fmt.validHash(b.hash)?'<a class="mono" href="/block/'+E(b.hash)+'" title="'+E(b.hash)+'">'+E(fmt.short(b.hash))+' ↗</a>':'Awaiting validation')+'</td></tr>').join('')||missing(6,blockTab==='orphans'?'No orphan records in this API window.':'No '+blockTab+' blocks in this API window.'))+'</tbody></table></div>');
    paginate('block-pagination',data.length,blockPage,'blocks');
  }
  document.querySelectorAll('[data-tab]').forEach(b=>b.addEventListener('click',()=>{blockTab=b.dataset.tab;blockPage=0;history.replaceState(null,'','/blocks?tab='+blockTab);blockRows();}));
  document.addEventListener('click',e=>{const b=e.target.closest('[data-page-kind]');if(!b)return;if(b.dataset.pageKind==='blocks'){blockPage=Math.max(0,blockPage+Number(b.dataset.direction));blockRows();}else{paymentPage=Math.max(0,paymentPage+Number(b.dataset.direction));paymentRows();}});
  function paymentRows() {
    if (!store.payments || !$('payments-table')) return;
    const filter=$('payments-filter').value.toLowerCase(), list=(store.payments.payments || []).filter(p=>(p.address || '').toLowerCase().includes(filter));
    put('payments-table',list.slice(paymentPage*20,(paymentPage+1)*20).map(p=>'<tr><td>'+addressLink(p.address)+'</td><td>'+fmt.units(p.amount,9,6)+'</td><td>'+E(fmt.date(p.timestamp))+'</td><td>'+txLink(p.tx)+'</td></tr>').join('')||missing(4,'No matching payments in this API window.'));
    paginate('payments-pagination',list.length,paymentPage,'payments');
  }
  $('payments-filter')?.addEventListener('input',()=>{paymentPage=0;paymentRows();});
  function minerRows() {
    if (!store.miners || !$('miners-table')) return;
    const filter=$('miners-filter').value.toLowerCase(), sort=$('miners-sort').value;
    const list=Object.entries(store.miners.miners || {}).filter(([address])=>address.toLowerCase().includes(filter));
    list.sort((a,b)=>sort==='recent'?b[1].lastBeat-a[1].lastBeat:b[1].hr-a[1].hr);
    put('miners-table',list.map(([address,v],i)=>'<tr><td>'+String(i+1).padStart(2,'0')+'</td><td>'+addressLink(address)+'</td><td>'+fmt.hashrate(v.hr)+'</td><td>'+time(v.lastBeat)+'</td><td>'+pill(v.offline?'Offline':'Active',v.offline?'warn':'good')+'</td></tr>').join('')||missing(5,'No matching mining addresses.'));
    text('miners-count',fmt.number(list.length)+' matching addresses. Worker and ASIC counts are separate from miner addresses.');
  }
  ['miners-filter','miners-sort'].forEach(id=>$(id)?.addEventListener(id.endsWith('sort')?'change':'input',minerRows));
  const accountAddress = location.pathname.match(/^\/account\/(0x[0-9a-fA-F]{40})/i)?.[1];
  if (accountAddress) { text('account-address',accountAddress); $('copy-account').addEventListener('click',()=>copyText(accountAddress)); }
  function renderAccount() {
    const a=store.account; if (!a)return;
    put('account-empty','');
    const s=a.stats || {};
    metric('accountHashrate',fmt.hashrate(a.currentHashrate));metric('accountAverage',fmt.hashrate(a.hashrate));metric('workers',fmt.number(a.workersOnline)+' / '+fmt.number(a.workersTotal));metric('paid',fmt.units(s.paid)+' RGN');metric('balance',fmt.units(s.balance)+' RGN');metric('accountImmature',fmt.units(s.immature)+' RGN');
    const workers=Object.entries(a.workers || {}).sort((x,y)=>y[1].hr-x[1].hr);
    put('workers-table',workers.map(([name,w])=>'<tr><td>'+E(name)+'</td><td>'+pill(w.offline?'Offline':'Online',w.offline?'warn':'good')+'</td><td>'+fmt.hashrate(w.hr)+'</td><td>'+fmt.hashrate(w.hr2)+'</td><td>'+time(w.lastBeat)+'</td></tr>').join('')||missing(5,'No workers in the current pool window.'));
    const max=Math.max(1,...workers.map(([,w])=>Number(w.hr)));
    put('worker-bars',workers.map(([name,w])=>'<div><span>'+E(name)+'</span><meter min="0" max="'+max+'" value="'+Number(w.hr)+'" aria-label="'+E(name)+' current hashrate">'+fmt.hashrate(w.hr)+'</meter><strong>'+fmt.hashrate(w.hr)+'</strong></div>').join('')||'<p class="empty">Waiting for worker observations.</p>');
    put('account-payments',(a.payments || []).map(p=>'<tr><td>'+fmt.units(p.amount,9,6)+'</td><td>'+E(fmt.date(p.timestamp))+'</td><td>'+txLink(p.tx)+'</td></tr>').join('')||missing(3,'No payments recorded for this address yet.'));
    text('account-payment-count','Showing '+(a.payments || []).length+' recent payments of '+fmt.number(a.paymentsTotal)+' recorded.');
    dl('account-details',[['Mature unpaid balance',fmt.units(s.balance,9,6)+' RGN'],['Payment pending',fmt.units(s.pending,9,6)+' RGN'],['Immature rewards',fmt.units(s.immature,9,6)+' RGN'],['Paid total',fmt.units(s.paid,9,6)+' RGN'],['Blocks found',fmt.number(s.blocksFound)],['Current round shares',fmt.number(a.roundShares)],['Last accepted share',E(fmt.date(s.lastShare))]]);
  }
  function chart() {
    const target=$('hashrate-chart');if(!target || !store.history)return;
    const rows=store.history.points || [];
    if(rows.length<2){target.innerHTML='<p class="empty">Collecting observed history.<br>The chart appears after two real samples, about 30 seconds apart.</p>';return;}
    const values=rows.map(p=>p[1]),max=Math.max(1,...values)*1.17,minT=rows[0][0],span=Math.max(1,rows[rows.length-1][0]-minT);
    const x=t=>45+(t-minT)/span*577,y=v=>160-v/max*140;
    let path='';rows.forEach((p,i)=>{path+=(i===0 || p[0]-rows[i-1][0]>95?'M':'L')+x(p[0]).toFixed(2)+','+y(p[1]).toFixed(2)+' ';});
    const lines=[0,.33,.66,1].map(t=>'<line class="grid-line" x1="45" x2="622" y1="'+y(max*t)+'" y2="'+y(max*t)+'"/><text x="0" y="'+(y(max*t)+3)+'">'+E(fmt.hashrate(max*t).replace('/s',''))+'</text>').join('');
    const ticks=[rows[0],rows[Math.floor(rows.length/2)],rows[rows.length-1]].map((p,i)=>'<text x="'+x(p[0])+'" y="188" text-anchor="'+(i===0?'start':i===2?'end':'middle')+'">'+new Date(p[0]*1000).toLocaleTimeString('en-GB',{hour:'2-digit',minute:'2-digit',hour12:false})+'</text>').join('');
    target.innerHTML='<svg viewBox="0 0 640 200" role="img" aria-label="Observed pool hashrate from '+E(fmt.date(minT))+' to '+E(fmt.date(rows.at(-1)[0]))+'"><title>Accepted pool hashrate. Gaps indicate missing observations.</title><defs><linearGradient id="chart-fill" x1="0" y1="0" x2="0" y2="1"><stop stop-color="#8edacf" stop-opacity="0.22"/><stop offset="1" stop-color="#8edacf" stop-opacity="0"/></linearGradient></defs>'+lines+'<path d="'+path+'" fill="none" stroke="#8edacf" stroke-width="2" vector-effect="non-scaling-stroke"/><circle cx="'+x(rows.at(-1)[0])+'" cy="'+y(rows.at(-1)[1])+'" r="3" fill="#e9ce9b"/>'+ticks+'</svg>';
    target.setAttribute('aria-label','Accepted hashrate · latest '+fmt.hashrate(rows.at(-1)[1]));
    text('chart-summary',fmt.hashrate(rows.at(-1)[1])+' · observed hashrate');text('chart-window',rows.length+' real observations · local time');
  }
  document.querySelectorAll('[data-hours]').forEach(b=>b.addEventListener('click',async()=>{historyHours=Number(b.dataset.hours);document.querySelectorAll('[data-hours]').forEach(x=>x.classList.toggle('selected',x===b));try{store.history=(await get('/api/history?hours='+historyHours)).data;chart();}catch{toast('History is temporarily unavailable.');}}));
  $('calculator-form')?.addEventListener('submit',event=>{
    event.preventDefault();
    const hashrate=Number($('calc-rate').value)*Number($('calc-unit').value), hours=Number($('calc-hours').value), difficulty=Number($('calc-difficulty').value);
    if(!Number.isFinite(hashrate)||hashrate<=0||!Number.isFinite(hours)||hours<=0)return;
    const pool=store.stats?.hashrate;
    const rows=[['Your hashrate',fmt.hashrate(hashrate)],['Share of current pool hashrate',pool>0?fmt.number(hashrate/pool*100,2)+'% (comparison, not future allocation)':'Pool data unavailable'],['Duration',fmt.number(hours,2)+' hours']];
    if(Number.isFinite(difficulty)&&difficulty>0){rows.push(['Expected network blocks',fmt.number(hashrate*hours*3600/(difficulty*4294967296),6)],['Chance of at least one',fmt.number(fmt.probability(hashrate,difficulty,hours)*100,3)+'%'],['Chance of none',fmt.number((1-fmt.probability(hashrate,difficulty,hours))*100,3)+'%']);}
    else rows.push(['Block probability','Enter a fresh chain network difficulty to calculate.']);
    dl('calculator-results',rows);
  });
  function renderSettings() {
    if(!cfg)return;
    document.querySelectorAll('[data-config]').forEach(el=>el.textContent=el.dataset.config==='environmentLabel'?(cfg.networkLabel || ('MAINNET')):cfg[el.dataset.config] || '');
    dl('connection-details',[['Algorithm','Scrypt'],['Stratum URL','<code>'+E(cfg.stratum)+'</code>'+copyButton(cfg.stratum)],['Username','<code>YOUR_RGN_ADDRESS.worker</code>'],['Password','<code>x</code>'],['Pool fee',cfg.poolFee===null?'Not published':fmt.number(cfg.poolFee,2)+'%'],['Reward scheme',E(cfg.rewardScheme)],['Payout threshold',cfg.payoutThreshold===null?'Not published':fmt.number(cfg.payoutThreshold,6)+' '+E(cfg.symbol)],['Maturity depth',cfg.maturityDepth===null?'Not published':fmt.number(cfg.maturityDepth)+' blocks'],['Payout interval',cfg.payoutInterval===null?'Not published':E(cfg.payoutInterval)]]);
    dl('wallet-details',[['Network name',E(cfg.network)],['Chain ID',E(cfg.chainId)],['Currency symbol',E(cfg.symbol)],['Read-only RPC URL','<code>'+E(cfg.walletRpc)+'</code>'+copyButton(cfg.walletRpc)],['Chain records','<a href="/blocks">Pool block records ↗</a>']]);
    renderTreasury();
  }
  $('setup-form')?.addEventListener('submit',e=>{e.preventDefault();const address=$('setup-address').value.trim(),worker=$('setup-worker').value.trim();if(!fmt.validAddress(address)||!/^[a-zA-Z0-9_-]{1,32}$/.test(worker)){text('setup-error','Use a full RGN address and a worker name containing letters, numbers, _ or -.');return;}if(!cfg){text('setup-error','Network settings unavailable. Please refresh.');return;}text('setup-error','');safeStorage('rogue-pool-address',address);const endpoint=new URL(cfg.stratum);const rows=[['Pool URL',cfg.stratum],['Username',address+'.'+worker],['Password','x'],['NiceHash hostname',endpoint.hostname],['NiceHash port',endpoint.port]];put('config-output',rows.map(([label,value])=>'<div class="config-line"><div><small>'+label+'</small><code>'+E(value)+'</code></div>'+copyButton(value)+'</div>').join('')+'<a class="subtle-link" href="/account/'+address+'">Open your miner dashboard ↗</a>');$('config-output').hidden=false;});
  $('add-network')?.addEventListener('click',async()=>{if(!cfg)return;const wallet=window.ethereum;if(!wallet?.request){text('wallet-message','No compatible wallet detected. Copy the network settings above into your wallet manually.');return;}try{await wallet.request({method:'wallet_addEthereumChain',params:[{chainId:'0x'+Number(cfg.chainId).toString(16),chainName:cfg.network,nativeCurrency:{name:cfg.symbol,symbol:cfg.symbol,decimals:18},rpcUrls:[cfg.walletRpc]}]});text('wallet-message','Network request sent. Check your wallet to confirm the selected network.');}catch(error){text('wallet-message',error.code===4001?'Network request cancelled in your wallet.':'Wallet could not add this network. Use the manual settings above.');}});
  function renderAdmin() {
    const admin=store.admin;if(!admin)return;const h=admin.pool,a=h.accounting || {},w=admin.website;
    text('admin-summary','Work admission: '+(h.readyForNiceHash?'accepting':'paused')+' · source '+fmt.date(h.updated));
    dl('admin-accounting',[['Health',E(a.health || 'Unavailable')],['Accepts work',yn(h.accountingAcceptsWork)],['Pending records',fmt.number(a.pendingRecords)],['Uncredited records',fmt.number(a.uncreditedRecords)],['Undurable records',fmt.number(a.undurableRecords)],['Pending bytes',fmt.number(a.pendingBytes)],['Oldest pending age',fmt.number(a.oldestAgeSeconds)+' seconds'],['Persistence failures',fmt.number(a.persistenceFailures)],['Last accounting success',E(fmt.date(Number(a.lastAccountingSuccessMS)/1000))],['Last recovery',E(fmt.date(Number(a.lastRecoveryCompletedMS)/1000))],['Max pending records',fmt.number(a.limits?.maxPendingRecords)]]);
    dl('admin-services',[['Combined job fresh',yn(h.freshCombinedJob)],['Pool services admit work',yn(h.poolServicesAcceptsWork)],['Pool service failure',E(h.poolServiceFailure || 'None reported')],['Website read-only',yn(w.readOnly)],['Latest history sample',E(fmt.date(w.lastSample))],['History sampler',E(w.samplingError || 'No error reported')],['Gateway cache entries',fmt.number(w.cachedEntries)]]);
    const p=h.peerCanonicalCheck || {};dl('admin-peer',Object.entries(p).filter(([k])=>!['reason','error'].includes(k)).map(([k,v])=>[k,E(typeof v==='object'?JSON.stringify(v):v)]));
    if(store.blocks&&store.payments){const b=store.blocks,pay=store.payments.payments?.[0];dl('admin-payouts',[['Candidates',fmt.number(b.candidatesTotal)],['Immature',fmt.number(b.immatureTotal)],['Matured records',fmt.number(b.maturedTotal)],['Highest recent matured',fmt.number(Math.max(0,...(b.matured || []).map(x=>x.height)))],['Payment records',fmt.number(store.payments.paymentsTotal)],['Latest payment',pay?E(fmt.date(pay.timestamp)):'No record'],['Latest transaction',pay?txLink(pay.tx):'—']]);}
  }
  function renderDetail() {
    const d=store.detail;if(!d)return;const b=d.block,tx=d.transaction,r=d.receipt;
    const hexNumber=v=>v===null||v===undefined?'—':fmt.number(BigInt(v));
    let rows;
    if(b){rows=[['Height',hexNumber(b.number)],['Block hash','<code>'+E(b.hash)+'</code>'],['Parent','<a class="mono" href="/block/'+E(b.parentHash)+'">'+E(b.parentHash)+'</a>'],['Canonical',yn(d.canonical)],['Timestamp',E(fmt.date(parseInt(b.timestamp,16)))],['Transactions',fmt.number(b.transactions?.length)],['Gas used',hexNumber(b.gasUsed)],['Miner',E(b.miner)]];}
    else{rows=[['Transaction','<code>'+E(tx.hash)+'</code>'],['Receipt status',r?pill(r.status==='0x1'?'Successful':'Failed',r.status==='0x1'?'good':'bad'):pill('Pending','warn')],['Canonical',r?yn(d.canonical):'Not yet confirmed'],['Confirmations',fmt.number(d.confirmations)],['From',addressLink(tx.from,true)],['To',tx.to?addressLink(tx.to,true):'Contract creation'],['Value',fmt.units(tx.value?BigInt(tx.value).toString():null,18,9)+' RGN'],['Block',tx.blockNumber?blockLink(parseInt(tx.blockNumber,16)):'Pending'],['Gas used',hexNumber(r?.gasUsed)]];}
    put('chain-detail','<p class="record-title">'+(b?'RGN block':'RGN transaction')+' · '+E(cfg.network)+'</p><dl class="detail-list" id="record-details"></dl>');dl('record-details',rows);
    if(b?.transactions?.length) $('chain-detail').insertAdjacentHTML('beforeend','<h3>Transactions</h3><div class="stack">'+b.transactions.slice(0,100).map(txLink).join('')+'</div>');
  }
  async function refresh() {
    if(busy || document.hidden)return;busy=true;$('refresh-data').disabled=true;
    try {
      let failed=false,stale=false;
      if(!cfg){try{cfg=(await get('/api/config')).data;renderSettings();}catch{failed=true;}}
      const routes={overview:{stats:'/api/stats',health:'/api/health',blocks:'/api/blocks',payments:'/api/payments',history:'/api/history?hours='+historyHours},miners:{stats:'/api/stats',miners:'/api/miners'},blocks:{stats:'/api/stats',blocks:'/api/blocks'},payments:{payments:'/api/payments'},account:{account:accountAddress?'/api/accounts/'+accountAddress:null},status:{health:'/api/health'},forge:{health:'/api/health'},admin:{admin:'/api/admin/health',blocks:'/api/blocks',payments:'/api/payments'},detail:{detail:'/api'+location.pathname.replace(/\/$/,'')}};
      if(page==='calculator')routes.calculator={stats:'/api/stats'};
      const selected=Object.entries(routes[page] || {});
      const replies=await Promise.allSettled(selected.map(([,url])=>url?get(url):Promise.reject(new Error('Choose an address'))));
      replies.forEach((reply,i)=>{
        const key=selected[i][0];
        if(reply.status==='fulfilled'){store[key]=reply.value.data;if(key==='health')store[key]._gatewayStale=reply.value.stale;stale ||= reply.value.stale;return;}
        if(key==='health'&&store.health)store.health._gatewayStale=true;
        failed=true;
        if(page==='account'){put('account-empty','<div class="notice">'+E(reply.reason.message)+' <a href="/start">Mining setup ↗</a></div>');put('workers-table',missing(5,'No live worker data available.'));}
        if(page==='detail')put('chain-detail','<p class="empty">'+(reply.reason.status===404?'No such record on this network.':'Chain record temporarily unavailable.')+'</p>');
        if(!store[key]){
          if(key==='health'){put('chain-list','<p class="empty">Network status is temporarily unavailable.</p>');text('network-summary','Live network status is unavailable. Mining readiness cannot be verified right now.');}
          if(key==='blocks'){put('recent-blocks',missing(4,'Block data temporarily unavailable.'));if(page==='blocks')put('block-content','<p class="empty">Block data temporarily unavailable. The site will retry.</p>');}
          if(key==='payments'){put('recent-payments',missing(3,'Payment data temporarily unavailable.'));put('payments-table',missing(4,'Payment data temporarily unavailable.'));}
          if(key==='miners')put('miners-table',missing(5,'Miner data temporarily unavailable.'));
          if(key==='history')put('hashrate-chart','<p class="empty">Observed history temporarily unavailable.</p>');
        }
      });
      if(!['matured','immature','candidates','orphans','luck'].includes(blockTab))blockTab='matured';
      renderMetrics();renderChains();renderRecent();minerRows();blockRows();paymentRows();renderAccount();chart();renderAdmin();renderDetail();dataStatus(failed,stale);
    }catch(error){dataStatus(true,false);console.warn('Pool view could not refresh.');}
    finally{busy=false;$('refresh-data').disabled=false;clearTimeout(timer);timer=setTimeout(refresh,20000);}
  }
  $('refresh-data').addEventListener('click',refresh);
  document.addEventListener('visibilitychange',()=>{document.body.classList.toggle('page-hidden',document.hidden);oceanController?.setMotion(!document.documentElement.classList.contains('motion-off'));if(document.hidden)clearTimeout(timer);else refresh();});
  refresh();
})();
