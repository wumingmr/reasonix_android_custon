package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reasonix/internal/sessionexport"
	"strings"

	"reasonix/internal/fileutil"
)

// SaveSessionMarkdownForTab writes the complete host-side transcript without
// transferring or joining it in the renderer. The destination is published
// only after the buffered stream has been flushed and synced.
func (a *App) SaveSessionMarkdownForTab(tabID, path, title string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	query, ref, err := a.canonicalSessionQuery(tabID)
	if err != nil {
		return err
	}
	snapshot, err := query.CaptureExportSnapshot(context.Background(), ref)
	if err != nil {
		return err
	}
	snapshot.Title = title
	dir, err := os.MkdirTemp("", "reasonix-markdown-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if _, err = sessionexport.Build(context.Background(), query, snapshot, dir, nil); err != nil {
		return err
	}
	return writeStreamingExport(path, func(dst io.Writer) error {
		file, err := os.Open(filepath.Join(dir, "markdown"))
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(dst, file)
		return err
	})
}

func writeSessionMarkdown(dst io.Writer, title string, messages []HistoryMessage) error {
	writer := bufio.NewWriterSize(dst, 64<<10)
	heading := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(title, "\r", " "), "\n", " "))
	if heading == "" {
		heading = "Reasonix session"
	}
	if _, err := fmt.Fprintf(writer, "# %s\n\n", heading); err != nil {
		return err
	}
	for _, message := range messages {
		if err := writeSessionMarkdownMessage(writer, message); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeSessionMarkdownMessage(dst io.Writer, message HistoryMessage) error {
	writeSection := func(heading, body string) error {
		body = strings.TrimSpace(body)
		if body == "" {
			return nil
		}
		_, err := fmt.Fprintf(dst, "%s\n\n%s\n\n", heading, body)
		return err
	}
	switch message.Role {
	case "user":
		return writeSection("## User", message.Content)
	case "assistant":
		if err := writeSection("### Reasoning", message.Reasoning); err != nil {
			return err
		}
		if err := writeSection("## Assistant", message.Content); err != nil {
			return err
		}
		for _, call := range message.ToolCalls {
			if _, err := fmt.Fprintf(dst, "### Tool: %s\n\n", call.Name); err != nil {
				return err
			}
			if err := writeMarkdownFence(dst, "Args", call.Arguments); err != nil {
				return err
			}
		}
	case "tool":
		if err := writeMarkdownFence(dst, "Output", message.Content); err != nil {
			return err
		}
		return writeMarkdownFence(dst, "Error", message.ToolResultError)
	case "notice":
		return writeSection("### Notice", strings.TrimSpace(message.Content+"\n"+message.Detail))
	case "phase":
		return writeSection("### Phase", message.Content)
	}
	return nil
}

func writeMarkdownFence(dst io.Writer, label, body string) error {
	if body == "" {
		return nil
	}
	fence := sessionexport.Fence(body)
	_, err := fmt.Fprintf(dst, "%s\n%s\n%s\n%s\n\n", label, fence, body, fence)
	return err
}

func writeStreamingExport(path string, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".reasonix-session-export-*")
	if err != nil {
		return exportOperationError("save session export", path, err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o644); err != nil {
		return exportOperationError("save session export", path, err)
	}
	if err := write(tmp); err != nil {
		return exportOperationError("save session export", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return exportOperationError("save session export", path, err)
	}
	if err := tmp.Close(); err != nil {
		return exportOperationError("save session export", path, err)
	}
	if err := fileutil.ReplaceFile(tmpPath, path); err != nil {
		return exportOperationError("save session export", path, err)
	}
	keep = true
	return nil
}
