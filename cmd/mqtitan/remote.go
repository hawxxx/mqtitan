package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/mqtitan/mqtitan/internal/api"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func request(ctx context.Context, controller, method, path string, body any, out any) error {
	base, err := url.Parse(controller)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return errors.New("controller must be HTTP or HTTPS URL")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(controller, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if token := os.Getenv("MQTITAN_TOKEN"); token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		return errors.New("controller request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("controller returned HTTP %d", resp.StatusCode)
	}
	if writer, ok := out.(io.Writer); ok {
		_, err = io.Copy(writer, io.LimitReader(resp.Body, 64<<20))
		return err
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&envelope); err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}
func remoteCommand(command string, args []string) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	controller := f.String("controller", controllerDefault("http://127.0.0.1:8080"), "controller URL (env MQTITAN_CONTROLLER)")
	format := f.String("format", "json", "export format json/csv")
	id := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id = args[0]
		args = args[1:]
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	path := "/api/v1/tests"
	method := "GET"
	switch command {
	case "workers":
		path = "/api/v1/workers"
	case "status":
		if id != "" {
			path += "/" + url.PathEscape(id)
		}
	case "stop", "results", "export":
		if id == "" {
			return errors.New("test ID required")
		}
		path += "/" + url.PathEscape(id)
		if command == "stop" {
			path += "/stop"
			method = "POST"
		}
		if command == "export" {
			path += "/export?format=" + url.QueryEscape(*format)
		}
	}
	ctx, cancel := signalContext()
	defer cancel()
	return request(ctx, *controller, method, path, nil, os.Stdout)
}
func remoteRun(ctx context.Context, controller, raw string, workers []string) error {
	var t api.Test
	if err := request(ctx, controller, "POST", "/api/v1/tests", map[string]any{"scenario": raw, "workers": workers}, &t); err != nil {
		return err
	}
	fmt.Printf("Test %s\nDashboard: %s\n", t.ID, controller)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = request(stopCtx, controller, "POST", "/api/v1/tests/"+t.ID+"/stop", nil, nil)
			return exitError{130, "test interrupted"}
		case <-ticker.C:
			if err := request(ctx, controller, "GET", "/api/v1/tests/"+t.ID, nil, &t); err != nil {
				return err
			}
			fmt.Printf("connected=%d total=%d p95=%s\n", t.Snapshot.Connected, t.Snapshot.Published, t.Snapshot.P95)
			if t.Status != "running" {
				return verdict(t)
			}
		}
	}
}
