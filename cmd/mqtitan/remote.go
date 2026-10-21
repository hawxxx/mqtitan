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
		// The controller's error messages are written for clients; show them instead of a bare status.
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&failure) == nil && failure.Error.Message != "" {
			return fmt.Errorf("controller returned HTTP %d: %s", resp.StatusCode, failure.Error.Message)
		}
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
	interrupt := func() error {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := request(stopCtx, controller, "POST", "/api/v1/tests/"+url.PathEscape(t.ID)+"/stop", nil, nil); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not stop test %s: %v\n", t.ID, err)
		}
		return exitError{130, "test interrupted"}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return interrupt()
		case <-ticker.C:
			var next api.Test
			if err := request(ctx, controller, "GET", "/api/v1/tests/"+url.PathEscape(t.ID), nil, &next); err != nil {
				// An interrupt cancels the in-flight poll; still stop the remote test.
				if ctx.Err() != nil {
					return interrupt()
				}
				// Tolerate brief controller hiccups rather than abandoning a running test.
				if failures++; failures >= 5 {
					return fmt.Errorf("lost contact with controller; test %s may still be running: %w", t.ID, err)
				}
				fmt.Fprintf(os.Stderr, "warning: %v; retrying\n", err)
				continue
			}
			failures = 0
			t = next
			fmt.Printf("connected=%d total=%d p95=%s\n", t.Snapshot.Connected, t.Snapshot.Published, t.Snapshot.P95)
			if t.Status != "running" {
				return verdict(t)
			}
		}
	}
}
