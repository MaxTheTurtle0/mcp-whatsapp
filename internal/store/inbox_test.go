package store

import (
	"context"
	"testing"
	"time"
)

func inboxStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func addLive(t *testing.T, s *Store, id, chat, body string, fromMe bool) {
	t.Helper()
	ctx := context.Background()
	if err := s.StoreChat(chat, "Chat "+chat, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := Message{ID: id, ChatJID: chat, Sender: "4911", Content: body, Timestamp: time.Now(), IsFromMe: fromMe}
	if err := s.StoreMessage(ctx, m, nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendInbox(ctx, id, chat); err != nil {
		t.Fatal(err)
	}
}

func TestInboxCursorFiltersAndPaging(t *testing.T) {
	s := inboxStore(t)
	ctx := context.Background()
	a, b := "4911@s.whatsapp.net", "123@g.us"

	start, _ := s.InboxCursor(ctx)
	if start != 0 {
		t.Fatalf("empty cursor = %d", start)
	}
	addLive(t, s, "m1", a, "hi", false)
	addLive(t, s, "m2", a, "mine", true)
	addLive(t, s, "m3", b, "group", false)

	page, err := s.ReadInbox(ctx, InboxQuery{After: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != "m1" || page.Messages[1].ID != "m3" || page.More {
		t.Fatalf("default read = %+v", page)
	}
	if page.Cursor != 3 {
		t.Fatalf("cursor = %d, want 3", page.Cursor)
	}

	// Own messages on request; a chat filter skips other chats for good.
	page, _ = s.ReadInbox(ctx, InboxQuery{After: 0, IncludeFromMe: true, ChatJID: a})
	if len(page.Messages) != 2 || page.Cursor != 3 {
		t.Fatalf("own+chat read = %+v", page)
	}

	// Paging: limit 1 stops at the first match and reports more.
	page, _ = s.ReadInbox(ctx, InboxQuery{After: 0, Limit: 1})
	if len(page.Messages) != 1 || !page.More || page.Cursor != 1 {
		t.Fatalf("paged read = %+v", page)
	}
	page, _ = s.ReadInbox(ctx, InboxQuery{After: page.Cursor, Limit: 1})
	if len(page.Messages) != 1 || page.Messages[0].ID != "m3" || page.More {
		t.Fatalf("second page = %+v", page)
	}

	// Nothing new: empty page, cursor unchanged.
	page, _ = s.ReadInbox(ctx, InboxQuery{After: 3})
	if len(page.Messages) != 0 || page.Cursor != 3 {
		t.Fatalf("caught-up read = %+v", page)
	}
}

func TestWaitInboxWakesOnAppendAndTimesOut(t *testing.T) {
	s := inboxStore(t)
	ctx := context.Background()
	chat := "4911@s.whatsapp.net"

	begin := time.Now()
	page, err := s.WaitInbox(ctx, InboxQuery{}, 150*time.Millisecond)
	if err != nil || len(page.Messages) != 0 || time.Since(begin) < 140*time.Millisecond {
		t.Fatalf("timeout wait = %+v, %v after %v", page, err, time.Since(begin))
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		addLive(t, s, "own", chat, "from my phone", true) // filtered: must not wake the result
		time.Sleep(100 * time.Millisecond)
		addLive(t, s, "in", chat, "hello", false)
	}()
	begin = time.Now()
	page, err = s.WaitInbox(ctx, InboxQuery{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != "in" || page.Cursor != 2 {
		t.Fatalf("woken wait = %+v", page)
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatalf("wait did not wake promptly: %v", time.Since(begin))
	}
}

func TestInboxIgnoresMessagesWithoutRow(t *testing.T) {
	s := inboxStore(t)
	ctx := context.Background()
	if err := s.AppendInbox(ctx, "ghost", "x@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadInbox(ctx, InboxQuery{})
	if err != nil || len(page.Messages) != 0 || page.Cursor != 1 {
		t.Fatalf("ghost read = %+v, %v", page, err)
	}
}
