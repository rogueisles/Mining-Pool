package forgepool

import "testing"

func TestMultiAuxTreeUsesDistinctSlotsAndCorrectBranches(t *testing.T) {
	leaves := map[uint32]Hash{OGGAuxChainID: {1}, DOGEAuxChainID: {2}, 63: {3}, 50: {4}, 16: {5}}
	tree, err := NewAuxTree(leaves, 21)
	if err != nil {
		t.Fatal(err)
	}
	if tree.Size != 32 {
		t.Fatal("PEP and OGG must not collide in the tree")
	}
	seen := map[uint32]bool{}
	for id, leaf := range leaves {
		i := tree.Indices[id]
		if seen[i] {
			t.Fatal("slot collision")
		}
		seen[i] = true
		if ExpectedChainIndex(tree.Nonce, id, tree.Size) != i {
			t.Fatal("nonstandard AuxPoW index")
		}
		if MerkleRootFromBranch(leaf, tree.Branches[id], i) != tree.Root {
			t.Fatal("invalid native AuxPoW branch")
		}
	}
	again, err := NewAuxTree(leaves, 21)
	if err != nil || again.Root != tree.Root {
		t.Fatal("map iteration changed commitment")
	}
}

func TestMultiAuxTreeRejectsUnboundedCollisionAndZeroLeaf(t *testing.T) {
	if _, err := NewAuxTree(map[uint32]Hash{1: {1}, 65537: {2}}, 0); err == nil {
		t.Fatal("unbounded collision accepted")
	}
	if _, err := NewAuxTree(map[uint32]Hash{1: {1}, 2: {}}, 0); err == nil {
		t.Fatal("zero leaf accepted")
	}
}
