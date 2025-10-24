package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/netheril96/dns-forwarder/lib"
)

func main() {
	// Load configuration
	config, err := lib.LoadConfig(os.Args[1])
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Listen for UDP packets
	addr := net.UDPAddr{
		Port: config.ListenPort,
		IP:   net.ParseIP(config.ListenAddress),
	}
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		log.Fatalf("Failed to listen on port %d: %v", config.ListenPort, err)
	}
	defer conn.Close()

	fmt.Printf("DNS forwarder listening on port %d", config.ListenPort)

	for {
		// Read from UDP
		buffer := make([]byte, 512)
		n, clientAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			log.Printf("Failed to read from UDP: %v", err)
			continue
		}

		// Handle query in a new goroutine
		go handleQuery(conn, clientAddr, buffer[:n], config.UpstreamServers)
	}
}

func handleQuery(localConn *net.UDPConn, clientAddr *net.UDPAddr, query []byte, upstreams []lib.UpstreamServerConfig) {
	// Iterate over upstream servers
	for _, upstreamConfig := range upstreams {
		forwarder := lib.NewForwarder(upstreamConfig)
		// Forward query and get response
		response, err := forwarder.Forward(context.Background(), query)
		if err != nil {
			log.Printf("Upstream %s failed: %v", upstreamConfig.Address, err)
			continue
		}

		// Send response back to client
		_, err = localConn.WriteToUDP(response, clientAddr)
		if err != nil {
			log.Printf("Failed to write to UDP: %v", err)
		}
		return
	}

	// If all upstreams fail, send a failure response
	// (This part is simplified and can be improved)
	log.Printf("All upstreams failed for a query from %s", clientAddr.String())
}
