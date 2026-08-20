// Command systemctlshim stands in for systemd during real-runtime tests.
//
// It is NOT a mock of the adapter or of its runtime. ExecRuntime is executed
// in full: it really shells out to a program called "systemctl" on PATH, really
// passes the arguments it builds, and really parses the output and exit status.
// What this replaces is only the init system, which is not antimage's code and
// cannot be run inside `go test` on either a developer's machine or a GitHub
// runner.
//
// The processes it manages are the real xray and sing-box binaries reading the
// real generated configuration. A unit that fails to come up -- because the
// binary rejected its config, or could not bind its port -- makes "restart"
// exit non-zero exactly as systemd would, which is what the adapter's failure
// path is meant to react to.
//
// State lives under $SHIM_STATE:
//
//	<unit>.json  {"path":"...","args":[...],"port":N}
//	<unit>.pid   the managed process
//	<unit>.log   its combined output, surfaced on failure
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type unitSpec struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	// Port is what the managed process is expected to listen on. Liveness is
	// judged by whether it is accepting connections, which is a stronger claim
	// than "a pid exists" and is portable across the platforms these tests run
	// on.
	Port int `json:"port"`
}

func stateDir() string {
	dir := os.Getenv("SHIM_STATE")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "systemctlshim: SHIM_STATE is not set")
		os.Exit(64)
	}
	return dir
}

func specPath(unit string) string { return filepath.Join(stateDir(), unit+".json") }
func pidPath(unit string) string  { return filepath.Join(stateDir(), unit+".pid") }
func logPath(unit string) string  { return filepath.Join(stateDir(), unit+".log") }

func readSpec(unit string) (unitSpec, error) {
	var s unitSpec
	body, err := os.ReadFile(specPath(unit))
	if err != nil {
		return s, fmt.Errorf("no unit definition for %s: %w", unit, err)
	}
	return s, json.Unmarshal(body, &s)
}

func listening(port int) bool {
	if port == 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func stop(unit string) {
	body, err := os.ReadFile(pidPath(unit))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err == nil {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
			_, _ = p.Wait()
		}
	}
	_ = os.Remove(pidPath(unit))

	// Wait for the port to be released so an immediate restart does not race
	// the old process out of the way.
	if spec, err := readSpec(unit); err == nil {
		for i := 0; i < 40 && listening(spec.Port); i++ {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func start(unit string) int {
	spec, err := readSpec(unit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "systemctlshim: %v\n", err)
		return 5
	}

	logFile, err := os.Create(logPath(unit))
	if err != nil {
		fmt.Fprintf(os.Stderr, "systemctlshim: open log: %v\n", err)
		return 1
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "systemctlshim: start %s: %v\n", unit, err)
		return 1
	}
	if err := os.WriteFile(pidPath(unit), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "systemctlshim: write pid: %v\n", err)
		return 1
	}

	// Reap the child in the background so a process that dies immediately does
	// not linger as a zombie holding the port.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// The unit is up when it is serving. A binary that rejects its config exits
	// instead, and that must be reported as a failed start, not a slow one.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case werr := <-exited:
			out, _ := os.ReadFile(logPath(unit))
			fmt.Fprintf(os.Stderr, "systemctlshim: %s exited during startup: %v\n%s\n",
				unit, werr, strings.TrimSpace(string(out)))
			_ = os.Remove(pidPath(unit))
			return 1
		default:
		}
		if listening(spec.Port) {
			fmt.Printf("started %s\n", unit)
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}

	out, _ := os.ReadFile(logPath(unit))
	fmt.Fprintf(os.Stderr, "systemctlshim: %s never listened on %d\n%s\n",
		unit, spec.Port, strings.TrimSpace(string(out)))
	stop(unit)
	return 1
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: systemctl <verb> <unit>")
		os.Exit(64)
	}
	verb, unit := os.Args[1], os.Args[2]

	switch verb {
	case "is-active":
		spec, err := readSpec(unit)
		if err != nil || !listening(spec.Port) {
			// systemd prints the state and exits non-zero when a unit is down.
			fmt.Println("inactive")
			os.Exit(3)
		}
		fmt.Println("active")

	case "start":
		os.Exit(start(unit))

	case "stop":
		stop(unit)

	case "restart", "reload-or-restart":
		stop(unit)
		os.Exit(start(unit))

	default:
		fmt.Fprintf(os.Stderr, "systemctlshim: unsupported verb %q\n", verb)
		os.Exit(64)
	}
}
