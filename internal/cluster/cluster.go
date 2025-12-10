package cluster

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/jerkeyray/mimori/internal/utils"
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
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, peer := range c.Peers {
		host, basePort := utils.ParseHostPort(peer.Addr)
		if basePort == 0 {
			// can't parse port, mark as dead
			if peer.Alive {
				log.Printf("[cluster] peer %s seems dead (bad addr)", peer.Addr)
			}
			peer.Alive = false
			continue
		}

		// If host is empty (addr like ":4000"), assume peer is on localhost.
		if host == "" {
			host = "127.0.0.1"
		}

		httpPort := basePort + 1
		url := fmt.Sprintf("http://%s:%d/healthz", host, httpPort)

		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := http.DefaultClient.Do(req)
		cancel()

		if err == nil {
			// ensure body closed in every case
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
			if resp.StatusCode == http.StatusOK {
				if !peer.Alive {
					log.Printf("[cluster] peer %s is now alive", peer.Addr)
				}
				peer.Alive = true
				peer.LastOK = time.Now()
				continue
			}
		}

		// any error or non-200 status => dead
		if peer.Alive {
			log.Printf("[cluster] peer %s seems dead", peer.Addr)
		}
		peer.Alive = false
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
