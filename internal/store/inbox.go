package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The inbox is an append-only log of messages that arrived live (including
// the backlog WhatsApp delivers right after a reconnect), in arrival order.
// History-sync imports never enter it. Its sequence number is the cursor for
// wait_for_messages, so an agent sees every new message exactly once per
// cursor even when old history is imported later with older timestamps.
const inboxSchema = `
CREATE TABLE IF NOT EXISTS inbox (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	message_id TEXT NOT NULL,
	chat_jid TEXT NOT NULL,
	received_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS inbox_message ON inbox (message_id, chat_jid);
`

// inboxRetention bounds the log; older cursors simply resume at the oldest kept row.
const inboxRetention = 30 * 24 * time.Hour

// inboxSignal wakes waiters when a row is appended. Closing and replacing a
// channel broadcasts to every waiter without tracking them.
type inboxSignal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *inboxSignal) current() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

func (s *inboxSignal) broadcast() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		close(s.ch)
	}
	s.ch = make(chan struct{})
}

func applyInboxSchema(db *sql.DB) error {
	if _, err := db.Exec(inboxSchema); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM inbox WHERE received_at < ?`, time.Now().Add(-inboxRetention))
	return err
}

// AppendInbox records a stored live message and wakes waiters.
func (s *Store) AppendInbox(ctx context.Context, messageID, chatJID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO inbox (message_id, chat_jid, received_at) VALUES (?, ?, ?)`,
		messageID, chatJID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("append inbox: %w", err)
	}
	s.inbox.broadcast()
	return nil
}

// InboxCursor returns the newest sequence number (0 when empty).
func (s *Store) InboxCursor(ctx context.Context) (int64, error) {
	var seq sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM inbox`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("inbox cursor: %w", err)
	}
	return seq.Int64, nil
}

// InboxQuery filters an inbox read.
type InboxQuery struct {
	After         int64  // return rows with seq > After
	ChatJID       string // optional: only this chat
	IncludeFromMe bool   // include messages the account itself sent (from the phone or another device)
	Limit         int
}

// InboxPage is one read of the inbox.
type InboxPage struct {
	Messages []Message
	Cursor   int64 // pass as After next time
	More     bool  // more matching rows are already waiting
}

// ReadInbox returns matching messages after q.After in arrival order. The
// returned cursor covers every row inspected, so rows filtered out (other
// chats, own messages) are skipped for good rather than rescanned.
func (s *Store) ReadInbox(ctx context.Context, q InboxQuery) (InboxPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	maxSeq, err := s.InboxCursor(ctx)
	if err != nil {
		return InboxPage{}, err
	}
	page := InboxPage{Cursor: q.After}
	if maxSeq <= q.After {
		return page, nil
	}

	where := []string{"inbox.seq > ?", "inbox.seq <= ?"}
	args := []any{q.After, maxSeq}
	if q.ChatJID != "" {
		where = append(where, "inbox.chat_jid = ?")
		args = append(args, q.ChatJID)
	}
	if !q.IncludeFromMe {
		where = append(where, "messages.is_from_me = 0")
	}
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx,
		"SELECT inbox.seq, "+listMessageColumns+
			" FROM inbox JOIN messages ON messages.id = inbox.message_id AND messages.chat_jid = inbox.chat_jid"+
			" JOIN chats ON messages.chat_jid = chats.jid"+
			" WHERE "+strings.Join(where, " AND ")+
			" ORDER BY inbox.seq LIMIT ?", args...)
	if err != nil {
		return InboxPage{}, fmt.Errorf("read inbox: %w", err)
	}
	defer rows.Close()

	var seqs []int64
	for rows.Next() {
		var seq int64
		m, err := s.scanListMessageRow(seqScanner{rows, &seq})
		if err != nil {
			return InboxPage{}, err
		}
		seqs = append(seqs, seq)
		page.Messages = append(page.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return InboxPage{}, err
	}
	if len(page.Messages) > limit {
		page.Messages = page.Messages[:limit]
		page.Cursor = seqs[limit-1]
		page.More = true
	} else {
		page.Cursor = maxSeq
	}
	return page, nil
}

// WaitInbox blocks until ReadInbox returns at least one message, the
// timeout passes or ctx ends. A timeout is not an error: the page is empty
// and its cursor still advances past rows that did not match.
func (s *Store) WaitInbox(ctx context.Context, q InboxQuery, timeout time.Duration) (InboxPage, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		// Take the signal before reading so an append between the read and
		// the wait still wakes us.
		wake := s.inbox.current()
		page, err := s.ReadInbox(ctx, q)
		if err != nil || len(page.Messages) > 0 {
			return page, err
		}
		q.After = page.Cursor
		select {
		case <-wake:
		case <-deadline.C:
			return page, nil
		case <-ctx.Done():
			return page, nil
		}
	}
}

// seqScanner prepends the inbox seq column to the shared message projection.
type seqScanner struct {
	rows *sql.Rows
	seq  *int64
}

func (r seqScanner) Scan(dest ...any) error {
	return r.rows.Scan(append([]any{r.seq}, dest...)...)
}
