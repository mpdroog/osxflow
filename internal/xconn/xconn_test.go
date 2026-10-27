package xconn

import (
	"errors"
	"testing"
	"time"

	"github.com/jezek/xgb"
)

func TestRetryOutlastsAStartingServer(t *testing.T) {
	calls := 0
	conn, err := retry(func() (*xgb.Conn, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("connect: resource temporarily unavailable")
		}
		return &xgb.Conn{}, nil
	}, time.Second)
	if err != nil || conn == nil {
		t.Fatalf("got %v, %v; want a connection", conn, err)
	}
	if calls != 3 {
		t.Errorf("dialled %d times, want 3", calls)
	}
}

func TestRetryGivesUp(t *testing.T) {
	refused := errors.New("no display")
	start := time.Now()
	_, err := retry(func() (*xgb.Conn, error) { return nil, refused }, 200*time.Millisecond)
	if !errors.Is(err, refused) {
		t.Fatalf("got %v, want it to wrap %v", err, refused)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("gave up after %v, want about 200ms", took)
	}
}
