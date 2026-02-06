package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"sandbox/pkg/api"
	"sandbox/pkg/sbxclient"

	"github.com/gorilla/websocket"
)

const defaultBaseURL = "http://localhost:8080"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	baseURL := fs.String("addr", defaultBaseURL, "control-plane base URL")
	id := fs.String("id", "", "sandbox id")
	image := fs.String("image", "", "sandbox image")
	volumeMode := fs.String("volume", "", "volume mode: emptydir|pvc")
	cacheMode := fs.String("cache-mode", "", "cache mode: emptydir|hostpath|pvc")
	cachePVCSize := fs.String("cache-pvc-size", "", "cache pvc size (e.g. 5Gi)")
	cachePVCStorageClass := fs.String("cache-pvc-storage-class", "", "cache pvc storage class")
	cachePVCAccessMode := fs.String("cache-pvc-access-mode", "", "cache pvc access mode (ReadWriteOnce/ReadWriteMany/ReadOnlyMany)")
	var envVars stringSlice
	var allowHosts stringSlice
	var denyHosts stringSlice
	fs.Var(&envVars, "env", "environment variable (KEY=VALUE), repeatable")
	fs.Var(&allowHosts, "allow-host", "allowed host (repeatable)")
	fs.Var(&denyHosts, "deny-host", "disallowed host (repeatable)")
	command := fs.String("cmd", "", "command to exec (space-separated)")
	syncMode := fs.Bool("sync", false, "run exec synchronously (block until completion)")
	stream := fs.Bool("stream", false, "stream exec output after starting")
	streamRaw := fs.Bool("stream-raw", false, "stream only stdout/stderr (no JSON)")
	execID := fs.String("exec-id", "", "exec id")
	timeoutSeconds := fs.Int("timeout", 0, "exec timeout in seconds")
	fs.Parse(os.Args[2:])

	client := sbxclient.New(*baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch cmd {
	case "create":
		req := api.CreateSandboxRequest{
			ID:                   *id,
			Image:                *image,
			VolumeMode:           *volumeMode,
			CacheMode:            *cacheMode,
			CachePVCSize:         *cachePVCSize,
			CachePVCStorageClass: *cachePVCStorageClass,
			CachePVCAccessMode:   *cachePVCAccessMode,
			AllowedHosts:         allowHosts,
			DisallowedHosts:      denyHosts,
		}
		envMap, err := parseEnvPairs(envVars)
		fatalIf(err)
		req.Env = envMap
		resp, err := client.Create(ctx, req)
		fatalIf(err)
		fmt.Printf("id=%s namespace=%s pod=%s\n", resp.ID, resp.Namespace, resp.PodName)
	case "exec":
		if *id == "" {
			fatal("-id is required")
		}
		args := fs.Args()
		if len(args) == 0 && *command != "" {
			args = strings.Fields(*command)
		}
		if len(args) == 0 {
			fatal("-cmd is required")
		}
		async := !*syncMode
		if *stream || *streamRaw {
			async = true
		}
		req := api.ExecRequest{Command: args, Async: &async}
		if *timeoutSeconds > 0 {
			req.TimeoutSeconds = timeoutSeconds
		}
		resp, err := client.Exec(ctx, *id, req)
		fatalIf(err)
		if resp.ExecID != "" {
			fmt.Printf("exec_id=%s status=%s\n", resp.ExecID, resp.Status)
			if *stream || *streamRaw {
				streamExecWS(*baseURL, *id, resp.ExecID, *streamRaw)
			}
			return
		}
		fmt.Print(resp.Stdout)
		if resp.Stderr != "" {
			fmt.Fprint(os.Stderr, resp.Stderr)
		}
	case "status":
		if *id == "" {
			resp, err := client.ListSandboxes(ctx)
			fatalIf(err)
			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tAGE\tSTATE\tALLOCATED\tLAST_EXEC_TIME")
			for _, s := range resp {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Age, s.State, s.Allocated, s.LastExecTime)
			}
			_ = w.Flush()
			return
		}
		resp, err := client.Status(ctx, *id)
		fatalIf(err)
		for k, v := range resp {
			fmt.Printf("%s=%s\n", k, v)
		}
	case "delete":
		if *id == "" {
			fatal("-id is required")
		}
		fatalIf(client.Delete(ctx, *id))
		fmt.Println("deleted")
	case "exec-status":
		if *id == "" {
			fatal("-id is required")
		}
		if *execID == "" {
			fatal("-exec-id is required")
		}
		resp, err := client.ExecStatus(ctx, *id, *execID)
		fatalIf(err)
		printExecStatus(resp)
	case "exec-cancel":
		if *id == "" {
			fatal("-id is required")
		}
		if *execID == "" {
			fatal("-exec-id is required")
		}
		resp, err := client.CancelExec(ctx, *id, *execID)
		fatalIf(err)
		printExecStatus(resp)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("usage: sbx <create|exec|status|delete|exec-status|exec-cancel> [flags]")
	fmt.Println("  -addr http://localhost:8080")
	fmt.Println("  -id demo")
	fmt.Println("  -image ubuntu:22.04")
	fmt.Println("  -volume emptydir|pvc")
	fmt.Println("  -cache-mode emptydir|hostpath|pvc")
	fmt.Println("  -cache-pvc-size 5Gi")
	fmt.Println("  -cache-pvc-storage-class standard")
	fmt.Println("  -cache-pvc-access-mode ReadWriteOnce")
	fmt.Println("  -env KEY=VALUE (repeatable)")
	fmt.Println("  -allow-host example.com (repeatable)")
	fmt.Println("  -deny-host example.com (repeatable)")
	fmt.Println("  -cmd 'bash -lc ls -la'")
	fmt.Println("  -timeout 30")
	fmt.Println("  -exec-id <exec_id>")
	fmt.Println("  -sync (block until completion; disables streaming)")
	fmt.Println("  -stream (starts async exec and connects to stream)")
	fmt.Println("  -stream-raw (starts async exec and prints only stdout/stderr)")
	fmt.Println("  status without -id lists all sandboxes")
	fmt.Println("  exec supports args after --, e.g. sbx exec -id demo -- bash -lc 'uname -a'")
}

