// maxmix mixes the audio of selected applications (the running game, Discord)
// or devices into one virtual output (VB-CABLE) and points game recorders
// (Steam Game Recording, NVIDIA App / ShadowPlay) at that output, so
// recordings contain game and voice chat together. It lives in the tray.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

const usage = `maxmix - mix game + Discord into one virtual device for Steam / NVIDIA recordings

usage: maxmix [-config maxmix.json] <command>

commands:
  run             tray icon + mixer + routing of recorder apps (default when started without arguments)
  run -no-route   same without touching the recorder apps
  run -settings   open the settings window right away
  devices         list active playback and recording endpoints
  sessions        list processes that currently have audio sessions
  game            show which running process the game detector would pick, and why
  selftest [secs]  measure every input, the mix and the microphone; writes selftest-*.wav
  default [name [role]]  show the system default devices, or make the matching playback device the
                  default (for all roles, or only console | multimedia | communications)
  route           point recorder apps (steam.exe, nvcontainer.exe, ...) at the virtual device
  unroute         remove the per-app override, apps follow the system default again
  status          show the current per-app device of the recorder apps
  install-cable   install VB-CABLE (downloads the driver pack if needed, UAC prompt)
  autostart       start with Windows            (autostart -remove to undo)
  config          print the effective configuration
`

