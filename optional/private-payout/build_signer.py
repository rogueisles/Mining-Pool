#!/usr/bin/env python3
"""Build/test the reference private wallet service against a clean pinned node checkout."""
import argparse,json,os,shutil,subprocess,tempfile
from pathlib import Path
COMMIT='18367e0aea25899af7fe8d608f0a19fa420740a9'
HERE=Path(__file__).resolve().parent
def git(root,*args):return subprocess.check_output(['git','-C',str(root),*args],text=True).strip()
def main():
    a=argparse.ArgumentParser(description=__doc__);a.add_argument('--node-source',required=True,type=Path)
    a.add_argument('--output',required=True,type=Path);a.add_argument('--go',default='go');args=a.parse_args()
    if os.name!='posix':raise SystemExit('Build on Linux')
    if git(args.node_source,'rev-parse','HEAD')!=COMMIT or git(args.node_source,'status','--porcelain','--untracked-files=no'):
        raise SystemExit('A clean pinned Rogue-Network node checkout is required')
    if not args.output.is_absolute() or args.output.exists():raise SystemExit('Choose a NEW absolute output directory')
    args.output.mkdir(parents=True,mode=0o700)
    with tempfile.TemporaryDirectory(prefix='rgn-private-wallet-build-') as name:
        gp=Path(name);node=gp/'src/github.com/TeamEGEM/go-egem';node.mkdir(parents=True)
        # Only tracked source files are copied; local credentials/untracked files are excluded.
        tracked=subprocess.check_output(['git','-C',str(args.node_source),'ls-files','-z']).decode().split('\0')
        for relative in filter(None,tracked):
            src=args.node_source/relative;dst=node/relative
            if src.is_symlink() or not src.is_file():raise SystemExit('Unsupported node source member')
            dst.parent.mkdir(parents=True,exist_ok=True);shutil.copyfile(src,dst)
        service=node/'cmd/rgn-private-payout-reference';service.mkdir()
        for src in (HERE/'signer').glob('*.go.txt'):shutil.copyfile(src,service/src.name[:-4])
        env=dict(os.environ,GO111MODULE='off',GOPROXY='off',GOPATH=str(gp),CGO_ENABLED='1',CGO_CFLAGS='-O2 -g0',CGO_CXXFLAGS='-O2 -g0',GOMAXPROCS='2')
        subprocess.run([args.go,'test','-race','-count=1','-timeout=120s','.'],cwd=service,env=env,check=True)
        subprocess.run([args.go,'build','-trimpath','-ldflags=-buildid=','-o',str(args.output/'rgn-private-payout'),'.'],cwd=service,env=env,check=True)
        for relative in ('COPYING','COPYING.LESSER'):
            shutil.copyfile(node/relative,args.output/relative)
    print(json.dumps(dict(privateWalletReferenceBuilt=True,nodeSourceCommit=COMMIT,liveWalletUsed=False,servicesChanged=False)))
if __name__=='__main__':main()
