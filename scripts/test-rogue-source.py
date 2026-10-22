#!/usr/bin/env python3
"""Test source against a new disposable Redis. Never uses an existing database."""
import argparse,json,os,pathlib,socket,subprocess,tempfile,time
p=argparse.ArgumentParser()
p.add_argument('--go',required=True,help='absolute Go 1.16.15 executable')
p.add_argument('--redis-server',required=True,help='absolute local Redis executable')
p.add_argument('--report-dir',required=True,help='NEW directory for test results')
a=p.parse_args();root=pathlib.Path(__file__).resolve().parents[1]
go=pathlib.Path(a.go);redis=pathlib.Path(a.redis_server);out=pathlib.Path(a.report_dir)
assert go.is_absolute() and go.is_file() and redis.is_absolute() and redis.is_file(),'Absolute executable paths required'
assert out.is_absolute(),'Absolute NEW report directory required'
out.mkdir(mode=0o700)
assert (root/'go.mod').is_file(),'Pool repository root missing'
with tempfile.TemporaryDirectory(prefix='rgn-pool-source-test-') as folder:
 with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
 with (out/'redis-test.log').open('wb') as fh:
  process=subprocess.Popen([str(redis),'--bind','127.0.0.1','--port',str(port),'--dir',folder,'--save','','--appendonly','no','--daemonize','no','--protected-mode','yes'],stdout=fh,stderr=subprocess.STDOUT)
  try:
   for _ in range(100):
    if process.poll() is not None:raise RuntimeError('New test Redis did not start; no existing server used')
    try:
     with socket.create_connection(('127.0.0.1',port),timeout=0.5) as s:
      s.sendall(b'*2\r\n$4\r\nINFO\r\n$6\r\nserver\r\n');f=s.makefile('rb');line=f.readline();assert line.startswith(b'$');body=f.read(int(line[1:]))
      assert ('process_id:'+str(process.pid)+'\r\n').encode() in body,'Test Redis identity mismatch'
     break
    except (ConnectionError,OSError):time.sleep(0.1)
   else:raise RuntimeError('New test Redis readiness timed out')
   env=dict(os.environ,FORGE_TEST_REDIS_ADDR='127.0.0.1:'+str(port),FORGE_PHASE6_REDIS_SERVER=str(redis),GOMAXPROCS='2')
   cmd=[str(go),'test','-race','-vet=off','-p=2','-parallel=1','-count=1','-timeout=10m','./forgepool','./proxy','./storage','./rpc','./payouts','./forgesigner','./cmd/forge-live-signer']
   with (out/'pool-source-tests.log').open('wb') as log:result=subprocess.run(cmd,cwd=root,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=800)
   (out/'report.json').write_text(json.dumps({'exitCode':result.returncode,'disposableRedis':True,'existingRedisUsed':False,'command':cmd},indent=2)+'\n')
   raise SystemExit(result.returncode)
  finally:
   if process.poll() is None:process.terminate();process.wait(timeout=20)
