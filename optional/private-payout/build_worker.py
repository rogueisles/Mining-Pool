#!/usr/bin/env python3
"""Build the isolated Unix-capable payout worker; never modify the mining tree."""
import argparse,hashlib,json,os,shutil,subprocess
from pathlib import Path
ROOT=Path(__file__).resolve().parents[2];HERE=Path(__file__).resolve().parent
def main():
    a=argparse.ArgumentParser(description=__doc__);a.add_argument('--output',required=True,type=Path);a.add_argument('--go',default='go');args=a.parse_args()
    out=args.output
    if not out.is_absolute() or out.exists() or out.resolve().is_relative_to(ROOT.resolve()):raise SystemExit('Choose a NEW absolute build directory outside the repository')
    inventory=json.loads((ROOT/'evidence/qualified-core-source-sha256.json').read_text())
    for name,digest in inventory.items():
        if name.endswith('.go') or name in ('go.mod','go.sum'):
            if hashlib.sha256((ROOT/name).read_bytes()).hexdigest()!=digest:raise SystemExit('Core source differs: '+name)
    source=out/'source';source.mkdir(parents=True)
    for name in inventory:
        p=source/name;p.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(ROOT/name,p)
    p=source/'payouts/payer.go';text=p.read_text()
    old='u.rpc = rpc.NewRPCClient("PayoutsProcessor", cfg.Daemon, cfg.Timeout, "", "")'
    new='var err error\n\tu.rpc, err = rpc.NewPayoutRPCClient("PayoutsProcessor", cfg.Daemon, cfg.Timeout)\n\tif err != nil { panic("Invalid private payout daemon configuration") }'
    if text.count(old)!=1:raise SystemExit('Payout transport patch does not match this core')
    p.write_text(text.replace(old,new),encoding='utf-8',newline='\n')
    for name in ('payout_unix.go','payout_unix_test.go','payout_unix_integration_test.go'):
        shutil.copyfile(HERE/(name+'.txt'),source/'rpc'/name)
    env=dict(os.environ,GO111MODULE='on',CGO_ENABLED='1',CGO_CFLAGS='-O2 -g0',CGO_CXXFLAGS='-O2 -g0',GOMAXPROCS='2')
    subprocess.run([args.go,'test','-race','-count=1','./rpc','./payouts'],cwd=source,env=env,check=True)
    subprocess.run([args.go,'build','-mod=readonly','-trimpath','-ldflags=-buildid=','-o',str(out/'rgn-payout-worker'),'.'],cwd=source,env=env,check=True)
    print(json.dumps(dict(payoutWorkerBuilt=True,binarySHA256=hashlib.sha256((out/'rgn-payout-worker').read_bytes()).hexdigest(),servicesChanged=False)))
if __name__=='__main__':main()
