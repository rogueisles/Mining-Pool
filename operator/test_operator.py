import copy,importlib.util,json,os,stat,sys,tempfile,unittest
from unittest.mock import patch
from pathlib import Path
HERE=Path(__file__).resolve().parent
def load(name):
    spec=importlib.util.spec_from_file_location(name,HERE/(name+'.py'));m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m);return m
gate=load('work_gate');make=load('make_config')
ADDRESS='0x1111111111111111111111111111111111111111'
class Backend:
    def __init__(self,height=1100099):self.height=height;self.calls=[];self.peer='0x2';self.synced=False;self.cfg=copy.deepcopy(gate.EXPECTED);self.version=4;self.beneficiary=ADDRESS
    def rpc(self,m,p):
        self.calls.append(m)
        if m=='admin_nodeInfo':return {'protocols':{'eth':{'config':self.cfg}}}
        if m=='net_version':return '70088'
        if m=='net_peerCount':return self.peer
        if m=='eth_syncing':return self.synced
        if m=='eth_coinbase':return self.beneficiary
        if m=='eth_createAuxBlock':return dict(height=hex(self.height+1),proofVersion=hex(self.version))
        if m=='eth_submitAuxBlock':return True
        if m=='eth_getBlockByNumber':
            if p[0]=='0x0':return {'hash':gate.GENESIS}
            if p[0]=='pending':return {'miner':self.beneficiary}
            return {'number':hex(self.height)}
        raise AssertionError('Unexpected mutation '+m)
class Tests(unittest.TestCase):
    def fixture(self,height=1100099,armed=True):
        b=Backend(height);control=dict(armed=armed,poolAddress=ADDRESS)
        return b,gate.Gate(b.rpc,lambda:control)
    def test_prefork_never_requests_work_or_changes_miner(self):
        b,g=self.fixture(1100098)
        for method,params in [('eth_createAuxBlock',[]),('eth_submitAuxBlock',['0x00','0x00'])]:
            with self.assertRaises(gate.GateError):g.handle(method,params)
        self.assertNotIn('eth_createAuxBlock',b.calls);self.assertNotIn('eth_submitAuxBlock',b.calls)
        self.assertFalse(any(x.startswith('miner_') for x in b.calls))
    def test_first_fork_candidate_allowed(self):
        b,g=self.fixture();self.assertEqual(g.handle('eth_createAuxBlock',[])['height'],hex(1100100))
    def test_unarmed_refused(self):
        b,g=self.fixture(armed=False)
        with self.assertRaises(gate.GateError):g.handle('eth_createAuxBlock',[])
        self.assertNotIn('eth_createAuxBlock',b.calls)
    def test_wrong_beneficiary_no_issuance(self):
        b,g=self.fixture();b.beneficiary='0x2222222222222222222222222222222222222222'
        with self.assertRaises(gate.GateError):g.handle('eth_createAuxBlock',[])
        self.assertNotIn('eth_createAuxBlock',b.calls)
    def test_wrong_config_or_no_peers_or_syncing(self):
        for field,value in [('cfg',{}),('peer','0x0'),('synced',{'currentBlock':'0x1'})]:
            b,g=self.fixture();setattr(b,field,value)
            with self.assertRaises(gate.GateError):g.handle('eth_createAuxBlock',[])
            self.assertNotIn('eth_createAuxBlock',b.calls)
    def test_wrong_protocol_rejected(self):
        b,g=self.fixture();b.version=3
        with self.assertRaises(gate.GateError):g.handle('eth_createAuxBlock',[])
    def test_no_generic_wallet_or_admin_forwarding(self):
        b,g=self.fixture()
        for m in ('eth_sendTransaction','personal_unlockAccount','miner_start','admin_addPeer'):
            with self.assertRaises(gate.GateError):g.handle(m,[])
        self.assertEqual(b.calls,[])
    def test_config_automatic_public_commitments(self):
        with tempfile.TemporaryDirectory() as d:
            c=make.configurations(ADDRESS,Path(d),str(Path(d)/'test-node.ipc'),{'redis':'fixture-only','digibyte':{'username':'fixture','password':'fixture-only'},'dogecoin':{'username':'fixture','password':'fixture-only'}})
            p=json.loads((HERE.parent/'network/public-profile.json').read_text())
            f=c['mining.json']['proxy']['forge']
            self.assertEqual(f['templateQuorum'],2);self.assertEqual(f['templateSigners'],p['templateSigners'])
            for coin in ('digibyte','dogecoin'):self.assertEqual(f[coin]['payoutScript'],p['payoutScripts'][coin])
            self.assertEqual(c['mining.json']['payouts']['address'],ADDRESS)
            self.assertFalse(c['gate.json']['armed']);self.assertFalse(c['payouts-disabled.json']['payouts']['enabled'])
            self.assertEqual(c['payouts-disabled.json']['payouts']['interval'],'1h')
    def test_private_key_not_accepted_as_wallet(self):
        for x in ('0x'+'1'*64,'0x'+'0'*40,''):
            with self.assertRaises(ValueError):make.wallet(x)
    @unittest.skipUnless(os.name=='posix','Linux permissions')
    def test_generated_secrets_stay_private_and_not_on_stdout(self):
        import contextlib,io
        with tempfile.TemporaryDirectory() as d:
            out=Path(d)/'private';stdout=io.StringIO()
            with patch.object(sys,'argv',['make_config.py','--wallet',ADDRESS,'--ipc','/tmp/node.ipc','--output',str(out)]),patch.object(make.getpass,'getpass',return_value='fixture-password-never-print'),patch('builtins.input',return_value='fixture-user'),contextlib.redirect_stdout(stdout):
                make.main()
            self.assertNotIn('fixture-password-never-print',stdout.getvalue())
            self.assertEqual(stat.S_IMODE(out.stat().st_mode),0o700)
            for p in out.glob('*.json'):self.assertEqual(stat.S_IMODE(p.stat().st_mode),0o600)
            self.assertFalse(gate.config(out/'gate.json')['armed'])
            (out/'gate.json').chmod(0o644)
            with self.assertRaises(gate.GateError):gate.config(out/'gate.json')
if __name__=='__main__':unittest.main()
