package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sealjay/mcp-whatsapp/internal/store"
)

const (
	waitDefaultSeconds = 50
	waitMaxSeconds     = 300
	waitMaxLimit       = 50
)

type waitForMessagesArgs struct {
	Cursor         string `json:"cursor,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	ChatJID        string `json:"chat_jid,omitempty"`
	IncludeOwn     bool   `json:"include_own,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

type waitMessage struct {
	MessageID   string `json:"message_id"`
	ChatJID     string `json:"chat_jid"`
	ChatName    string `json:"chat_name,omitempty"`
	IsGroup     bool   `json:"is_group"`
	From        string `json:"from"`
	SenderJID   string `json:"sender_jid,omitempty"`
	SenderPhone string `json:"sender_phone,omitempty"`
	FromMe      bool   `json:"from_me"`
	Timestamp   string `json:"timestamp"`
	Text        string `json:"text,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
	Filename    string `json:"filename,omitempty"`
}

type waitResult struct {
	Cursor   string        `json:"cursor"`
	TimedOut bool          `json:"timed_out"`
	More     bool          `json:"more"`
	Messages []waitMessage `json:"messages"`
}

func (s *Server) registerWaitForMessages() {
	tool := mcp.NewTool("wait_for_messages",
		mcp.WithDescription("Receive new WhatsApp messages as they arrive. Blocks until at least one new message is available or the timeout passes, then returns them oldest first with a `cursor`. "+
			"Pass that cursor on the next call to get only messages after it, so nothing is missed or repeated; call in a loop to keep listening. "+
			"Without a cursor it waits for the next message from now on (use list_messages for anything older). "+
			"Only live arrivals count, including messages that arrived while the server was offline; imported history does not. "+
			"By default only messages from other people are returned. Read-only. "+
			"Returns JSON `{cursor, timed_out, more, messages:[{message_id, chat_jid, chat_name, is_group, from, sender_jid, sender_phone, from_me, timestamp, text, media_type, filename}]}`; "+
			"`more: true` means further messages are already waiting, so call again immediately. Reply with send_message (chat_jid as recipient) or send_reply."),
		mcp.WithString("cursor", mcp.Description("cursor from the previous wait_for_messages result; omit to start from now")),
		mcp.WithNumber("timeout_seconds", mcp.DefaultNumber(waitDefaultSeconds), mcp.Description(fmt.Sprintf("how long to wait for a new message (default %d, max %d); 0 returns at once", waitDefaultSeconds, waitMaxSeconds))),
		mcp.WithString("chat_jid", mcp.Description("only wait for messages in this chat ("+jidDesc+")")),
		mcp.WithBoolean("include_own", mcp.DefaultBool(false), mcp.Description("also return messages this account sent from the phone or another device (default false)")),
		mcp.WithNumber("limit", mcp.DefaultNumber(20), mcp.Description(fmt.Sprintf("max messages per call (default 20, max %d)", waitMaxLimit))),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
	)
	s.mcp.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, req mcp.CallToolRequest, a waitForMessagesArgs) (*mcp.CallToolResult, error) {
		st := s.client.Store()

		var after int64
		if c := strings.TrimSpace(a.Cursor); c != "" {
			v, err := strconv.ParseInt(c, 10, 64)
			if err != nil || v < 0 {
				return mcp.NewToolResultError("invalid cursor: pass the cursor string from a previous wait_for_messages result, or omit it"), nil
			}
			after = v
		} else {
			cur, err := st.InboxCursor(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			after = cur
		}

		timeout := waitDefaultSeconds
		if _, set := req.GetArguments()["timeout_seconds"]; set {
			timeout = a.TimeoutSeconds
		}
		timeout = max(0, min(timeout, waitMaxSeconds))
		limit := a.Limit
		if limit <= 0 {
			limit = 20
		}
		limit = min(limit, waitMaxLimit)

		chatJID := ""
		if a.ChatJID != "" {
			chatJID = normalizeRecipientToChatJID(a.ChatJID)
			if chatJID == "" {
				return mcp.NewToolResultError("invalid chat_jid: " + jidDesc), nil
			}
		}

		q := store.InboxQuery{After: after, ChatJID: chatJID, IncludeFromMe: a.IncludeOwn, Limit: limit}
		page, err := st.WaitInbox(ctx, q, time.Duration(timeout)*time.Second)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		out := waitResult{Cursor: strconv.FormatInt(page.Cursor, 10), More: page.More, Messages: []waitMessage{}}
		out.TimedOut = len(page.Messages) == 0
		for _, m := range page.Messages {
			from := "Me"
			if !m.IsFromMe {
				from = st.GetSenderName(ctx, m.Sender)
			}
			wm := waitMessage{
				MessageID: m.ID, ChatJID: m.ChatJID, ChatName: m.ChatName, IsGroup: m.IsGroup,
				From: from, FromMe: m.IsFromMe, SenderPhone: m.SenderPhone,
				Timestamp: m.Timestamp.UTC().Format(time.RFC3339),
				Text:      m.Content, MediaType: m.MediaType, Filename: m.Filename,
			}
			if !m.IsFromMe && m.Sender != "" {
				wm.SenderJID = m.Sender
			}
			out.Messages = append(out.Messages, wm)
		}
		return resultJSON(out)
	}))
}
