package qq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"reasonix/internal/bot"
	"reasonix/internal/config"
)

type keyboardSendRequest struct {
	MsgType  int    `json:"msg_type"`
	Content  string `json:"content"`
	MsgID    string `json:"msg_id"`
	MsgSeq   int    `json:"msg_seq"`
	Markdown *struct {
		Content string `json:"content"`
	} `json:"markdown"`
	Keyboard *struct {
		Content struct {
			Rows []struct {
				Buttons []struct {
					ID         string `json:"id"`
					RenderData struct {
						Label string `json:"label"`
						Style int    `json:"style"`
					} `json:"render_data"`
					Action struct {
						Type       int    `json:"type"`
						Data       string `json:"data"`
						Enter      bool   `json:"enter"`
						Permission *struct {
							Type int `json:"type"`
						} `json:"permission"`
					} `json:"action"`
				} `json:"buttons"`
			} `json:"rows"`
		} `json:"content"`
	} `json:"keyboard"`
}

func keyboardTestAdapter(t *testing.T, send func(*http.Request) (*http.Response, error)) *adapter {
	t.Helper()
	original := qqHTTPClient
	qqHTTPClient = &http.Client{Transport: roundTripFunc(send)}
	t.Cleanup(func() { qqHTTPClient = original })
	return &adapter{
		cfg:         config.QQBotConfig{AppID: "app-id"},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		token:       "test-token",
		tokenExpiry: time.Now().Add(time.Hour),
	}
}

func keyboardTestMessage(chatType bot.ChatType) bot.OutboundMessage {
	return bot.OutboundMessage{
		ChatType: chatType, ChatID: "chat-1", ReplyToMsgID: "incoming-1",
		Text: "需要批准操作：echo hello\n回复 1 批准，2 拒绝；/approve approval-1 或 /deny approval-1。",
		Keyboard: &bot.InlineKeyboard{Rows: []bot.InlineKeyboardRow{
			{Buttons: []bot.InlineKeyboardButton{{ID: " allow_once ", Label: "允许一次", Style: 1, CallbackID: "/approve approval-1"}}},
			{Buttons: []bot.InlineKeyboardButton{{ID: "deny", Label: "拒绝", Style: 2, CallbackID: "/deny approval-1"}}},
		}},
	}
}

func TestSendApprovalKeyboardMatchesQQProtocol(t *testing.T) {
	for _, chatType := range []bot.ChatType{bot.ChatDM, bot.ChatGroup} {
		t.Run(string(chatType), func(t *testing.T) {
			msg := keyboardTestMessage(chatType)
			var body keyboardSendRequest
			a := keyboardTestAdapter(t, func(req *http.Request) (*http.Response, error) {
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatalf("decode QQ request: %v", err)
				}
				return jsonResponse(http.StatusOK, map[string]string{"id": "approval-sent"}), nil
			})
			result, err := a.Send(context.Background(), msg)
			if err != nil || result.MessageID != "approval-sent" {
				t.Fatalf("Send() = %#v, %v", result, err)
			}
			if body.MsgType != 2 || body.Content != "" || body.Markdown == nil || body.Markdown.Content != msg.Text {
				t.Fatalf("expected Markdown body without top-level content: %#v", body)
			}
			if body.MsgID != msg.ReplyToMsgID || body.MsgSeq != 1 {
				t.Fatalf("reply identity = %q/%d", body.MsgID, body.MsgSeq)
			}
			if body.Keyboard == nil || len(body.Keyboard.Content.Rows) != 2 {
				t.Fatalf("expected keyboard.content.rows: %#v", body.Keyboard)
			}
			for i, want := range []struct {
				id, label, command string
				style              int
			}{{"allow_once", "允许一次", "/approve approval-1", 1}, {"deny", "拒绝", "/deny approval-1", 3}} {
				buttons := body.Keyboard.Content.Rows[i].Buttons
				if len(buttons) != 1 {
					t.Fatalf("row %d has %d buttons", i, len(buttons))
				}
				button := buttons[0]
				if button.ID != want.id || button.RenderData.Label != want.label || button.RenderData.Style != want.style {
					t.Fatalf("button %d rendering = %#v", i, button)
				}
				if button.Action.Type != 2 || button.Action.Data != want.command || button.Action.Permission == nil || button.Action.Permission.Type != 2 {
					t.Fatalf("button %d command action = %#v", i, button.Action)
				}
				if button.Action.Enter != (chatType == bot.ChatDM) {
					t.Fatalf("button %d enter = %v for %s", i, button.Action.Enter, chatType)
				}
			}
		})
	}
}

