package historycatalog

import (
	"context"
	"database/sql"
)

type searchViewKey struct{}
type searchView struct {
	owner *Catalog
	tx    *sql.Tx
}
type searchReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (c *Catalog) searchDB(ctx context.Context) searchReader {
	if v, _ := ctx.Value(searchViewKey{}).(*searchView); v != nil && v.owner == c {
		return v.tx
	}
	return c.db
}

// CaptureSearch freezes ranking against one FTS read view. The visitor must
// only copy candidates; source file reads belong after this transaction.
func (c *Catalog) CaptureSearch(ctx context.Context, req SearchRequest, visit func(Candidate) error) error {
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ctx = context.WithValue(ctx, searchViewKey{}, &searchView{c, tx})
	req.After = nil
	return c.searchCandidates(ctx, req, true, visit)
}
