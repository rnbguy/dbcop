package backend

import "testing"

func TestStagedLookupRollbackAndDrain(t *testing.T) {
	txn := &Txn{}
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: nil})
	txn.stageMembership("items", "1", true)
	before := txn.checkpoint()
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 20})
	txn.stage(StagedWrite{Tab: "items", Pkey: "2", Col: "value", Value: 30})
	txn.stageMembership("items", "1", false)
	txn.restore(before)
	if value, ok := txn.staged("items", "1", "value"); !ok || value != nil {
		t.Fatalf("rollback lost preceding NULL write: %v, %v", value, ok)
	}
	if value, ok := txn.staged("items", "1", rowExistenceColumn); !ok || value != true {
		t.Fatalf("rollback lost preceding membership: %v, %v", value, ok)
	}
	if value, ok := txn.staged("items", "2", "value"); ok {
		t.Fatalf("rollback retained failed statement's new cell: %v", value)
	}
	txn.pending()
	if value, ok := txn.staged("items", "1", "value"); ok {
		t.Fatalf("drained write remains visible: %v", value)
	}
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 40})
	txn.discard()
	if value, ok := txn.staged("items", "1", "value"); ok {
		t.Fatalf("discarded write remains visible: %v", value)
	}
}
