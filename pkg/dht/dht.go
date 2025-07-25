// Package dht provides DHT server functionality for CrowdLlama.
package dht

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/pkg/peermanager"
)

// DefaultListenAddrs is the default listen addresses for the DHT server
var DefaultListenAddrs = []string{
	"/ip4/0.0.0.0/tcp/9000",
	"/ip4/0.0.0.0/udp/9000/quic-v1",
}

// Server represents a DHT server node
type Server struct {
	Host        host.Host
	DHT         *dht.IpfsDHT
	logger      *zap.Logger
	ctx         context.Context
	cancel      context.CancelFunc
	peerManager *peermanager.Manager
	peerAddrs   []string
}

// NewDHTServer creates a new DHT server instance
func NewDHTServer(ctx context.Context, privKey crypto.PrivKey, logger *zap.Logger) (*Server, error) {
	return NewDHTServerWithAddrs(ctx, privKey, logger, DefaultListenAddrs)
}

// NewDHTServerWithAddrs creates a new DHT server instance with custom listen addresses
func NewDHTServerWithAddrs(ctx context.Context, privKey crypto.PrivKey, logger *zap.Logger, listenAddrs []string) (*Server, error) {
	ctx, cancel := context.WithCancel(ctx)

	// Use default addresses if none provided
	if len(listenAddrs) == 0 {
		listenAddrs = DefaultListenAddrs
	}

	h, err := createLibp2pHost(ctx, privKey, listenAddrs)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	kadDHT, err := createDHT(ctx, h)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create DHT: %w", err)
	}

	peerConfig := getPeerManagerConfig()
	server := &Server{
		Host:        h,
		DHT:         kadDHT,
		logger:      logger,
		ctx:         ctx,
		cancel:      cancel,
		peerManager: peermanager.NewManager(ctx, h, kadDHT, logger, peerConfig),
	}

	server.peerAddrs = generatePeerAddrs(h)
	if len(server.peerAddrs) == 0 {
		logger.Warn("No peer addresses generated, this may indicate a configuration issue")
	}

	h.Network().Notify(&network.NotifyBundle{
		ConnectedF:    server.handlePeerConnected,
		DisconnectedF: server.handlePeerDisconnected,
	})

	return server, nil
}

func createLibp2pHost(_ context.Context, privKey crypto.PrivKey, listenAddrs []string) (host.Host, error) {
	libp2pOpts := []libp2p.Option{
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.DefaultTransports,
		libp2p.DefaultMuxers,
		libp2p.DefaultSecurity,
		libp2p.NATPortMap(),
	}
	h, err := libp2p.New(libp2pOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}
	return h, nil
}

func createDHT(ctx context.Context, h host.Host) (*dht.IpfsDHT, error) {
	dhtInstance, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, fmt.Errorf("failed to create DHT: %w", err)
	}
	return dhtInstance, nil
}

func getPeerManagerConfig() *peermanager.Config {
	if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
		return &peermanager.Config{
			DiscoveryInterval:      2 * time.Second,
			AdvertisingInterval:    5 * time.Second,
			MetadataUpdateInterval: 5 * time.Second,
			PeerHealthConfig: &peermanager.PeerHealthConfig{
				StalePeerTimeout:    30 * time.Second,
				HealthCheckInterval: 5 * time.Second,
				MaxFailedAttempts:   2,
				BackoffBase:         5 * time.Second,
				MetadataTimeout:     2 * time.Second,
				MaxMetadataAge:      30 * time.Second,
			},
		}
	}
	return peermanager.DefaultConfig()
}

func generatePeerAddrs(h host.Host) []string {
	addrs := make([]string, 0, len(h.Addrs()))
	for _, addr := range h.Addrs() {
		fullAddr := fmt.Sprintf("%s/p2p/%s", addr.String(), h.ID().String())
		addrs = append(addrs, fullAddr)
	}
	return addrs
}

