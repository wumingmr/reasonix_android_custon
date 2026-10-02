package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"reasonix/internal/agent"
	"reasonix/internal/provider"
)

func buildNativeSearch(ctx context.Context, db *sql.DB, p *nativeHistoryPreparation) error {
	pager := p.pager.WithContext(ctx)
	source := historySourceFromPager(ctx, pager, p.path, p.key)
	for position := 0; position < pager.Header.MessageCount; {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for batch := 0; batch < 32 && position < pager.Header.MessageCount; batch++ {
			var messages []provider.Message
			messages, err = source.fetch(position, position+1)
			if err != nil {
				break
			}
			if len(messages) != 1 {
				err = errors.New("history search source omitted an indexed message")
				break
			}
			message := messages[0]
			preview := message.Content
			if message.Role == provider.RoleUser {
				preview = agent.UserMessageText(message)
			} else if preview == "" {
				preview = message.RawContent
			}
			if agent.IsHostGeneratedUserMessage(message) {
				preview = ""
			}
			preview, err = nativeHistoryPrompt(ctx, preview)
			if err != nil {
				break
			}
			text := strings.Join([]string{message.Content, message.RawContent, message.ReasoningContent}, "\n")
			_, err = tx.ExecContext(ctx, `INSERT INTO documents(position,role,preview,text) VALUES(?,?,?,?)`, position, message.Role, preview, text)
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO documents_fts(rowid,text) VALUES(?,?)`, position, text)
			}
			if err != nil {
				break
			}
			position++
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if err := pager.Validate(); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO metadata VALUES('complete',?)`, p.key)
	return err
}
