package main

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNormalizeLeaderAddr(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		leaderID string
		want     string
	}{
		{
			name:     "inherit-host-when-leaderid-has-only-port",
			base:     "127.0.0.1:4002",
			leaderID: ":4000",
			want:     "127.0.0.1:4000",
		},
		{
			name:     "inherit-host-when-leaderid-is-just-port-number",
			base:     "localhost:4002",
			leaderID: "4000",
			want:     "localhost:4000",
		},
		{
			name:     "leave-hosted-leaderid-as-is",
			base:     "127.0.0.1:4002",
			leaderID: "localhost:4000",
			// When base host is loopback, we ignore the leader host and
			// always dial baseHost:port instead.
			want: "127.0.0.1:4000",
		},
		{
			name:     "when-base-has-no-host-keep-colon-port",
			base:     ":4002",
			leaderID: ":4000",
			want:     ":4000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeLeaderAddr(tt.base, tt.leaderID); got != tt.want {
				t.Fatalf("normalizeLeaderAddr(%q, %q)=%q want %q", tt.base, tt.leaderID, got, tt.want)
			}
		})
	}
}

func TestExtractLeaderHint(t *testing.T) {
	err := status.Error(codes.FailedPrecondition, "not leader, leader=:4000 (use allow_stale=true for follower reads)")
	got, ok := extractLeaderHint(err)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if got != ":4000" {
		t.Fatalf("leader hint=%q want %q", got, ":4000")
	}
}

func TestSplitAddrList(t *testing.T) {
	got := splitAddrList(" 127.0.0.1:4002,127.0.0.1:4000 , ,localhost:4004 ")
	want := []string{"127.0.0.1:4002", "127.0.0.1:4000", "localhost:4004"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("idx %d: got %q want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

