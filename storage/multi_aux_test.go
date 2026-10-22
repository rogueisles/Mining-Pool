package storage

import (
 "encoding/json"
 "strings"
 "testing"
)
func TestMultiAuxExternalLedgersAndReplayAreIndependent(t *testing.T) {
 reset();s:=forgeReceipt("ab")
 s.Pepecoin=ForgeSubmission{Attempted:true,Accepted:true,BlockHash:"0x"+strings.Repeat("11",32)}
 s.Dingocoin=ForgeSubmission{Attempted:true,BlockHash:"0x"+strings.Repeat("22",32),Error:"pending"}
 s.Bellscoin=ForgeSubmission{Attempted:true,Accepted:true,BlockHash:"0x"+strings.Repeat("33",32)}
 if dup,e:=r.WriteForgeShare(s);e!=nil || dup {t.Fatal(dup,e)}
 if dup,e:=r.WriteForgeShare(s);e!=nil || !dup {t.Fatal(dup,e)}
 s.Dingocoin.Accepted=true;s.Dingocoin.Error=""
 if e:=r.WriteForgeExternalOutcomes(s);e!=nil {t.Fatal(e)}
 for chain,want:=range map[string]ForgeSubmission{"pep":s.Pepecoin,"dingo":s.Dingocoin,"bells":s.Bellscoin} {
  raw,e:=r.client.HGet(r.formatKey("forge","submissions",chain,s.Height),s.ID).Result();if e!=nil {t.Fatal(e)}
  var got ForgeSubmission;if json.Unmarshal([]byte(raw),&got)!=nil || got!=want {t.Fatal("mixed outcome",chain,got,want)}
 }
 if got:=r.client.HGet(r.formatKey("shares","roundCurrent"),s.Login).Val();got!="10" {t.Fatal("duplicate credit",got)}
 if got:=r.client.ZCard(r.formatKey("blocks","candidates")).Val();got!=0 {t.Fatal("external win fabricated OGG round")}
 t.Log("PEP, DINGO and BELLS outcomes persisted independently, retried once, and never minted an OGG block")
}
