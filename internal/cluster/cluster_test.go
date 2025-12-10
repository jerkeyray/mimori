package cluster

import (
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestCluster_HealthCheck(t *testing.T) {
	// Start a mock HTTP server for a peer
	// We need to control the port.
	
	// Let's find a free port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	httpPort := addr.Port
	// peer address must be httpPort - 1
	peerPort := httpPort - 1
	peerAddr := fmt.Sprintf("127.0.0.1:%d", peerPort)

	// Handler that can be toggled
	var alive bool = true
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if alive {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})

	server := &http.Server{Handler: mux}
	go server.Serve(l)
	defer server.Close()

	// Create Cluster
	// Self address doesn't matter much here
	c := New("127.0.0.1:9000", []string{peerAddr})
	
	// Start monitoring
	// We can't easily wait for the ticker in Start(), so we might want to just call pingPeers directly
	// but pingPeers is private. 
	// The user prompt says "Mock HTTP health endpoints to test: peer marked alive, peer marked dead".
	// Since pingPeers is private, I can't call it from a test in a different package (if I used package cluster_test).
	// But I am using package cluster.

	// Initial check - alive
	c.pingPeers()
	status := c.PeersStatus()
	if len(status) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(status))
	}
	if !status[0].Alive {
		t.Errorf("expected peer to be alive")
	}

	// Mark dead
	alive = false
	c.pingPeers()
	status = c.PeersStatus()
	if status[0].Alive {
		t.Errorf("expected peer to be dead")
	}

	// Mark alive again
	alive = true
	c.pingPeers()
	status = c.PeersStatus()
	if !status[0].Alive {
		t.Errorf("expected peer to be alive again")
	}
}

func TestCluster_StartStop(t *testing.T) {
	c := New("127.0.0.1:8000", []string{"127.0.0.1:8002"})
	c.Start()
	time.Sleep(10 * time.Millisecond) // let it spin up
	c.Stop()
	// Just ensure no panic/deadlock
}

