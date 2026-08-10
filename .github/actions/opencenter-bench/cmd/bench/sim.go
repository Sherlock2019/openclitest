package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/opencenter-cloud/opencli-testbench/internal/flexsim"
)

// serveSimulator runs the OpenStack simulator on its own, for the times you
// want to point something other than the bench at it — the openstack client,
// curl, or a CLI you are stepping through in a debugger.
func serveSimulator(ctx context.Context, address, inventoryPath, cloudsPath string, verbose bool) error {
	inventory, err := flexsim.LoadInventory(inventoryPath)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	baseURL := "http://" + listener.Addr().String()
	server := flexsim.New(inventory, baseURL, verbose)

	if cloudsPath != "" {
		if err := os.WriteFile(cloudsPath, []byte(server.CloudsYAML("flex-sim")), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", cloudsPath, err)
		}
		fmt.Printf("  wrote %s\n", cloudsPath)
	}

	fmt.Printf("\n  OpenStack simulator\n  %s\n\n  %s\n\n", baseURL, server.Describe())
	fmt.Printf("  export OS_CLOUD=flex-sim OS_CLIENT_CONFIG_FILE=%s\n\n", cloudsPath)
	fmt.Println("  Discovery and validation only. Provisioning is not simulated;")
	fmt.Println("  use the Kind or FLEX environment for a real cluster lifecycle.")
	fmt.Println()

	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
