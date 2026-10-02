package evidence

import (
	"strings"
)

// LookupReceipt returns a copy of a receipt by host-issued ID. Arguments are
// dropped: a citation resolves a fact, it never reopens the original call.
func (l *Ledger) LookupReceipt(id string) (Receipt, bool) {
	id = strings.TrimSpace(id)
	if l == nil || id == "" {
		return Receipt{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.receipts {
		if r.ID == id {
			r.Args = nil
			return r, true
		}
	}
	return Receipt{}, false
}

// ReceiptRef returns a bounded model-safe receipt projection.
func (l *Ledger) ReceiptRef(id string) (ReceiptRef, bool) {
	r, ok := l.LookupReceipt(id)
	if !ok {
		return ReceiptRef{}, false
	}
	return r.Ref(), true
}

// CitableReceipts returns up to limit successful receipts from this turn, most
// recent first, so a rejection can list what the model may cite instead of
// asking it to guess a command string.
func (l *Ledger) CitableReceipts(limit int) []ReceiptRef {
	if l == nil || limit <= 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]ReceiptRef, 0, limit)
	for i := len(l.receipts) - 1; i >= 0 && len(out) < limit; i-- {
		r := l.receipts[i]
		if !r.Success || r.ToolName == "complete_step" || r.ToolName == "todo_write" {
			continue
		}
		r.Args = nil
		out = append(out, r.Ref())
	}
	return out
}
