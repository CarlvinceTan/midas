package session

import "testing"

func TestSessionCloseIsIdempotent(t *testing.T) {
	resource := &fakeResource{}
	s := &Session{resource: resource}

	if err := s.Close(); err != nil {
		t.Fatalf("first close failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}
	if resource.closed != 1 {
		t.Fatalf("expected resource close to be called once, got %d", resource.closed)
	}
}

func TestSessionContextNilSafety(t *testing.T) {
	var s *Session
	if s.Context() != nil {
		t.Fatal("expected nil context for nil session")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("expected nil close error, got %v", err)
	}
}

type fakeResource struct {
	closed int
}

func (f *fakeResource) Close() error {
	f.closed++
	return nil
}
