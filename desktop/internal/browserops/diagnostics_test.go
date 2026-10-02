package browserops

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsReadOnlyBoundedAndAttributed(t *testing.T) {
	file := ledgerFile{Version: 1, Operations: map[string]*Operation{}}
	for i := range 205 {
		id := fmt.Sprintf("op-%03d", i)
		file.Operations[id] = &Operation{ID: id, DiagnosticScope: "source", State: StateReserved, ReservedAt: time.Unix(int64(i), 0), Generation: "DO-NOT-EXPORT-GRANT", DocumentToken: "DO-NOT-EXPORT-TOKEN", Reason: "password=DO-NOT-EXPORT-PASSWORD"}
	}
	file.Operations["foreign"] = &Operation{ID: "FOREIGN-OP", DiagnosticScope: "other", State: StateUnknown}
	file.Operations["legacy"] = &Operation{ID: "LEGACY-OP", SessionID: "source", State: StateUnknown}
	file.Operations["op-204"].TabID = strings.Repeat("x", 1<<20)
	path := filepath.Join(t.TempDir(), "operations.json")
	before, _ := json.Marshal(file)
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDiagnostics(path, "source")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || !got.Truncated || !got.FieldsTruncated || got.Matched != 205 || len(got.Operations) != 200 || got.Operations[0].OperationID != "op-204" || len(got.Operations[0].TabID) != 160 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	for _, op := range got.Operations {
		if op.State != StateReserved {
			t.Fatalf("export changed state: %+v", op)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("export rewrote ledger")
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"DO-NOT-EXPORT", "FOREIGN-OP", "LEGACY-OP"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("export leaked %s", secret)
		}
	}
	l := &Ledger{file: file}
	if got := l.Diagnostics(""); got.Available || len(got.Operations) != 0 {
		t.Fatal("empty identity must fail closed")
	}
	if got := l.Diagnostics("missing"); !got.Available || len(got.Operations) != 0 {
		t.Fatal("incorrect filtering")
	}
}
