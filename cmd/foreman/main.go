package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/plumpslabs/bro-code/internal/foreman"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	switch os.Args[1] {
	case "start":
		if err := start(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "foreman:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println("Foreman dev")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("Foreman — local-first agent workspace")
	fmt.Println("Usage: foreman start [--project DIR] [--port PORT] [--no-open]")
}

func start(args []string) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	projectPath := flags.String("project", ".", "Project directory (default: current directory)")
	port := flags.Int("port", 0, "Preferred local port; 0 selects an available port")
	noOpen := flags.Bool("no-open", false, "Do not open a browser")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535")
	}

	st, err := foreman.OpenStore(*projectPath)
	if err != nil {
		return err
	}
	state := st.Snapshot()
	server, err := foreman.NewServer(st, state.ProjectPath)
	if err != nil {
		return fmt.Errorf("configure server: %w", err)
	}
	listener, actualPort, err := listenLocal(*port)
	if err != nil {
		return err
	}
	address := fmt.Sprintf("http://127.0.0.1:%d/?project=%s", actualPort, url.QueryEscape(state.ProjectPath))
	httpServer := foreman.NewHTTPServer(listener.Addr().String(), server.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.Serve(listener)
	}()

	fmt.Printf("Foreman is ready for %s\n", state.ProjectPath)
	fmt.Printf("Dashboard: %s\n", address)
	fmt.Println("Local-only mode · no agent run or external request is started automatically")
	if !*noOpen {
		_ = openBrowser(address)
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown dashboard: %w", err)
		}
		fmt.Println("Foreman stopped")
		return nil
	case err := <-serverErrors:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("dashboard server: %w", err)
		}
		return nil
	}
}

func listenLocal(preferred int) (net.Listener, int, error) {
	if preferred > 0 {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(preferred)))
		if err == nil {
			return listener, listener.Addr().(*net.TCPAddr).Port, nil
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("find available local port: %w", err)
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

func openBrowser(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	return command.Start()
}