func printExecStatus(resp *api.ExecStatusResponse) {
	fmt.Printf("sandbox_id=%s\n", resp.SandboxID)
	fmt.Printf("exec_id=%s\n", resp.ExecID)
	fmt.Printf("status=%s\n", resp.Status)
	if resp.ExitCode != nil {
		fmt.Printf("exit_code=%d\n", *resp.ExitCode)
	}
	if resp.TimeoutSeconds != nil {
		fmt.Printf("timeout_seconds=%d\n", *resp.TimeoutSeconds)
	}
	if resp.StartedAt != "" {
		fmt.Printf("started_at=%s\n", resp.StartedAt)
	}
	if resp.FinishedAt != "" {
		fmt.Printf("finished_at=%s\n", resp.FinishedAt)
	}
	if resp.Error != "" {
		fmt.Printf("error=%s\n", resp.Error)
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

func fatalIf(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func streamExecWS(baseURL, id, execID string, raw bool) {
	wsURL := strings.TrimRight(baseURL, "/")
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL = fmt.Sprintf("%s/sandboxes/%s/stream?exec_id=%s", wsURL, id, execID)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		fatal(err.Error())
	}
	defer conn.Close()
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var evt struct {
			Type   string `json:"type"`
			ExecID string `json:"exec_id"`
			Stream string `json:"stream"`
			Data   string `json:"data"`
		}
		if jsonErr := json.Unmarshal(msg, &evt); jsonErr == nil {
			if raw && evt.Type == "output" {
				if evt.Stream == "stderr" {
					fmt.Fprint(os.Stderr, evt.Data)
				} else {
					fmt.Fprint(os.Stdout, evt.Data)
				}
			} else if !raw {
				fmt.Fprintln(os.Stdout, string(msg))
			}
			if evt.ExecID == execID && evt.Type == "exit" {
				return
			}
			continue
		}
		if !raw {
			fmt.Fprintln(os.Stdout, string(msg))
		}
	}
}

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(val string) error {
	*s = append(*s, val)
	return nil
}

func parseEnvPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, pair := range pairs {
		key, val, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid env pair: %q (expected KEY=VALUE)", pair)
		}
		out[key] = val
	}
	return out, nil
}
