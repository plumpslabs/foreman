package foreman

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/session"
)

func TestSessionDBPathIsProjectLocal(t *testing.T) {
	st := testStore(t)
	got := st.SessionDBPath()
	if !filepath.IsAbs(got) {
		t.Fatalf("expected absolute path, got %q", got)
	}
	if filepath.Base(got) != "sessions.db" {
		t.Fatalf("expected sessions.db, got %q", got)
	}
	if filepath.Base(filepath.Dir(got)) != ".foreman" {
		t.Fatalf("expected session db inside .foreman, got %q", filepath.Dir(got))
	}
}

// TestSessionServicePersistsAcrossEngines proves that a session written by one
// engine instance is visible to a fresh instance over the same store — the
// property that removes the cold start between server restarts.
func TestSessionServicePersistsAcrossEngines(t *testing.T) {
	st := testStore(t)
	e1 := NewADKEngine(st)
	svc1 := e1.sessionService()
	if svc1 == nil {
		t.Fatal("expected persistent session service")
	}
	// Lazy open must have materialized the database file.
	if _, err := os.Stat(st.SessionDBPath()); err != nil {
		t.Fatalf("session db file missing after open: %v", err)
	}

	ctx := context.Background()
	cr, err := svc1.Create(ctx, &session.CreateRequest{AppName: "foreman", UserID: "user-1", SessionID: "room-a:lead"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if cr == nil || cr.Session == nil || cr.Session.ID() != "room-a:lead" {
		t.Fatalf("unexpected create response: %+v", cr)
	}

	// A fresh engine over the same store must read the session back.
	e2 := NewADKEngine(st)
	svc2 := e2.sessionService()
	if svc2 == nil {
		t.Fatal("expected persistent session service on second engine")
	}
	gr, err := svc2.Get(ctx, &session.GetRequest{AppName: "foreman", UserID: "user-1", SessionID: "room-a:lead"})
	if err != nil {
		t.Fatalf("get session from second engine: %v", err)
	}
	if gr == nil || gr.Session == nil || gr.Session.ID() != "room-a:lead" {
		t.Fatalf("session did not persist across engine instances: %+v", gr)
	}
}

// TestSessionPersistenceSetting selects the backend: the persistent service is
// cached (same instance every call), while the disabled setting yields a fresh
// in-memory service per run — never touching the on-disk store.
func TestSessionPersistenceSetting(t *testing.T) {
	// Persistence ON (default): one cached persistent service.
	st := testStore(t)
	e := NewADKEngine(st)
	svc1, unlock1 := e.pickSessionService("conv-a")
	unlock1()
	svc2, unlock2 := e.pickSessionService("conv-a")
	unlock2()
	if svc1 == nil || svc1 != svc2 {
		t.Fatalf("expected one cached persistent service, got %T and %T", svc1, svc2)
	}
	if _, err := os.Stat(st.SessionDBPath()); err != nil {
		t.Fatalf("session db file should exist when persistence enabled: %v", err)
	}

	// Persistence OFF: fresh in-memory service per call, no database file.
	st2 := testStore(t)
	cur := st2.Settings()
	cur.DisableSessionPersistence = true
	if _, err := st2.UpdateSettings(cur); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	e2 := NewADKEngine(st2)
	a1, u1 := e2.pickSessionService("conv-b")
	u1()
	a2, u2 := e2.pickSessionService("conv-b")
	u2()
	if a1 == nil || a1 == a2 {
		t.Fatalf("expected fresh in-memory services when persistence disabled, got same %T", a1)
	}
	if _, err := os.Stat(st2.SessionDBPath()); err == nil {
		t.Fatal("session db file must not be created when persistence is disabled")
	}
}

// TestConversationLockSerializesSameThread proves that concurrent runs sharing
// one conversation ID are serialized (the persistent store rejects the second
// writer otherwise), while different conversations proceed concurrently.
func TestConversationLockSerializesSameThread(t *testing.T) {
	st := testStore(t)
	e := NewADKEngine(st)

	// Two goroutines on the same conversation: second must wait for first.
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	done := make(chan struct{})
	go func() {
		unlock := e.conversationLock("same-conv")
		close(firstEntered)
		<-releaseFirst
		unlock()
		close(done)
	}()
	<-firstEntered

	secondDone := make(chan struct{})
	go func() {
		unlock := e.conversationLock("same-conv")
		unlock()
		close(secondDone)
	}()

	select {
	case <-secondDone:
		t.Fatal("second same-conversation run must wait for the first to release")
	case <-time.After(50 * time.Millisecond):
		// expected: blocked
	}
	close(releaseFirst)
	<-done
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("second run never acquired the conversation lock after release")
	}

	// Different conversations are independent.
	var wg sync.WaitGroup
	wg.Add(2)
	blocked := make(chan struct{})
	go func() {
		defer wg.Done()
		unlock := e.conversationLock("conv-x")
		<-blocked
		unlock()
	}()
	go func() {
		defer wg.Done()
		unlock := e.conversationLock("conv-y")
		unlock()
	}()
	close(blocked)
	wg.Wait()
}
