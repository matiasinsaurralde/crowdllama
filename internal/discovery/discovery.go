// Package discovery provides peer and DHT discovery utilities for CrowdLlama.
package discovery

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
	"go.uber.org/zap"

	"github.com/crowdllama/crowdllama/pkg/crowdllama"
)

// advertiseInterval is the interval at which the model is advertised to the DHT:
var advertiseInterval = 10 * time.Second

// SetTestMode enables test mode with shorter intervals
func SetTestMode() {
	advertiseInterval = 2 * time.Second
}

// GetAdvertiseInterval returns the current advertisement interval
func GetAdvertiseInterval() time.Duration {
	return advertiseInterval
}

var defaultListenAddrs = []string{"/ip4/0.0.0.0/tcp/0"}

const (
	// defaultBootstrapPeerAddr is the default bootstrap peer address for the DHT:
	// defaultBootstrapPeerAddr = "/dns4/dht.crowdllama.org/tcp/9000/p2p/12D3KooWGtAsTBuXFJrywcneqUYsGLD6ym9en2uqc56g4fMySVcK"
	defaultBootstrapPeerAddr = "/ip4/127.0.0.1/tcp/9000/p2p/12D3KooWLLUBEZhkEq6NtTLD99RRpEYdcbe8uzx3L56UgF5VK4bw"
)

// NewHostAndDHT creates a libp2p host with DHT
func NewHostAndDHT(ctx context.Context, privKey crypto.PrivKey, _ *zap.Logger) (host.Host, *dht.IpfsDHT, error) {
	libp2pOpts := []libp2p.Option{
		libp2p.ListenAddrStrings(defaultListenAddrs...),
		libp2p.Identity(privKey),
	}
	if os.Getenv("CROWDLLAMA_TEST_MODE") != "1" {
		// Use static relays for auto-relay functionality
		staticRelays := []peer.AddrInfo{
			// Add some well-known libp2p relays here
			// For now, we'll disable auto-relay to avoid the error
			// TODO: Add proper static relays when needed
		}

		libp2pOpts = append(libp2pOpts,
			libp2p.EnableHolePunching(),
		)

		// Only enable auto-relay if we have static relays
		if len(staticRelays) > 0 {
			libp2pOpts = append(libp2pOpts,
				libp2p.EnableAutoRelayWithStaticRelays(staticRelays),
			)
		}
	}

	h, err := libp2p.New(libp2pOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create libp2p host: %w", err)
	}

	kadDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, nil, fmt.Errorf("create DHT instance: %w", err)
	}

	return h, kadDHT, nil
}

// BootstrapDHT connects to bootstrap peers. If customPeers is nil, use a local bootstrap address for fast local discovery.
func BootstrapDHT(ctx context.Context, h host.Host, kadDHT *dht.IpfsDHT, logger *zap.Logger) error {
	return BootstrapDHTWithPeers(ctx, h, kadDHT, nil, logger)
}

// BootstrapDHTWithPeers connects to custom bootstrap peers. If customPeers is nil or empty, use defaults.
func BootstrapDHTWithPeers(ctx context.Context, h host.Host, kadDHT *dht.IpfsDHT, customPeers []string, logger *zap.Logger) error {
	var bootstrapPeers []peer.AddrInfo

	if len(customPeers) > 0 {
		// Use custom bootstrap peers
		for _, peerAddr := range customPeers {
			addr, err := multiaddr.NewMultiaddr(peerAddr)
			if err != nil {
				logger.Debug("Failed to parse custom bootstrap peer address", zap.String("peer_addr", peerAddr), zap.Error(err))
				continue
			}
			peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
			if err != nil {
				logger.Debug("Failed to parse custom bootstrap peer info", zap.String("peer_addr", peerAddr), zap.Error(err))
				continue
			}
			bootstrapPeers = append(bootstrapPeers, *peerInfo)
		}
	}

	// If no custom peers or all failed to parse, fallback to default
	if len(bootstrapPeers) == 0 {
		addr, err := multiaddr.NewMultiaddr(defaultBootstrapPeerAddr)
		if err == nil {
			peerInfo, err := peer.AddrInfoFromP2pAddr(addr)
			if err != nil {
				logger.Debug("Failed to parse bootstrap peer info", zap.Error(err))
				// fallback to default public bootstrap peers
				bootstrapPeers = dht.GetDefaultBootstrapPeerAddrInfos()
			} else {
				bootstrapPeers = []peer.AddrInfo{*peerInfo}
			}
		} else {
			// fallback to default public bootstrap peers
			bootstrapPeers = dht.GetDefaultBootstrapPeerAddrInfos()
		}
	}

	for _, peerInfo := range bootstrapPeers {
		if err := h.Connect(ctx, peerInfo); err != nil {
			logger.Debug("Failed to connect to bootstrap", zap.String("peer_id", peerInfo.ID.String()), zap.Error(err))
		} else {
			logger.Debug("Connected to bootstrap", zap.String("peer_id", peerInfo.ID.String()))
		}
	}
	if err := kadDHT.Bootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap DHT: %w", err)
	}
	return nil
}