func TestSendApprovalKeyboardFailureFallsBackWithoutDisablingMarkdown(t *testing.T) {
	var bodies []keyboardSendRequest
	a := keyboardTestAdapter(t, func(req *http.Request) (*http.Response, error) {
		var body keyboardSendRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			return jsonResponse(http.StatusInternalServerError, map[string]any{"message": "系统繁忙，请稍后重试", "code": 50015001}), nil
		}
		return jsonResponse(http.StatusOK, map[string]string{"id": fmt.Sprintf("sent-%d", len(bodies))}), nil
	})
	msg := keyboardTestMessage(bot.ChatDM)
	result, err := a.Send(context.Background(), msg)
	if err != nil || result.MessageID != "sent-2" {
		t.Fatalf("approval Send() = %#v, %v", result, err)
	}
	msg.Keyboard = nil
	if _, err := a.Send(context.Background(), msg); err != nil {
		t.Fatalf("ordinary Send(): %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want rich approval, text fallback, ordinary Markdown", len(bodies))
	}
	plain := bodies[1]
	if plain.MsgType != 0 || plain.Content != msg.Text || plain.Keyboard != nil || plain.Markdown != nil {
		t.Fatalf("fallback lost approval instructions or retained rich fields: %#v", plain)
	}
	if plain.MsgID != msg.ReplyToMsgID || plain.MsgSeq != 2 || bodies[2].MsgSeq != 3 || bodies[2].Markdown == nil {
		t.Fatalf("fallback reply identity or later Markdown delivery = %#v", bodies)
	}
}

func TestSendApprovalKeyboardFallbackPreservesDeliveredChunks(t *testing.T) {
	var sequences []int
	a := keyboardTestAdapter(t, func(req *http.Request) (*http.Response, error) {
		var body keyboardSendRequest
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, body.MsgSeq)
		if len(sequences) == 2 {
			return jsonResponse(http.StatusInternalServerError, map[string]int{"code": 50015001}), nil
		}
		return jsonResponse(http.StatusOK, map[string]string{"id": fmt.Sprintf("sent-%d", len(sequences))}), nil
	})
	msg := keyboardTestMessage(bot.ChatDM)
	msg.Text = strings.Repeat("a", qqMaxChunkBytes) + "\n/approve approval-1 or /deny approval-1"
	result, err := a.Send(context.Background(), msg)
	if err != nil {
		t.Fatalf("Send(): %v", err)
	}
	if !slices.Equal(sequences, []int{1, 2, 3}) || !slices.Equal(result.DeliveredMessageIDs(), []string{"sent-1", "sent-3"}) {
		t.Fatalf("sequences = %v, delivered = %v", sequences, result.DeliveredMessageIDs())
	}
}

func TestSendApprovalKeyboardFallbackFailureReturnsError(t *testing.T) {
	calls := 0
	a := keyboardTestAdapter(t, func(req *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(http.StatusInternalServerError, map[string]int{"code": 50015001}), nil
	})
	result, err := a.Send(context.Background(), keyboardTestMessage(bot.ChatDM))
	if err == nil || calls != 2 || len(result.DeliveredMessageIDs()) != 0 {
		t.Fatalf("Send() = %#v, %v after %d requests", result, err, calls)
	}
}

func TestSendApprovalKeyboardCancellationDoesNotSendFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	a := keyboardTestAdapter(t, func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return nil, context.Canceled
	})
	_, err := a.Send(ctx, keyboardTestMessage(bot.ChatDM))
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Send() error = %v after %d requests", err, calls)
	}
}