// Start starts the DHT server and returns the primary peer address
func (s *Server) Start() (string, error) {
	// Set up network notifier to detect new connections and NAT traversal
	s.Host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			peerID := conn.RemotePeer().String()
			remoteAddr := conn.RemoteMultiaddr().String()
			direction := conn.Stat().Direction.String()

			// Log connection details
			s.logger.Debug("New peer connected",
				zap.String("peer_id", peerID),
				zap.String("remote_addr", remoteAddr),
				zap.String("direction", direction),
				zap.String("transport", conn.RemoteMultiaddr().Protocols()[0].Name),
				zap.Bool("is_relay", isRelayConnection(remoteAddr)),
				zap.Bool("is_hole_punched", s.isHolePunchedConnection(remoteAddr, direction)))

			// Log NAT traversal information
			if isRelayConnection(remoteAddr) {
				s.logger.Debug("Connection established via relay (NAT traversal)",
					zap.String("peer_id", peerID),
					zap.String("relay_addr", remoteAddr))
			} else if s.isHolePunchedConnection(remoteAddr, direction) {
				s.logger.Debug("Direct connection established (hole punching successful)",
					zap.String("peer_id", peerID),
					zap.String("direct_addr", remoteAddr))
			} else {
				s.logger.Debug("Direct connection established (no NAT)",
					zap.String("peer_id", peerID),
					zap.String("direct_addr", remoteAddr))
			}
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			s.logger.Info("Peer disconnected",
				zap.String("peer_id", conn.RemotePeer().String()),
				zap.String("remote_addr", conn.RemoteMultiaddr().String()))
		},
		ListenF: func(_ network.Network, addr multiaddr.Multiaddr) {
			s.logger.Debug("Started listening on address",
				zap.String("listen_addr", addr.String()))
		},
		ListenCloseF: func(_ network.Network, addr multiaddr.Multiaddr) {
			s.logger.Debug("Stopped listening on address",
				zap.String("listen_addr", addr.String()))
		},
	})

	// Start the peer manager
	s.peerManager.Start()

	// Start periodic logging
	go s.startPeriodicLogging()

	s.logger.Info("Bootstrapping DHT network")
	if err := s.DHT.Bootstrap(s.ctx); err != nil {
		return "", fmt.Errorf("failed to bootstrap DHT: %w", err)
	}

	// Return the first peer address (usually the most accessible one)
	if len(s.peerAddrs) > 0 {
		return s.peerAddrs[0], nil
	}
	return "", fmt.Errorf("no peer addresses available")
}

// Stop stops the DHT server
func (s *Server) Stop() {
	s.logger.Info("Stopping DHT server...")
	s.peerManager.Stop()
	s.cancel()
	if err := s.Host.Close(); err != nil {
		s.logger.Error("Failed to close host", zap.Error(err))
	}
}

// GetPeerID returns the DHT server's peer ID
func (s *Server) GetPeerID() string {
	return s.Host.ID().String()
}

// GetPeerAddrs returns all peer addresses in the required format
func (s *Server) GetPeerAddrs() []string {
	return s.peerAddrs
}

// GetPrimaryPeerAddr returns the primary peer address (first in the list)
func (s *Server) GetPrimaryPeerAddr() string {
	if len(s.peerAddrs) > 0 {
		return s.peerAddrs[0]
	}
	return ""
}

// GetPeers returns all connected peer IDs
func (s *Server) GetPeers() []string {
	peers := s.Host.Network().Peers()
	peerIDs := make([]string, 0, len(peers))
	for _, p := range peers {
		peerIDs = append(peerIDs, p.String())
	}
	return peerIDs
}

// HasPeer checks if a specific peer ID is connected
func (s *Server) HasPeer(peerID string) bool {
	peers := s.Host.Network().Peers()
	for _, p := range peers {
		if p.String() == peerID {
			return true
		}
	}
	return false
}

// GetConnectedPeersCount returns the number of connected peers
func (s *Server) GetConnectedPeersCount() int {
	return len(s.Host.Network().Peers())
}

// GetHealthyPeers returns the peer manager's healthy peers
func (s *Server) GetHealthyPeers() map[string]*peermanager.PeerInfo {
	return s.peerManager.GetHealthyPeers()
}

// CheckProvider checks if a specific peer is providing a specific CID
func (s *Server) CheckProvider(ctx context.Context, peerID peer.ID, c cid.Cid) bool {
	providers := s.DHT.FindProvidersAsync(ctx, c, 10)
	for provider := range providers {
		if provider.ID == peerID {
			return true
		}
	}
	return false
}

// GetNATStatus returns information about NAT traversal and connection types
func (s *Server) GetNATStatus() map[string]interface{} {
	connections := s.Host.Network().Conns()

	stats := map[string]interface{}{
		"total_connections":        len(connections),
		"direct_connections":       0,
		"relay_connections":        0,
		"hole_punched_connections": 0,
		"local_connections":        0,
		"external_connections":     0,
	}

	for _, conn := range connections {
		addr := conn.RemoteMultiaddr().String()
		direction := conn.Stat().Direction.String()

		if isRelayConnection(addr) {
			stats["relay_connections"] = stats["relay_connections"].(int) + 1
		} else if s.isHolePunchedConnection(addr, direction) {
			stats["hole_punched_connections"] = stats["hole_punched_connections"].(int) + 1
			stats["external_connections"] = stats["external_connections"].(int) + 1
		} else if isExternalIP(addr) {
			stats["direct_connections"] = stats["direct_connections"].(int) + 1
			stats["external_connections"] = stats["external_connections"].(int) + 1
		} else {
			stats["local_connections"] = stats["local_connections"].(int) + 1
		}
	}

	return stats
}