// AdvertiseModel periodically announces model availability
func AdvertiseModel(ctx context.Context, kadDHT *dht.IpfsDHT, namespace string, logger *zap.Logger) {
	ticker := time.NewTicker(advertiseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c, err := cid.Parse(namespace)
			if err != nil {
				logger.Debug("Failed to parse namespace as CID", zap.Error(err))
				continue
			}
			err = kadDHT.Provide(ctx, c, true)
			if err != nil {
				logger.Debug("Failed to advertise model", zap.Error(err))
			} else {
				logger.Debug("Model advertised successfully")
			}
		case <-ctx.Done():
			return
		}
	}
}

// WaitForShutdown handles termination signals
func WaitForShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}

// GetPeerNamespaceCID generates the CID for peer discovery namespace
func GetPeerNamespaceCID() (cid.Cid, error) {
	namespace := crowdllama.PeerNamespace
	mh, err := multihash.Sum([]byte(namespace), multihash.IDENTITY, -1)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to create multihash for namespace: %w", err)
	}
	return cid.NewCidV1(cid.Raw, mh), nil
}

// readMetadataStream reads all data from the stream until EOF
func readMetadataStream(stream network.Stream, peerID peer.ID, logger *zap.Logger) ([]byte, error) {
	var metadataJSON []byte
	buf := make([]byte, 1024)
	totalRead := 0

	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			metadataJSON = append(metadataJSON, buf[:n]...)
			totalRead += n
			logger.Debug("Read bytes from metadata stream",
				zap.String("peer_id", peerID.String()),
				zap.Int("bytes_read", n),
				zap.Int("total_read", totalRead))
		}
		if readErr != nil {
			if readErr.Error() == "EOF" {
				logger.Debug("Received EOF from metadata stream",
					zap.String("peer_id", peerID.String()),
					zap.Int("total_bytes_read", totalRead))
				break // EOF reached, we're done reading
			}
			logger.Error("Failed to read metadata from peer",
				zap.String("peer_id", peerID.String()),
				zap.Error(readErr))
			return nil, fmt.Errorf("failed to read metadata from peer: %w", readErr)
		}
	}

	if len(metadataJSON) == 0 {
		return nil, fmt.Errorf("no metadata received from peer")
	}

	return metadataJSON, nil
}

// RequestPeerMetadata retrieves metadata from a peer using the metadata protocol
func RequestPeerMetadata(ctx context.Context, h host.Host, peerID peer.ID, logger *zap.Logger) (*crowdllama.Resource, error) {
	logger.Debug("Opening stream to peer for metadata request",
		zap.String("peer_id", peerID.String()),
		zap.String("protocol", crowdllama.MetadataProtocol))

	// Open a stream to the peer
	stream, err := h.NewStream(ctx, peerID, crowdllama.MetadataProtocol)
	if err != nil {
		logger.Error("Failed to open stream to peer",
			zap.String("peer_id", peerID.String()),
			zap.Error(err))
		return nil, fmt.Errorf("failed to open stream to peer: %w", err)
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			logger.Warn("failed to close stream", zap.Error(closeErr))
		}
	}()

	if setDeadlineErr := stream.SetReadDeadline(time.Now().Add(5 * time.Second)); setDeadlineErr != nil {
		logger.Warn("failed to set read deadline", zap.Error(setDeadlineErr))
	}

	logger.Debug("Reading metadata response from peer",
		zap.String("peer_id", peerID.String()))

	// Read the metadata response
	metadataJSON, err := readMetadataStream(stream, peerID, logger)
	if err != nil {
		return nil, err
	}

	logger.Debug("Parsing metadata response",
		zap.String("peer_id", peerID.String()),
		zap.Int("metadata_length", len(metadataJSON)))

	// Parse the metadata
	metadata, err := crowdllama.FromJSON(metadataJSON)
	if err != nil {
		logger.Error("Failed to parse metadata from peer",
			zap.String("peer_id", peerID.String()),
			zap.Error(err))
		return nil, fmt.Errorf("failed to parse metadata from peer: %w", err)
	}

	logger.Debug("Successfully retrieved metadata from peer",
		zap.String("peer_id", peerID.String()),
		zap.String("gpu_model", metadata.GPUModel),
		zap.Int("vram_gb", metadata.VRAMGB),
		zap.Float64("tokens_throughput", metadata.TokensThroughput))

	return metadata, nil
}

