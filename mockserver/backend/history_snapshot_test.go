package backend

import (
	"reflect"
	"testing"
)

func TestCommittedHistoryMutationDoesNotChangeAdmission(t *testing.T) {
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	writer := NewSession(store, ReadCommitted, 1)
	t.Cleanup(writer.SessionEnd)
	commitInitializationTestWrites(t, writer, StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: int64(7)})
	store.SetSchedule([]int{1})
	reader := NewSession(store, ReadCommitted, 2)
	t.Cleanup(reader.SessionEnd)
	tx := beginSessionTransaction(t, reader)
	if value, _, ok := reader.oracleRead("items", "1", "value"); !ok || value != int64(7) {
		t.Fatalf("read before snapshot: value=%v ok=%v", value, ok)
	}
	if err := reader.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
	before := store.CommittedHistory()
	view := store.CommittedHistory()
	for i := range view {
		view[i].Session = 99
		view[i].Seq = 99
		for j := range view[i].Writes {
			view[i].Writes[j].Key = "changed"
			view[i].Writes[j].ID = 99
		}
		for j := range view[i].Reads {
			view[i].Reads[j].Key = "changed"
			view[i].Reads[j].WriteID = 99
		}
		for j := range view[i].Events {
			if write := view[i].Events[j].Write; write != nil {
				write.Key, write.ID = "changed", 99
			}
			if read := view[i].Events[j].Read; read != nil {
				read.Key, read.WriteID = "changed", 99
			}
		}
		view[i].Events = nil
	}
	if !reflect.DeepEqual(before, store.CommittedHistory()) {
		t.Fatal("caller mutation changed the published history")
	}
	// A fresh level must rebuild its oracle from the untouched published log.
	fresh := NewSession(store, Serializable, 3)
	t.Cleanup(fresh.SessionEnd)
	if value, _, ok := fresh.oracleRead("items", "1", "value"); !ok || value != int64(7) {
		t.Fatalf("snapshot mutation changed admission: value=%v ok=%v", value, ok)
	}
}

func TestExplorerConstructionRejectsNil(t *testing.T) {
	if store, err := newStoreWithExplorer(1, 2, nil); err == nil || store != nil {
		t.Fatalf("nil explorer: store=%v err=%v", store, err)
	}
}