func main() {
	hasConsole := attachParentConsole()
	fs := flag.NewFlagSet("maxmix", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to maxmix.json")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	fs.Parse(os.Args[1:])

	cmd := "run"
	rest := fs.Args()
	if len(rest) > 0 {
		cmd = rest[0]
		rest = rest[1:]
	}

	if err := initCOM(); err != nil {
		fatal(err)
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fatal(fmt.Errorf("config %s: %w", *cfgPath, err))
	}

	switch cmd {
	case "run":
		noRoute, settings := false, false
		for _, a := range rest {
			switch a {
			case "-no-route", "--no-route":
				noRoute = true
			case "-settings", "--settings":
				settings = true
			}
		}
		setupLogging(hasConsole)
		runCommand(cfg, *cfgPath, !noRoute, settings)
	case "devices":
		err = cmdDevices()
	case "sessions":
		err = cmdSessions()
	case "game":
		err = cmdGame()
	case "default":
		err = cmdDefault(rest)
	case "selftest", "check":
		err = cmdSelfTest(cfg, rest)
	case "route":
		err = cmdRoute(cfg)
	case "unroute":
		var res RouteResult
		res, err = routeApps(cfg.Apps, "")
		if err == nil {
			err = reportRoute(res, "override left as is")
		}
	case "status":
		apps := cfg.Apps
		if len(rest) > 0 {
			apps = rest // maxmix status foo.exe bar.exe
		}
		var eps []Endpoint
		eps, err = listEndpoints(eRender)
		if err == nil {
			err = routeStatus(apps, eps)
		}
	case "install-cable":
		err = installCable()
	case "autostart":
		err = setAutostart(!(len(rest) > 0 && (rest[0] == "-remove" || rest[0] == "--remove")))
	case "config":
		fmt.Println(cfg)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// osStdout / osStderr rebind the process streams after AttachConsole.
func osStdout(h uintptr) { os.Stdout = os.NewFile(h, "stdout") }
func osStderr(h uintptr) { os.Stderr = os.NewFile(h, "stderr") }

// tolerantWriter writes to every sink and ignores individual failures (the
// console may be gone while the log file is fine).
type tolerantWriter []io.Writer

func (t tolerantWriter) Write(p []byte) (int, error) {
	for _, w := range t {
		w.Write(p)
	}
	return len(p), nil
}

func setupLogging(console bool) {
	sinks := tolerantWriter{}
	if console {
		sinks = append(sinks, os.Stderr)
	}
	if err := os.MkdirAll(filepath.Dir(logPath()), 0o755); err == nil {
		if f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			sinks = append(sinks, f)
		}
	}
	log.SetOutput(sinks)
	log.SetFlags(log.Ldate | log.Ltime)
}

func cmdDevices() error {
	for _, flow := range []struct {
		name string
		flow uint32
	}{{"playback (render)", eRender}, {"recording (capture)", eCapture}} {
		eps, err := listEndpoints(flow.flow)
		if err != nil {
			return err
		}
		fmt.Printf("%s:\n", flow.name)
		for _, e := range eps {
			fmt.Printf("  %-50s %s\n", e.Name, e.ID)
		}
	}
	return nil
}

// targetID resolves the configured virtual device.
func targetID(cfg Config) (Endpoint, error) {
	eps, err := listEndpoints(eRender)
	if err != nil {
		return Endpoint{}, err
	}
	for _, cand := range cfg.Target {
		if ep, ok := findEndpoint(eps, cand); ok {
			return ep, nil
		}
	}
	return Endpoint{}, fmt.Errorf("target endpoint not found (tried %v)", cfg.Target)
}

func cmdRoute(cfg Config) error {
	ep, err := targetID(cfg)
	if err != nil {
		return err
	}
	res, err := routeApps(cfg.Apps, ep.ID)
	if err != nil {
		return err
	}
	return reportRoute(res, "start them and rerun `maxmix route`")
}

// reportRoute prints the apps that were skipped or rejected. It returns an
// error only when nothing at all could be applied.
func reportRoute(res RouteResult, missingHint string) error {
	if len(res.Missing) > 0 {
		fmt.Printf("not running (%s): %s\n", missingHint, strings.Join(res.Missing, ", "))
	}
	for app, e := range res.Failed {
		fmt.Printf("%s: %v\n  -> %s\n", app, e, notReadyHint)
	}
	if len(res.Done) == 0 && (len(res.Missing) > 0 || len(res.Failed) > 0) {
		return errors.New("no app could be routed")
	}
	return nil
}

// routeLoop applies the per-app override for every configured app, retrying
// the ones that are not running yet or not yet known to the audio service
// (see routeApps). It stops once every app has been handled.
func routeLoop(ctx context.Context, m *mixer) {
	warned := map[string]bool{}
	pending := append([]string(nil), m.config().Apps...)
	for len(pending) > 0 {
		cfg := m.config()
		ep, err := targetID(cfg)
		if err == nil {
			var res RouteResult
			res, err = routeApps(pending, ep.ID)
			if err == nil {
				pending = pending[:0]
				pending = append(pending, res.Missing...)
				for app, e := range res.Failed {
					pending = append(pending, app)
					if !warned[app] {
						warned[app] = true
						log.Printf("[%s] not routed yet: %v; %s (retrying every 15s)", app, e, notReadyHint)
					}
				}
				for _, app := range res.Missing {
					if !warned[app] {
						warned[app] = true
						log.Printf("[%s] not running; will route when it appears", app)
					}
				}
			}
		}
		if err != nil {
			log.Printf("route: %v", err)
		}
		if len(pending) == 0 {
			log.Print("all recorder apps routed to the virtual device")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

// runCommand starts the mixer in the background and runs the tray icon /
// settings window message loop on this thread until the user exits.
func runCommand(cfg Config, cfgPath string, route, openSettings bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !claimSingleInstance() {
		log.Print("maxmix is already running; opened its settings instead")
		return
	}
	log.Printf("maxmix starting; sources: %s; target: %v", describeSources(cfg), cfg.Target)
	m := newMixer(cfg, cfgPath)
	if route {
		go routeLoop(ctx, m)
	}
	done := make(chan struct{})
	go func() { defer close(done); m.run(ctx) }()

	g, err := newGUI(m, cancel)
	if err != nil {
		// No desktop (service session?): run headless until Ctrl+C.
		log.Printf("tray unavailable (%v); running headless", err)
		sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt)
		<-sigCtx.Done()
		stop()
		cancel()
		<-done
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		select {
		case <-sig:
			g.postExit()
		case <-ctx.Done():
		}
	}()
	if openSettings {
		g.openSettings()
	}
	g.loop()
	cancel()
	<-done
	log.Print("maxmix stopped")
}
