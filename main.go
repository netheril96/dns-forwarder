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
	upstreamDNS, err := lib.CreateUpstreamDNS(config)
	if err != nil {
		log.Fatalf("Failed to create upstream DNS: %v", err)
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
		go handleQuery(conn, clientAddr, buffer[:n], upstreamDNS)
	}
}

func handleQuery(localConn *net.UDPConn, clientAddr *net.UDPAddr, query []byte, upstream lib.UpstreamDNS) {
	response, err := upstream.Query(context.Background(), query)
	if err != nil {
		log.Printf("Upstream failed: %v", err)
		return
	}
	// Send response back to client
	_, err = localConn.WriteToUDP(response, clientAddr)
	if err != nil {
		log.Printf("Failed to write to UDP: %v", err)
	}
}
