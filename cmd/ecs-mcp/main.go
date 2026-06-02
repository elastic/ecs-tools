// Licensed to Elasticsearch B.V. under one or more agreements.
// Elasticsearch B.V. licenses this file to you under the Apache 2.0 License.
// See the LICENSE file in the project root for more information.

// Package main provides the ecs-mcp command provides a model context protocol (MCP) server for ECS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/handlers"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/elastic/ecs-tools/internal/fetch"
	"github.com/elastic/ecs-tools/internal/field"
	"github.com/elastic/ecs-tools/internal/store"
	"github.com/elastic/ecs-tools/internal/version"

	_ "modernc.org/sqlite"
)

var (
	cacheDir    string
	dbFile      string
	listen      string
	certFile    string
	keyFile     string
	insecure    bool
	enableDebug bool
	showVersion bool
)

func parseArgs() {
	flag.StringVar(&cacheDir, "cache", ".cache", "Directory to cache schema files")
	flag.StringVar(&dbFile, "db", "", "path to database file (when omitted, creates a temporary db that is removed on exit)")
	flag.StringVar(&listen, "listen", "", "listen for HTTP requests on this address, instead of stdin/stdout")
	flag.StringVar(&certFile, "cert", "cert.pem", "path to TLS certificate file")
	flag.StringVar(&keyFile, "key", "key.pem", "path to TLS private key file")
	flag.BoolVar(&insecure, "insecure", false, "disable TLS")
	flag.BoolVar(&showVersion, "version", false, "print version information and exit")
	flag.BoolVar(&enableDebug, "debug", false, "enable debug logging")

	flag.Parse()
}

func readEnv() {
	getStringEnv("ECS_MCP_CACHE", &cacheDir)
	getStringEnv("ECS_MCP_LISTEN", &listen)
	getStringEnv("ECS_MCP_CERT_FILE", &certFile)
	getStringEnv("ECS_MCP_KEY_FILE", &keyFile)
	getBoolEnv("ECS_MCP_INSECURE", &insecure)
	getBoolEnv("ECS_MCP_DEBUG", &enableDebug)
}

func getStringEnv(key string, target *string) {
	if value, ok := os.LookupEnv(key); ok {
		*target = value
	}
}

func getBoolEnv(key string, target *bool) {
	if value, ok := os.LookupEnv(key); ok {
		if v, err := strconv.ParseBool(value); err == nil {
			*target = v
		} else {
			slog.Warn("Unable to parse boolean from environment variable", slog.String("env", key))
		}
	}
}

func getSchemas(ctx context.Context) ([]*field.Schema, error) {
	var schemas []*field.Schema

	tags, err := fetch.VersionTags(ctx)
	if err != nil {
		return nil, err
	}

	seenVersions := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		schema, err := fetch.Schema(ctx, tag, cacheDir)
		if err != nil {
			return nil, err
		}
		if _, ok := seenVersions[schema.Version]; ok {
			continue
		}
		seenVersions[schema.Version] = struct{}{}
		schemas = append(schemas, schema)
	}

	return schemas, nil
}

// Main fetches the ECS schema, loads it into a SQLite database, and runs the
// MCP server over either HTTP or stdio depending on command-line flags.
func Main() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	schemas, err := getSchemas(ctx)
	if err != nil {
		return err
	}

	// Store fields.
	if dbFile == "" {
		dbFile = filepath.Join(os.TempDir(), fmt.Sprintf("ecs-mcp-%d.db", os.Getpid()))
		slog.Info("Using temporary database file", slog.String("path", dbFile))
		defer os.Remove(dbFile)
	}

	db, err := store.NewDB(ctx, dbFile, schemas)
	if err != nil {
		return err
	}
	defer db.Close()

	// Run MCP server.
	mcpSrv := mcp.NewServer(&mcp.Implementation{
		Name:    "ecs-mcp",
		Version: version.Version + "(" + version.Commit + ")",
	}, nil)
	addTools(mcpSrv, store.DDL, db)
	addPrompts(mcpSrv)

	if listen != "" {
		var handler http.Handler = mcp.NewStreamableHTTPHandler(
			func(_ *http.Request) *mcp.Server { return mcpSrv },
			&mcp.StreamableHTTPOptions{
				Stateless: true,
			},
		)
		handler = handlers.CombinedLoggingHandler(os.Stderr, handler)

		httpSrv := &http.Server{
			Addr:    listen,
			Handler: handler,
		}
		doneCh := make(chan struct{})

		go func() {
			timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer timeoutCancel()

			<-ctx.Done()

			_ = httpSrv.Shutdown(timeoutCtx)
			close(doneCh)
		}()

		srvURL := listen
		if strings.HasPrefix(listen, ":") {
			srvURL = "localhost" + srvURL
		}
		if insecure {
			srvURL = "http://" + srvURL
		} else {
			srvURL = "https://" + srvURL
		}

		slog.Info("Starting server", slog.String("listen", httpSrv.Addr), slog.String("url", srvURL))

		if insecure {
			err = httpSrv.ListenAndServe()
		} else {
			err = httpSrv.ListenAndServeTLS(certFile, keyFile)
		}
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
			cancel()
		}
		<-doneCh

		slog.Info("Server shut down", slog.String("listen", httpSrv.Addr))

		return err
	}

	t := &mcp.LoggingTransport{
		Transport: &mcp.StdioTransport{},
		Writer:    os.Stderr,
	}

	if err = mcpSrv.Run(ctx, t); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("failed to run stdio server: %w", err)
	}

	return nil
}

func main() {
	parseArgs()
	readEnv()

	if showVersion {
		_, _ = fmt.Fprintf(os.Stderr, "ecs-mcp version %s [commit %v]\n", version.Version, version.Commit)
		os.Exit(0)
	}

	level := slog.LevelInfo
	if enableDebug {
		level = slog.LevelDebug
	}
	logHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(logHandler))

	if err := Main(); err != nil {
		slog.Error("Error running app", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
