package transcript

import "testing"

func TestSnapshotLocatesNativeMessageWithinFrozenCut(t *testing.T) {
	p, err := NewProjection(Identity{SessionID: "native", RuntimeEpoch: "epoch"}, []Message{
		{Role: "user", MessageID: "first", Content: "first"},
		{Role: "assistant", MessageID: "answer", Content: "answer"},
		{Role: "user", MessageID: "last", Content: "last"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := p.Snapshot(PageRequest{Records: 1})
	if err != nil {
		t.Fatal(err)
	}
	page, err := p.Snapshot(PageRequest{SnapshotID: initial.SnapshotID, MessageID: "answer", Records: 1})
	if err != nil || page.NotFound || len(page.Records) != 1 || page.Records[0].Message.MessageID != "answer" || page.Records[0].Order != 1 {
		t.Fatalf("located page: %+v, %v", page, err)
	}
	missing, err := p.Snapshot(PageRequest{SnapshotID: initial.SnapshotID, MessageID: "absent", Records: 1})
	if err != nil || !missing.NotFound || len(missing.Records) != 0 {
		t.Fatalf("missing: %+v, %v", missing, err)
	}
}