// LogNATStatus logs current NAT traversal statistics
func (s *Server) LogNATStatus() {
	stats := s.GetNATStatus()
	s.logger.Debug("NAT traversal statistics",
		zap.Int("total_connections", stats["total_connections"].(int)),
		zap.Int("direct_connections", stats["direct_connections"].(int)),
		zap.Int("relay_connections", stats["relay_connections"].(int)),
		zap.Int("hole_punched_connections", stats["hole_punched_connections"].(int)),
		zap.Int("local_connections", stats["local_connections"].(int)),
		zap.Int("external_connections", stats["external_connections"].(int)))
}

// LogPeerStats logs current peer statistics
func (s *Server) LogPeerStats() {
	healthyPeers := s.peerManager.GetHealthyPeers()
	totalPeers := len(healthyPeers)
	workerPeers := 0
	consumerPeers := 0

	for _, peerInfo := range healthyPeers {
		if peerInfo.Metadata != nil {
			if peerInfo.Metadata.WorkerMode {
				workerPeers++
			} else {
				consumerPeers++
			}
		}
	}

	s.logger.Info("Peer statistics",
		zap.Int("total_peers", totalPeers),
		zap.Int("worker_peers", workerPeers),
		zap.Int("consumer_peers", consumerPeers))
}

// handlePeerConnected handles when a peer connects
func (s *Server) handlePeerConnected(_ network.Network, conn network.Conn) {
	peerID := conn.RemotePeer().String()
	remoteAddr := conn.RemoteMultiaddr().String()
	direction := conn.Stat().Direction.String()
	transport := conn.RemoteMultiaddr().Protocols()[0].Name

	s.logger.Debug("New peer connected",
		zap.String("peer_id", peerID),
		zap.String("remote_addr", remoteAddr),
		zap.String("direction", direction),
		zap.String("transport", transport),
		zap.Bool("is_relay", strings.Contains(remoteAddr, "/p2p-circuit/")),
		zap.Bool("is_hole_punched", s.isHolePunchedConnection(remoteAddr, direction)))

	// Check if this is a direct connection (not relay)
	if !strings.Contains(remoteAddr, "/p2p-circuit/") {
		s.logger.Debug("Direct connection established (no NAT)",
			zap.String("peer_id", peerID),
			zap.String("direct_addr", remoteAddr))
	}
}

// handlePeerDisconnected handles when a peer disconnects
func (s *Server) handlePeerDisconnected(_ network.Network, conn network.Conn) {
	peerID := conn.RemotePeer().String()
	remoteAddr := conn.RemoteMultiaddr().String()

	s.logger.Info("Peer disconnected",
		zap.String("peer_id", peerID),
		zap.String("remote_addr", remoteAddr))

	// Immediately remove the peer from the peer manager when disconnection is detected
	// This provides faster cleanup than waiting for health checks
	s.peerManager.RemovePeer(peerID)
	s.logger.Info("Removed peer from manager due to disconnection",
		zap.String("peer_id", peerID))
}

// isRelayConnection checks if a connection is going through a relay
func isRelayConnection(addr string) bool {
	return strings.Contains(addr, "/p2p-circuit/")
}

// isHolePunchedConnection determines if a connection was established through hole punching
func (s *Server) isHolePunchedConnection(remoteAddr, direction string) bool {
	// This is a simplified heuristic - in a real implementation you might track
	// connection establishment events more precisely
	return direction == "Outbound" && !strings.Contains(remoteAddr, "/p2p-circuit/")
}

// startPeriodicLogging starts periodic logging of NAT status and peer statistics
func (s *Server) startPeriodicLogging() {
	natLogInterval := 30 * time.Second
	if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
		natLogInterval = 10 * time.Second
	}
	natTicker := time.NewTicker(natLogInterval)
	defer natTicker.Stop()

	statsLogInterval := 15 * time.Second
	if os.Getenv("CROWDLLAMA_TEST_MODE") == "1" {
		statsLogInterval = 5 * time.Second
	}
	statsTicker := time.NewTicker(statsLogInterval)
	defer statsTicker.Stop()

	for {
		select {
		case <-natTicker.C:
			s.LogNATStatus()
		case <-statsTicker.C:
			s.LogPeerStats()
		case <-s.ctx.Done():
			return
		}
	}
}

// isExternalIP checks if an address is from an external IP
func isExternalIP(addr string) bool {
	// Extract IP from multiaddr
	if strings.Contains(addr, "/ip4/127.0.0.1/") ||
		strings.Contains(addr, "/ip4/192.168.") ||
		strings.Contains(addr, "/ip4/10.") ||
		strings.Contains(addr, "/ip4/172.") ||
		strings.Contains(addr, "/ip6/::1/") ||
		strings.Contains(addr, "/ip6/fe80:") {
		return false
	}
	return true
}
