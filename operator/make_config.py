#!/usr/bin/env python3
"""Create NEW private operator configs. No deployment, RPC calls or wallet imports."""
import argparse, getpass, json, os, re
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]
def wallet(raw):
    if not re.fullmatch(r'0x[0-9a-fA-F]{40}',raw) or int(raw[2:],16)==0:
        raise ValueError('Supply a nonzero public RGN address, never a private key')
    return raw.lower()

def configurations(address, folder, ipc, credentials):
    address=wallet(address);folder=Path(folder)
    if not folder.is_absolute() or not Path(ipc).is_absolute():raise ValueError('Absolute output and IPC paths required')
    p=json.loads((ROOT/'network/public-profile.json').read_text())
    c=dict(name='rgn-reference',coin='rgn',threads=2,newrelicEnabled=False,
        redis=dict(endpoint='127.0.0.1:6379',password=credentials['redis'],database=0,poolSize=10),
        upstream=[dict(name='private-rgn-work-gate',url='http://127.0.0.1:38096',timeout='20s')],
        upstreamCheckInterval='5s')
    policy=dict(workers=2,resetInterval='1h',refreshInterval='1m',limits=dict(enabled=False,limit=30,grace='5m',limitJump=10),
        banning=dict(enabled=False,timeout=1800,invalidPercent=30,checkThreshold=100,malformedLimit=100))
    f=dict(enabled=True,multiChain=True,externalChainPolicy='dgb-doge',additionalAux=[],retainExternalWork=False,
        accountingJournalDir=str(folder/'accounting'),extranonce1Size=4,extranonce2Size=4,jobBacklog=64,
        maxNTimeDriftSec=7200,templateValiditySec=120,templateQuorum=p['templateQuorum'],templateSigners=p['templateSigners'],
        variableDifficulty=dict(enabled=True,minimum=50000,maximum=2000000,targetSeconds=2,retargetSeconds=30))
    for coin,port in [('digibyte',33522),('dogecoin',32555)]:
        f[coin]=dict(name=coin,url='http://127.0.0.1:'+str(port),timeout='8s',
            username=credentials[coin]['username'],password=credentials[coin]['password'],payoutScript=p['payoutScripts'][coin])
    c['proxy']=dict(enabled=True,listen='127.0.0.1:38095',behindReverseProxy=False,healthCheck=True,maxFails=3,
        difficulty=50000,blockRefreshInterval='1s',stateUpdateInterval='10s',hashrateExpiration='1h',
        limitHeadersSize=4096,limitBodySize=4096,policy=policy,
        stratum=dict(enabled=True,listen='127.0.0.1:3333',timeout='120s',maxConn=4096,tls=False),forge=f)
    c['api']=dict(enabled=False,listen='127.0.0.1:8080',statsCollectInterval='5s',hashrateWindow='30m',
        hashrateLargeWindow='3h',luckWindow=[64,128],payments=30,blocks=50,purgeInterval='10m')
    c['unlocker']=dict(enabled=False,poolFee=0,poolFeeAddress='',depth=32,immatureDepth=16,interval='15s',
        timeout='5s',daemon='http://127.0.0.1:38096',forgeBlock=1100100,keepTxFees=False,donate=False)
    c['payouts']=dict(enabled=False,requirePeers=1,interval='1h',timeout='5s',gas='21000',gasPrice='1000000000',
        autoGas=False,threshold=1000000000,bgsave=True,daemon='unix:///run/rgn-pool-payments/signer.sock',address=address)
    clone=lambda:json.loads(json.dumps(c))
    unlocker=clone();unlocker['proxy']['enabled']=False;unlocker['proxy']['stratum']['enabled']=False;unlocker['unlocker']['enabled']=True
    api=clone();api['proxy']['enabled']=False;api['proxy']['stratum']['enabled']=False;api['api']['enabled']=True
    payouts=clone();payouts['proxy']['enabled']=False;payouts['proxy']['stratum']['enabled']=False
    gate=dict(armed=False,poolAddress=address,ipc=ipc,activationHeight=1100100)
    return {'mining.json':c,'unlocker.json':unlocker,'api.json':api,'payouts-disabled.json':payouts,'gate.json':gate}

def main():
    a=argparse.ArgumentParser(description=__doc__)
    a.add_argument('--wallet',required=True,type=wallet);a.add_argument('--output',required=True,type=Path)
    a.add_argument('--ipc',required=True,help='Absolute IPC socket of your dedicated RGN node')
    args=a.parse_args()
    if os.name!='posix':raise SystemExit('Run on the Linux pool host so private file permissions can be enforced')
    if not args.output.is_absolute() or args.output.exists():raise SystemExit('Choose a NEW absolute private directory outside this repository')
    if args.output.resolve().is_relative_to(ROOT.resolve()):raise SystemExit('Runtime secrets must stay outside the source repository')
    credentials={'redis':getpass.getpass('Private Redis password (hidden): ')}
    for coin in ('digibyte','dogecoin'):
        credentials[coin]=dict(username=input(coin+' local RPC username: ').strip(),password=getpass.getpass(coin+' local RPC password (hidden): '))
    if not credentials['redis'] or any(not credentials[x][k] for x in ('digibyte','dogecoin') for k in ('username','password')):
        raise SystemExit('Empty credentials refused; use your own private native RPC authentication')
    configs=configurations(args.wallet,args.output,args.ipc,credentials)
    args.output.mkdir(mode=0o700)
    (args.output/'accounting').mkdir(mode=0o700)
    for name,value in configs.items():
        fd=os.open(args.output/name,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
        with os.fdopen(fd,'w') as f:json.dump(value,f,indent=2);f.write('\n')
    print(json.dumps(dict(configurationsPrepared=True,path=str(args.output),poolAddress=args.wallet,
        publicMiningEnabled=False,payoutsEnabled=False,gateArmed=False,servicesChanged=False,credentialsPrinted=False)))
if __name__=='__main__':main()