// processProvider handles a single provider from the DHT discovery
func processProvider(
	ctx context.Context,
	provider peer.AddrInfo,
	kadDHT *dht.IpfsDHT,
	logger *zap.Logger,
	peerManager interface {
		MarkPeerAsRecentlyRemoved(string)
		IsPeerUnhealthy(string) bool
	},
) *crowdllama.Resource {
	peerID := provider.ID.String()
	logger.Debug("Found peer provider", zap.String("peer_id", peerID))

	// Check if this peer is already marked as unhealthy or recently removed
	if peerManager != nil && peerManager.IsPeerUnhealthy(peerID) {
		logger.Debug("Skipping peer that is already marked as unhealthy",
			zap.String("peer_id", peerID))
		return nil
	}

	// Give the peer a moment to set up handlers
	time.Sleep(100 * time.Millisecond)

	// Request metadata from the peer
	metadata, err := RequestPeerMetadata(ctx, kadDHT.Host(), provider.ID, logger)
	if err != nil {
		logger.Debug("Failed to get metadata from peer, skipping",
			zap.String("peer_id", peerID),
			zap.Error(err))

		// Mark the peer as recently removed to prevent repeated connection attempts
		if peerManager != nil {
			peerManager.MarkPeerAsRecentlyRemoved(peerID)
		}
		return nil
	}

	// Verify the metadata is recent (within last hour)
	if time.Since(metadata.LastUpdated) > 1*time.Hour {
		logger.Debug("Metadata from peer is too old, skipping",
			zap.String("peer_id", peerID),
			zap.Time("last_updated", metadata.LastUpdated))
		return nil
	}

	logger.Debug("Found peer",
		zap.String("peer_id", peerID),
		zap.String("gpu_model", metadata.GPUModel),
		zap.Strings("supported_models", metadata.SupportedModels))

	return metadata
}

// DiscoverPeers finds peers advertising the namespace and retrieves their metadata
func DiscoverPeers(ctx context.Context, kadDHT *dht.IpfsDHT, logger *zap.Logger, peerManager interface {
	MarkPeerAsRecentlyRemoved(string)
	IsPeerUnhealthy(string) bool
},
) ([]*crowdllama.Resource, error) {
	peers := make([]*crowdllama.Resource, 0, 10) // Preallocate with capacity 10

	// Get the namespace CID
	namespaceCID, err := GetPeerNamespaceCID()
	if err != nil {
		return nil, fmt.Errorf("failed to get namespace CID: %w", err)
	}

	logger.Debug("Searching for peers with namespace CID",
		zap.String("namespace", crowdllama.PeerNamespace),
		zap.String("cid", namespaceCID.String()))

	// Find providers for the namespace CID
	providers := kadDHT.FindProvidersAsync(ctx, namespaceCID, 10)

	providerCount := 0
	for provider := range providers {
		providerCount++
		metadata := processProvider(ctx, provider, kadDHT, logger, peerManager)
		if metadata != nil {
			peers = append(peers, metadata)
		}
	}

	logger.Debug("Discovery complete",
		zap.Int("providers_found", providerCount),
		zap.Int("peers_with_metadata", len(peers)))

	return peers, nil
}
