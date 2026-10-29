package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/plumpslabs/foreman/internal/foreman"
	"github.com/plumpslabs/foreman/internal/version"
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
	case "doctor":
		if err := doctor(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "foreman doctor:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		fmt.Println(version.Info())
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("Foreman — local-first agent workspace")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  foreman start [flags]   Start the dashboard (alias: fm start)")
	fmt.Println("  foreman doctor [flags]   Check Python, pip, and the OpenHands SDK")
	fmt.Println("  foreman version          Print version")
	fmt.Println()
	fmt.Println("start flags:")
	fmt.Println("  --project DIR     Project directory (default: current directory)")
	fmt.Println("  --port PORT       Preferred local port; 0 picks an available one")
	fmt.Println("  --no-open         Do not open a browser")
	fmt.Println("  --install-engine   Install the OpenHands SDK at startup if missing")
	fmt.Println("  --yes             Skip confirmation (with --install-engine)")
	fmt.Println()
	fmt.Println("doctor flags:")
	fmt.Println("  --project DIR     Inspect a workspace (optional)")
	fmt.Println("  --install         Install the OpenHands SDK if missing")
	fmt.Println()
	fmt.Println("Tip: the installer adds a `fm` alias next to `foreman`.")
}

func start(args []string) error {
	flags := flag.NewFlagSet("start", flag.ContinueOnError)
	projectPath := flags.String("project", ".", "Project directory (default: current directory)")
	port := flags.Int("port", 0, "Preferred local port; 0 selects an available port")
	noOpen := flags.Bool("no-open", false, "Do not open a browser")
	installEngine := flags.Bool("install-engine", false, "Install the OpenHands SDK if missing")
	autoYes := flags.Bool("yes", false, "Skip confirmation (with --install-engine)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535")
	}

	engine := foreman.NewEngine()
	installed, probe := engine.SDKInstalled()
	pyVer := engine.PythonVersion()

	if !installed && *installEngine {
		ok, err := promptInstallEngine(*autoYes)
		if err != nil {
			return err
		}
		if ok {
			fmt.Println("Installing the OpenHands SDK (this can take a few minutes)…")
			log, err := installEngineCLI(engine)
			if err != nil {
				return fmt.Errorf("engine install failed — see log below\n%s", log)
			}
			installed, probe = engine.SDKInstalled()
			if installed {
				fmt.Println("✅ OpenHands SDK installed")
			} else {
				fmt.Printf("⚠️  SDK still not detected after install (probe: %s)\n", probe)
			}
		}
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

	fmt.Println("◢ Foreman " + version.Version)
	fmt.Printf("  Project   : %s\n", state.ProjectPath)
	fmt.Printf("  Data      : %s\n", filepath.Join(state.ProjectPath, ".foreman"))
	if installed {
		fmt.Printf("  Engine    : ✓ OpenHands SDK (%s)\n", orDash(pyVer))
	} else {
		fmt.Printf("  Engine    : ✗ OpenHands SDK belum terpasang (%s)\n", orDash(pyVer))
		fmt.Println("              → foreman doctor --install  atau  python3 -m pip install openhands-sdk openhands-tools")
	}
	fmt.Printf("  Dashboard : %s\n", address)
	fmt.Println("  Mode      : local-only · no agent run starts automatically")
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

func doctor(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	projectPath := flags.String("project", ".", "Project directory to inspect (optional)")
	install := flags.Bool("install", false, "Install the OpenHands SDK if missing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	engine := foreman.NewEngine()
	installed, probe := engine.SDKInstalled()
	pyVer := engine.PythonVersion()

	fmt.Println("Foreman doctor")
	fmt.Printf("  Python        : %s\n", orDash(pyVer))
	fmt.Printf("  pip           : %s\n", yesNo(engine.PipAvailable()))
	fmt.Printf("  OpenHands SDK : %s\n", installedStatus(installed))
	if !installed && probe != "" {
		fmt.Printf("    probe       : %s\n", probe)
	}

	if !installed && *install {
		ok, err := promptInstallEngine(true)
		if err != nil {
			return err
		}
		if ok {
			fmt.Println("Installing the OpenHands SDK (this can take a few minutes)…")
			log, err := installEngineCLI(engine)
			if err != nil {
				fmt.Fprintf(os.Stderr, "install failed — see log below\n%s\n", log)
				return err
			}
			installed, probe = engine.SDKInstalled()
			if installed {
				fmt.Println("✅ OpenHands SDK installed")
			} else {
				fmt.Printf("⚠️  SDK still not detected (probe: %s)\n", probe)
			}
		}
	}

	if _, serr := foreman.OpenStore(*projectPath); serr == nil {
		fmt.Printf("  Workspace     : %s (ok)\n", *projectPath)
	} else {
		fmt.Printf("  Workspace     : %s (not a Foreman workspace yet — create it with `foreman start`)\n", *projectPath)
	}

	if !installed {
		return fmt.Errorf("OpenHands SDK is missing — run `foreman doctor --install` or `python3 -m pip install openhands-sdk openhands-tools`")
	}
	return nil
}

func installEngineCLI(engine *foreman.Engine) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return engine.InstallSDK(ctx)
}

func promptInstallEngine(autoYes bool) (bool, error) {
	if autoYes {
		return true, nil
	}
	fmt.Print("OpenHands SDK belum terpasang. Install sekarang? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "y"), nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func installedStatus(b bool) string {
	if b {
		return "installed ✓"
	}
	return "missing ✗"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
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
