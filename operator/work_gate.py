#!/usr/bin/env python3
"""Local work adapter. --check is read-only; work issuance requires --arm AND fork height."""
import argparse, hashlib, json, os, re, socket, stat, threading
from pathlib import Path
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
ROOT=Path(__file__).resolve().parents[1]
RAW=(ROOT/'network/RGN-mainnet-1100100-genesis.json').read_bytes()
if hashlib.sha256(RAW).hexdigest()!='b983ee4de13fb37ad68cfe3807d902955b969b32f2d7604310be07bc3ff38323':
    raise RuntimeError('Bundled mainnet configuration differs')
EXPECTED=json.loads(RAW)['config'];ACTIVATION=1100100
GENESIS='0xdc4d38c0ef41d3fdf50642fdf5fc99e221eadd09e73bbe616724f55a136270ab'
READS={'net_version','net_peerCount','eth_blockNumber','eth_syncing','eth_coinbase','eth_mining',
 'eth_getBlockByNumber','eth_getBlockByHash','eth_getUncleByBlockNumberAndIndex','eth_getTransactionReceipt','eth_getBalance','eth_getBlockReward'}
class GateError(ValueError):pass
def config(path):
    fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
    with os.fdopen(fd,'rb') as f:
        i=os.fstat(f.fileno())
        if not stat.S_ISREG(i.st_mode) or i.st_uid not in (0,os.geteuid()) or i.st_nlink!=1 or i.st_mode&0o077 or i.st_size>4096:
            raise GateError('Gate config must be an owner-only regular file')
        c=json.load(f)
    if set(c)!={'armed','poolAddress','ipc','activationHeight'} or type(c['armed'])!=bool or c['activationHeight']!=ACTIVATION:
        raise GateError('Gate configuration differs')
    if not re.fullmatch(r'0x[0-9a-f]{40}',c['poolAddress']) or int(c['poolAddress'][2:],16)==0 or not Path(c['ipc']).is_absolute():
        raise GateError('Wallet or IPC path differs')
    return c
def ipc_rpc(path,method,params):
    with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as s:
        s.settimeout(8);s.connect(path)
        s.sendall((json.dumps(dict(jsonrpc='2.0',id=1,method=method,params=params))+'\n').encode());raw=b''
        while len(raw)<=4*1024*1024:
            part=s.recv(65536)
            if not part:raise GateError('RGN IPC closed')
            raw+=part
            if b'\n' in raw:
                v=json.loads(raw.split(b'\n',1)[0])
                if v.get('id')!=1 or v.get('error'):raise GateError('RGN RPC failed')
                return v['result']
        raise GateError('RGN RPC response exceeded limit')
class Gate:
    def __init__(self,rpc,control):self.rpc=rpc;self.control=control;self.lock=threading.Lock()
    def snapshot(self):
        if self.rpc('admin_nodeInfo',[])['protocols']['eth']['config']!=EXPECTED:raise GateError('RGN consensus configuration differs')
        if self.rpc('net_version',[])!='70088' or self.rpc('eth_getBlockByNumber',['0x0',False])['hash'].lower()!=GENESIS:
            raise GateError('RGN chain identity differs')
        if self.rpc('eth_syncing',[]) is not False or int(self.rpc('net_peerCount',[]),16)<1:raise GateError('RGN node is not ready')
        return int(self.rpc('eth_getBlockByNumber',['latest',False])['number'],16)
    def allowed(self):
        c=self.control()
        if c['armed'] is not True:raise GateError('Mining is not armed')
        h=self.snapshot()
        if h+1<ACTIVATION:raise GateError('Awaiting fork height 1100100')
        return c,h
    def handle(self,method,params):
        if not isinstance(params,list):raise GateError('RPC params must be an array')
        if method in READS:return self.rpc(method,params)
        if method not in ('eth_createAuxBlock','eth_submitAuxBlock'):raise GateError('Unsupported pool RPC method')
        with self.lock:
            c,before=self.allowed()
            if self.rpc('eth_coinbase',[]).lower()!=c['poolAddress']:raise GateError('Set your dedicated node beneficiary to your pool wallet')
            if method=='eth_submitAuxBlock':
                if len(params)!=2 or any(not isinstance(x,str) or not re.fullmatch(r'0x(?:[0-9a-fA-F]{2})+',x) for x in params):
                    raise GateError('Invalid proof submission')
                return self.rpc(method,params)
            if params:raise GateError('Work issuance takes no parameters')
            # The native method starts work creation. Never call before allowed().
            w=self.rpc(method,params)
            if int(w['height'],16)!=before+1 or int(w['proofVersion'],16)!=4:raise GateError('V4 work height or version differs')
            pending=self.rpc('eth_getBlockByNumber',['pending',False])
            if not pending or pending.get('miner','').lower()!=c['poolAddress']:raise GateError('Pending beneficiary differs')
            if self.snapshot()!=before:raise GateError('Head changed; request fresh work')
            return w
class Server(ThreadingHTTPServer):
    daemon_threads=True
    def get_request(self):
        s,a=super().get_request();s.settimeout(5);return s,a
class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_POST(self):
        ident=None
        try:
            n=int(self.headers.get('Content-Length','0'))
            if self.path!='/' or n<1 or n>1024*1024 or self.headers.get('Transfer-Encoding'):raise GateError('Unsupported request')
            q=json.loads(self.rfile.read(n))
            if not isinstance(q,dict):raise GateError('RPC object required')
            ident=q.get('id')
            if q.get('jsonrpc')!='2.0' or not isinstance(q.get('method'),str):raise GateError('Malformed RPC request')
            v=dict(jsonrpc='2.0',id=ident,result=self.server.gate.handle(q['method'],q.get('params')))
        except Exception as e:v=dict(jsonrpc='2.0',id=ident,error=dict(code=-32000,message=str(e) if isinstance(e,GateError) else 'Pool check unavailable'))
        raw=json.dumps(v).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
def main():
    a=argparse.ArgumentParser(description=__doc__);a.add_argument('--config',required=True,type=Path)
    mode=a.add_mutually_exclusive_group(required=True)
    for name in ('check','serve','arm','disarm'):mode.add_argument('--'+name,action='store_true')
    args=a.parse_args();c=config(args.config)
    g=Gate(lambda m,p:ipc_rpc(c['ipc'],m,p),lambda:config(args.config))
    if args.arm or args.disarm:
        if args.arm:
            g.snapshot()
            if g.rpc('eth_coinbase',[]).lower()!=c['poolAddress']:raise GateError('Node beneficiary differs; nothing armed')
        c['armed']=bool(args.arm)
        fd=os.open(args.config,os.O_WRONLY|os.O_TRUNC|os.O_NOFOLLOW)
        with os.fdopen(fd,'w') as f:json.dump(c,f,indent=2);f.write('\n');f.flush();os.fsync(f.fileno())
        print(json.dumps(dict(miningArmed=c['armed'],forkHeightStillRequired=True,workRequested=False)));return
    if args.check:
        h=g.snapshot();print(json.dumps(dict(mainnetHeight=h,miningArmed=c['armed'],forkCandidateEligible=h+1>=ACTIVATION,
            nodeBeneficiaryMatches=g.rpc('eth_coinbase',[]).lower()==c['poolAddress'],workRequested=False)));return
    server=Server(('127.0.0.1',38096),Handler);server.gate=g;server.serve_forever()
if __name__=='__main__':
    try:main()
    except Exception as e:raise SystemExit(str(e) if isinstance(e,GateError) else 'Local gate failed; inspect private configuration and node readiness')
