#!/usr/bin/env python3
"""Independent, read-only pool website. Never controls mining, Redis or payouts."""
import argparse
import base64
import collections
from contextlib import contextmanager
import gzip
import hashlib
import hmac
import json
import mimetypes
import os
from pathlib import Path
import re
import socketserver
import sqlite3
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

ROOT = Path(__file__).resolve().parent
ACCOUNT = re.compile(r"/api/accounts/0x[0-9a-fA-F]{40}$")
HASH = re.compile(r"0x[0-9a-fA-F]{64}$")
API_PATHS = {'/api/stats', '/api/miners', '/api/blocks', '/api/payments', '/api/health'}
PUBLIC_HEALTH = ('updated', 'chainId', 'genesisHash', 'readyForNiceHash', 'ogg', 'external',
                 'freshCombinedJob', 'peerCanonicalCheck', 'accountingAcceptsWork', 'poolServicesAcceptsWork',
                 'activeExternalMask', 'requiresDgbOrDogeWork', 'revenueAnchorPresent', 'activationHeight', 'miningArmed', 'miningState', 'payoutsEnabled', 'payouts', 'services', 'accounting', 'stratum')
READ_RPC = frozenset(('net_version', 'net_peerCount', 'eth_chainId', 'web3_clientVersion',
    'eth_syncing', 'eth_blockNumber', 'eth_getBalance', 'eth_getCode', 'eth_getTransactionCount',
    'eth_getBlockByNumber', 'eth_getBlockByHash', 'eth_getTransactionByHash', 'eth_getTransactionReceipt', 'eth_gasPrice'))

class Missing(Exception): pass
class Unavailable(Exception): pass
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args): return None

def read_json(path): return json.loads(Path(path).read_text(encoding='utf-8'))
def safe_numbers(data):
    if isinstance(data,dict): return {k:safe_numbers(v) for k,v in data.items()}
    if isinstance(data,(list,tuple)): return [safe_numbers(v) for v in data]
    if type(data) is int and abs(data)>2**53-1: return str(data)
    return data
def encode(data): return json.dumps(safe_numbers(data), separators=(',', ':'), ensure_ascii=False).encode()
def public_health(value):
    out = {k: value[k] for k in PUBLIC_HEALTH if k in value}
    if 'external' in out:
        out['external'] = {coin: {k: v for k, v in node.items() if k in ('healthy','chain','blocks','headers','initialblockdownload','connections','pruned','networkactive','state')} for coin, node in out['external'].items() if coin in ('digibyte','dogecoin') and isinstance(node,dict)}
    if 'ogg' in out:
        out['ogg'] = {k: v for k, v in out['ogg'].items() if k != 'peerHeads'}
    peer = out.get('peerCanonicalCheck', {})
    out['peerCanonicalCheck'] = {k: peer[k] for k in ('healthy','height','hash','lagBlocks','commonHeight','sameHead','convergencePending','state','checkedAt') if k in peer}
    return out

