package cluster

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// Node represents each know peer in the cluster
type Node struct {
	Addr   string
	Alive  bool
	LastOK time.Time
}

// Cluster holds info about the current Node's peers
type Cluster struct {
	SelfAddr string
	Peers    []*Node
	mu       sync.RWMutex
	stop     chan struct{}
}

// New creates a new cluster manager given this node’s address and its peers.
// filters out itself
// build slice of Nodes for other peers
// return ready to use cluster manager 
func New(selfAddr string, peers []string) *Cluster {
	nodes := make([]*Node, 0, len(peers))
	for _, addr := range peers {
		if addr == selfAddr {
			continue
		}
		nodes = append(nodes, &Node{Addr: addr})
	}
	return &Cluster{
		SelfAddr: selfAddr,
		Peers:    nodes,
		stop:     make(chan struct{}),
	}
}

// Start begins periodic heartbeat checks to all peers
// call c.pingPeers every 2 seconds until stopped
func (c *Cluster) Start() {
	ticker := time.NewTicker(2 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				c.pingPeers()
			case <-c.stop:
				ticker.Stop()
				return
			}
		}
	}()
	log.Printf("[cluster] started heartbeat routine with %d peers", len(c.Peers))
}

// Stop ends the heartbeat loop
func (c *Cluster) Stop() { close(c.stop) }

// pingPeers performs a heartbeat check on all known peers
func (c *Cluster) pingPeers() {
	// lock the mutex so no one reads from peers while they are being updated
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, peer := range c.Peers {
		// for each peer
		// build an http GET request, wrap in a 800ms timeout context
		// send request with http.DefaultClient
		// if responds with OK, mark peer alive and update LastOK
		// else mark peer dead
		ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://%s/healthz", peer.Addr), nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()

		if err == nil && resp.StatusCode == http.StatusOK {
			if !peer.Alive {
				log.Printf("[cluster] peer %s is now alive", peer.Addr)
			}
			peer.Alive = true
			peer.LastOK = time.Now()
			_ = resp.Body.Close()
		} else {
			if peer.Alive {
				log.Printf("[cluster] peer %s seems dead", peer.Addr)
			}
			peer.Alive = false
		}
	}
}

// PeersStatus returns a snapshot of the current peer states.
func (c *Cluster) PeersStatus() []Node {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]Node, len(c.Peers))
	for i, p := range c.Peers {
		out[i] = *p
	}
	return out
}
