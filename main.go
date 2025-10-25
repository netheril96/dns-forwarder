package main

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/netheril96/dns-forwarder/lib"
	"go.uber.org/zap"
)

func main() {
	// Initialize logger
	logger, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("Failed to initialize logger: %v", err))
	}
	defer logger.Sync()

	// Load configuration
	config, err := lib.LoadConfig(os.Args[1])
	if err != nil {
		logger.Fatal("Failed to load configuration", zap.Error(err))
	}
	upstreamDNS, err := lib.CreateUpstreamDNS(config, logger)
	if err != nil {
		logger.Fatal("Failed to create upstream DNS", zap.Error(err))
	}

	// Listen for UDP packets
	addr := net.UDPAddr{
		Port: config.ListenPort,
		IP:   net.ParseIP(config.ListenAddress),
	}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		logger.Fatal("Failed to listen on port", zap.Int("port", config.ListenPort), zap.Error(err))
	}
	defer conn.Close()

	logger.Info("DNS forwarder listening on port", zap.Int("port", config.ListenPort))

	for {
		// Read from UDP
		buffer := make([]byte, 512)
		n, clientAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			logger.Error("Failed to read from UDP", zap.Error(err))
			continue
		}

		// Handle query in a new goroutine
		go handleQuery(conn, clientAddr, buffer[:n], upstreamDNS, logger)
	}
}

func handleQuery(localConn *net.UDPConn, clientAddr *net.UDPAddr, query []byte, upstream lib.UpstreamDNS, logger *zap.Logger) {
	response, err := upstream.Query(context.Background(), query)
	if err != nil {
		logger.Error("Upstream failed", zap.Error(err))
		return
	}
	// Send response back to client
	_, err = localConn.WriteToUDP(response, clientAddr)
	if err != nil {
		logger.Error("Failed to write to UDP", zap.Error(err))
	}
}