class Backend:
    def __init__(self, cfg, upstream, rpc_url, state_dir):
        self.cfg, self.upstream, self.rpc_url = cfg, upstream.rstrip('/'), rpc_url
        for url in (upstream, rpc_url):
            parts = urllib.parse.urlsplit(url)
            if parts.scheme not in ('http', 'https') or parts.username or parts.password or parts.query or parts.fragment:
                raise ValueError('Invalid fixed upstream configuration')
        self.http = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        self.cache, self.lock = collections.OrderedDict(), threading.RLock()
        self.fetch_slots = threading.BoundedSemaphore(6)
        self.state_dir = Path(state_dir); self.state_dir.mkdir(parents=True, exist_ok=True)
        # Chain-specific history keeps retained D12 samples out of D14 charts.
        self.db = self.state_dir / ('history-'+str(cfg['chainId'])+'-'+cfg['genesisHash'][2:14]+'.sqlite3')
        with self.connect() as db:
            db.execute('CREATE TABLE IF NOT EXISTS samples (t INTEGER PRIMARY KEY, hashrate REAL, miners INTEGER, height INTEGER, payments INTEGER)')
        self.last_error, self.last_sample = None, None
        self.identity_until = 0
        self.stop = threading.Event()

    @contextmanager
    def connect(self):
        db = sqlite3.connect(self.db, timeout=3)
        try:
            db.execute('PRAGMA journal_mode=WAL')
            with db: yield db
        finally: db.close()

    def request(self, url, body=None):
        if not self.fetch_slots.acquire(timeout=2): raise Unavailable('Upstream busy')
        try:
            req = urllib.request.Request(url, data=None if body is None else encode(body), headers={'Content-Type':'application/json','User-Agent':'RogueNetworkPool/1.0'})
            try:
                with self.http.open(req, timeout=5) as r:
                    raw = r.read(4*1024*1024+1)
                    if r.geturl() != url or len(raw) > 4*1024*1024: raise Unavailable()
                return json.loads(raw)
            except urllib.error.HTTPError as e:
                if e.code == 404: raise Missing() from None
                raise Unavailable() from None
            except (OSError, ValueError): raise Unavailable() from None
        finally: self.fetch_slots.release()

    def cached(self, key, load, ttl=10, stale_limit=180):
        now = time.time()
        with self.lock:
            entry = self.cache.get(key)
            if entry and now-entry[0] < ttl:
                self.cache.move_to_end(key)
                return entry[1], entry[0], False
        try:
            value = load()
            with self.lock:
                self.cache[key] = (now, value); self.cache.move_to_end(key)
                while len(self.cache) > 256: self.cache.popitem(last=False)
            return value, now, False
        except Missing: raise
        except Exception:
            if entry and now-entry[0] < stale_limit: return entry[1], entry[0], True
            raise Unavailable() from None

    def get(self, path):
        if path not in API_PATHS and not ACCOUNT.fullmatch(path): raise Missing()
        def load():
            if path != '/api/health':
                health, _, stale = self.get('/api/health')
                if stale or not 0 <= time.time()-float(health.get('updated',0)) < 40:
                    raise Unavailable('Live chain identity unavailable')
            data = self.request(self.upstream+path)
            if not isinstance(data, dict): raise Unavailable()
            if path == '/api/health':
                if data.get('chainId') != self.cfg['chainId'] or data.get('genesisHash','').lower() != self.cfg['genesisHash'].lower():
                    raise Unavailable('Network identity mismatch')
                if not 0 <= time.time()-float(data.get('updated',0)) < 40: raise Unavailable('Stale network status')
            return data
        return self.cached(path, load)

    def rpc(self, method, params):
        result = self.request(self.rpc_url, {'jsonrpc':'2.0','id':1,'method':method,'params':params})
        if result.get('error') or result.get('id') != 1 or 'result' not in result: raise Unavailable()
        return result['result']

    def wallet_read(self, request):
        ident = request.get('id') if isinstance(request, dict) else None
        def error(code, message): return {'jsonrpc':'2.0','id':ident,'error':{'code':code,'message':message}}
        if not isinstance(request, dict) or request.get('jsonrpc') != '2.0' or request.get('method') not in READ_RPC:
            return error(-32601, 'Read-only method unavailable')
        params = request.get('params', [])
        if not isinstance(params, list) or len(params) > 3 or isinstance(ident, (dict,list)):
            return error(-32602, 'Invalid request parameters')
        # Never serve a wallet a configured D14 identity over another chain.
        try:
            self.verify_identity()
            result = hex(self.cfg['chainId']) if request['method'] == 'eth_chainId' else self.rpc(request['method'], params)
            return {'jsonrpc':'2.0','id':ident,'result':result}
        except (Unavailable, Missing, OSError, ValueError, KeyError):
            return error(-32000, 'Pinned mainnet read-only node unavailable')

    def verify_identity(self):
        if time.monotonic() < self.identity_until: return
        version = self.rpc('net_version', [])
        genesis = self.rpc('eth_getBlockByNumber', ['0x0',False])
        if str(version) != str(self.cfg['chainId']) or not genesis or genesis.get('hash','').lower() != self.cfg['genesisHash'].lower():
            raise Unavailable('Network identity mismatch')
        self.identity_until = time.monotonic()+20

    def detail(self, kind, ident):
        def load():
            self.verify_identity()
            if kind == 'block':
                if HASH.fullmatch(ident): value = self.rpc('eth_getBlockByHash',[ident,False])
                elif ident.isdecimal() and len(ident) <= 12: value = self.rpc('eth_getBlockByNumber',[hex(int(ident)),False])
                else: raise Missing()
                if value is None: raise Missing()
                canonical = self.rpc('eth_getBlockByNumber',[value['number'],False])
                return {'block':value,'canonical':bool(canonical and canonical.get('hash')==value.get('hash'))}
            if not HASH.fullmatch(ident): raise Missing()
            tx = self.rpc('eth_getTransactionByHash',[ident])
            if not tx: raise Missing()
            receipt = self.rpc('eth_getTransactionReceipt',[ident])
            canonical = None
            confirmations = 0
            if receipt and receipt.get('blockNumber'):
                block = self.rpc('eth_getBlockByNumber',[receipt['blockNumber'],False])
                canonical = bool(block and block.get('hash') == receipt.get('blockHash'))
                if canonical: confirmations = max(0,int(self.rpc('eth_blockNumber',[]),16)-int(receipt['blockNumber'],16)+1)
            return {'transaction':tx,'receipt':receipt,'canonical':canonical,'confirmations':confirmations}
        return self.cached(kind+ident, load, ttl=10, stale_limit=0)

    def sample(self):
        stats, _, stale = self.get('/api/stats')
        health, _, old = self.get('/api/health')
        if stale or old or not stats.get('now') or abs(time.time()-stats['now']/1000)>45: raise Unavailable()
        if 'hashrate' not in stats or 'minersTotal' not in stats: raise Unavailable()
        t = int(stats['now']/1000)
        with self.connect() as db:
            db.execute('INSERT OR REPLACE INTO samples VALUES (?,?,?,?,?)', (t, stats['hashrate'],stats['minersTotal'],health.get('ogg',{}).get('height'),None))
            db.execute('DELETE FROM samples WHERE t < ?', (int(time.time())-7*86400,))
        self.last_sample, self.last_error = t, None

    def history(self, hours=24):
        with self.connect() as db:
            rows = db.execute('SELECT t,hashrate,miners,height FROM samples WHERE t>=? ORDER BY t',(int(time.time())-hours*3600,)).fetchall()
        # At most 480 observed points, no interpolation or fabricated backfill.
        step = max(1, (len(rows)+478)//479)
        points = rows[::step]
        if rows and points[-1] != rows[-1]: points.append(rows[-1])
        return {'points':points,'columns':['timestamp','hashrate','miners','height'],'intervalSeconds':30,'retentionDays':7,'sampledAt':self.last_sample}

    def run(self):
        while not self.stop.is_set():
            try: self.sample()
            except Exception: self.last_error = 'Sampling unavailable; no values invented'
            self.stop.wait(30)

def check_auth(header, auth):
    if not auth or not isinstance(header,str) or not header.startswith('Basic '): return False
    try:
        raw = base64.b64decode(header[6:], validate=True).decode()
        user, password = raw.split(':',1)
        if len(password)>1024 or len(user)>100: return False
        digest=hashlib.pbkdf2_hmac('sha256',password.encode(),bytes.fromhex(auth['salt']),int(auth['iterations'])).hex()
        return hmac.compare_digest(user,auth['user']) & hmac.compare_digest(digest,auth['digest'])
    except (ValueError,KeyError,TypeError): return False

class BoundedServer(socketserver.ThreadingMixIn, HTTPServer):
    daemon_threads=True; block_on_close=False; allow_reuse_address=True
    slots=threading.BoundedSemaphore(24)
    def process_request(self, request, client_address):
        if not self.slots.acquire(False): request.close(); return
        try: super().process_request(request,client_address)
        except BaseException: self.slots.release(); raise
    def process_request_thread(self, request, client_address):
        try: super().process_request_thread(request,client_address)
        finally: self.slots.release()

def make_handler(backend, auth=None, require_https=True):
    dist = (ROOT/'dist').resolve()
    class Handler(BaseHTTPRequestHandler):
        server_version='RoguePool'; sys_version=''
        def setup(self): super().setup(); self.connection.settimeout(8)
        def log_message(self, format, *args):
            # No query strings, auth headers, addresses or RPC payloads in logs.
            pass
        def send(self, code, body, kind='application/json; charset=utf-8', extra=None):
            try: self._send(code,body,kind,extra)
            except (BrokenPipeError,ConnectionResetError,ConnectionAbortedError,TimeoutError):
                self.close_connection=True
        def _send(self, code, body, kind='application/json; charset=utf-8', extra=None):
            if not isinstance(body,bytes): body=encode(body)
            self.send_response(code)
            self.send_header('Content-Type',kind)
            self.send_header('X-Content-Type-Options','nosniff')
            self.send_header('Referrer-Policy','same-origin')
            self.send_header('X-Frame-Options','DENY')
            self.send_header('Permissions-Policy','camera=(), microphone=(), geolocation=()')
            if self.path == '/rpc':
                self.send_header('Access-Control-Allow-Origin','*')
            hashes=[]
            if kind.startswith('text/html'):
                hashes=["'sha256-"+base64.b64encode(hashlib.sha256(s).digest()).decode()+"'" for s in re.findall(rb'<script type="application/ld\+json">(.*?)</script>',body,re.S)]
            self.send_header('Content-Security-Policy',"default-src 'self'; script-src 'self' "+' '.join(hashes)+"; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
            cache='no-store' if self.path.startswith(('/api','/admin','/rpc')) else ('public, max-age=31536000, immutable' if re.search(r'\.[a-f0-9]{12}\.',self.path) else 'no-cache' if kind.startswith('text/html') else 'public, max-age=300')
            self.send_header('Cache-Control',cache)
            if not backend.cfg.get('indexable') or self.path.startswith(('/admin','/account','/tx/','/block/','/api')): self.send_header('X-Robots-Tag','noindex, nofollow' if self.path.startswith(('/admin','/api')) else 'noindex, follow')
            for key,value in (extra or {}).items(): self.send_header(key,str(value))
            if 'gzip' in self.headers.get('Accept-Encoding','') and len(body)>1000 and not kind.startswith('image/'):
                body=gzip.compress(body,compresslevel=5); self.send_header('Content-Encoding','gzip'); self.send_header('Vary','Accept-Encoding')
            self.send_header('Content-Length',str(len(body))); self.end_headers()
            if self.command != 'HEAD': self.wfile.write(body)
        def do_HEAD(self): self.do_GET()
        def do_OPTIONS(self):
            if self.path != '/rpc': return self.send(405, {'error':'Method unavailable'})
            return self.send(200,b'',extra={'Access-Control-Allow-Methods':'POST, OPTIONS','Access-Control-Allow-Headers':'Content-Type'})
        def do_POST(self):
            if self.path != '/rpc':
                return self.send(405,{'error':'This website is read-only'},extra={'Allow':'GET, HEAD'})
            try:
                length = int(self.headers.get('Content-Length', '0'))
                if self.headers.get('Transfer-Encoding') or not 0 < length <= 1024:
                    return self.send(413, {'error':'Request exceeds bound'})
                value = json.loads(self.rfile.read(length))
                if isinstance(value, list):
                    if not 1 <= len(value) <= 5: return self.send(400, {'error':'Batch exceeds bound'})
                    reply = [backend.wallet_read(v) for v in value]
                else: reply = backend.wallet_read(value)
                return self.send(200, reply)
            except (ValueError, TypeError): return self.send(400, {'error':'Invalid JSON-RPC request'})
        def do_GET(self):
            path=urllib.parse.unquote(urllib.parse.urlsplit(self.path).path)
            if path.startswith('/admin') or path.startswith('/api/admin'):
                secure=(self.headers.get('X-Forwarded-Proto')=='https')
                if require_https and not secure: return self.send(403,{'error':'HTTPS required for protected operations'})
                if not check_auth(self.headers.get('Authorization'),auth):
                    return self.send(401,{'error':'Operator authentication required'},extra={'WWW-Authenticate':'Basic realm="Rogue Pool Operations", charset="UTF-8"'})
            try:
                if path=='/api/config': return self.send(200,backend.cfg)
                if path=='/api/history':
                    hours=urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query).get('hours',['24'])[0]
                    if hours not in ('1','6','24','168'): return self.send(400,{'error':'Unsupported history window'})
                    return self.send(200,backend.history(int(hours)))
                if path=='/api/admin/health':
                    value, at, stale=backend.get('/api/health')
                    return self.send(200,{'pool':value,'website':{'lastSample':backend.last_sample,'samplingError':backend.last_error,'cachedEntries':len(backend.cache),'readOnly':True}},extra={'X-Data-As-Of':at,'X-Data-Stale':str(stale).lower()})
                if path in API_PATHS or ACCOUNT.fullmatch(path):
                    value,at,stale=backend.get(path)
                    if path=='/api/health': value=public_health(value)
                    return self.send(200,value,extra={'X-Data-As-Of':at,'X-Data-Stale':str(stale).lower()})
                match=re.fullmatch(r'/api/(block|tx)/([^/]+)',path)
                if match:
                    value,at,stale=backend.detail(*match.groups())
                    return self.send(200,value,extra={'X-Data-As-Of':at,'X-Data-Stale':str(stale).lower()})
                if path.startswith('/api/'): raise Missing()
                redirects={'/help':'/start','/about':'/forge','/blocks/pending':'/blocks?tab=candidates','/blocks/immature':'/blocks?tab=immature','/blocks/index':'/blocks','/luck':'/blocks?tab=luck'}
                if path in redirects: return self.send(308,b'',extra={'Location':redirects[path]})
                target=(dist/path.lstrip('/')).resolve()
                if not target.is_relative_to(dist): raise Missing()
                if path=='/': target=dist/'index.html'
                elif re.fullmatch(r'/account/0x[0-9a-fA-F]{40}(?:/payouts)?/?',path): target=dist/'account.html'
                elif re.fullmatch(r'/(?:block|tx)/(?:0x[0-9a-fA-F]{64}|[0-9]{1,12})/?',path): target=dist/'detail.html'
                elif not target.suffix: target=target.with_suffix('.html')
                if target.is_file():
                    kind={'.js':'application/javascript','.css':'text/css','.svg':'image/svg+xml','.webp':'image/webp','.html':'text/html'}.get(target.suffix,mimetypes.guess_type(target.name)[0] or 'application/octet-stream')
                    return self.send(200,target.read_bytes(),kind+('; charset=utf-8' if kind.startswith('text/') or kind=='application/javascript' else ''))
                raise Missing()
            except Missing: self.send(404,(dist/'404.html').read_bytes(),'text/html; charset=utf-8') if not path.startswith('/api') else self.send(404,{'error':'No record found'})
            except (Unavailable,OSError,ValueError,KeyError): self.send(503,{'error':'Live data temporarily unavailable; try again shortly'})
    return Handler

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--host',default='127.0.0.1');parser.add_argument('--port',type=int,default=8091)
    parser.add_argument('--config',default=str(ROOT/'config.json'));parser.add_argument('--upstream',default='http://127.0.0.1:8080')
    parser.add_argument('--rpc',default='http://127.0.0.1:18545');parser.add_argument('--state',default=str(ROOT/'state'))
    parser.add_argument('--auth');parser.add_argument('--preview',action='store_true');args=parser.parse_args()
    backend=Backend(read_json(args.config),args.upstream,args.rpc,args.state)
    auth=read_json(args.auth) if args.auth else None
    threading.Thread(target=backend.run,daemon=True).start()
    server=BoundedServer((args.host,args.port),make_handler(backend,auth,not args.preview))
    print('Rogue Network Pool listening on http://'+args.host+':'+str(args.port),flush=True)
    try: server.serve_forever()
    except KeyboardInterrupt: pass
    finally: backend.stop.set(); server.server_close()
if __name__=='__main__': main()
