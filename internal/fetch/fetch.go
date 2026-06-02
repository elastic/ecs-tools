// Licensed to Elasticsearch B.V. under one or more agreements.
// Elasticsearch B.V. licenses this file to you under the Apache 2.0 License.
// See the LICENSE file in the project root for more information.

package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/elastic/ecs-tools/internal/field"
)

type schemaFile struct {
	Version string `json:"version"`
	Data    []byte `json:"data"`
}

type tag struct {
	Name string `json:"name"`
}

// VersionTags fetches all version tags (those that begin with 'v') from the ECS
// GitHub repository via the GitHub API. Version tags prior to v1.12.0 are
// rejected due to an incompatible schema file format.
func VersionTags(ctx context.Context) ([]string, error) {
	client := &http.Client{}

	minVersion := semver.MustParse("v1.12.0")

	page := 1

	var tags []string
	for {
		url := fmt.Sprintf("https://api.github.com/repos/elastic/ecs/tags?per_page=20&page=%d", page)

		slog.Debug("Fetching tags", slog.String("url", url), slog.Int("page", page))
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}

		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		if res.StatusCode != http.StatusOK {
			_ = res.Body.Close()
			return nil, fmt.Errorf("failed to get tags from %q, unexpected status code %d", url, res.StatusCode)
		}

		var pageTags []tag
		if err = json.NewDecoder(res.Body).Decode(&pageTags); err != nil {
			_ = res.Body.Close()
			return nil, err
		}
		_ = res.Body.Close()

		if len(pageTags) == 0 {
			break
		}

		for _, v := range pageTags {
			if !strings.HasPrefix(v.Name, "v") {
				continue
			}
			ver, err := semver.NewVersion(v.Name)
			if err != nil || ver.LessThan(minVersion) {
				continue
			}

			tags = append(tags, v.Name)
		}

		page++
	}

	slog.Debug("Fetched tags", slog.Int("count", len(tags)))

	return tags, nil
}

// Schema fetches an ECS schema from the ECS GitHub repository using the provided
// git ref. The ref can be a tag (recommended), branch name, or hash.
func Schema(ctx context.Context, ref, cache string) (*field.Schema, error) {
	var cacheFile string
	if cache != "" {
		cacheFile = filepath.Join(cache, ref+"_schema.json")
	}
	if cacheFile != "" {
		if schema, err := loadCachedSchema(cacheFile); err == nil {
			slog.Debug("Using cached schema file", slog.String("file", cacheFile), slog.String("ref", ref))
			return schema, nil
		} else {
			slog.Warn("Failed to load schema cache file", slog.String("filename", cacheFile), slog.String("error", err.Error()))
		}
	}

	client := &http.Client{}

	// Get version
	versionRaw, err := fetchFile(ctx, client, "https://raw.githubusercontent.com/elastic/ecs/"+ref+"/version")
	if err != nil {
		return nil, err
	}
	version := string(bytes.TrimSpace(versionRaw))

	schemaRaw, err := fetchFile(ctx, client, "https://raw.githubusercontent.com/elastic/ecs/"+ref+"/generated/ecs/ecs_nested.yml")
	if err != nil {
		return nil, err
	}

	schema, err := field.ParseSchema(version, schemaRaw)
	if err != nil {
		return nil, err
	}

	if cacheFile != "" {
		if err = os.MkdirAll(filepath.Dir(cacheFile), 0o755); err == nil {
			cached := schemaFile{Version: version, Data: schemaRaw}
			if data, err := json.Marshal(&cached); err == nil {
				_ = os.WriteFile(cacheFile, data, 0o644)
			}
		}
	}

	if cacheFile != "" {
		if err = saveCachedSchema(cacheFile, version, schemaRaw); err == nil {
			return schema, nil
		}
		slog.Warn("Failed to save schema cache file", slog.String("filename", cacheFile), slog.String("error", err.Error()))
	}

	return schema, nil
}

func fetchFile(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	slog.Debug("Fetching file", slog.String("url", url))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for remote file %q: %w", url, err)
	}

	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote file %q: %w", url, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get remote file %q, unexpected status code %d", url, res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body from %q: %w", url, err)
	}

	return body, nil
}

func loadCachedSchema(filename string) (*field.Schema, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	var cached schemaFile
	if err = json.Unmarshal(raw, &cached); err != nil {
		return nil, err
	}

	return field.ParseSchema(cached.Version, cached.Data)
}

func saveCachedSchema(filename, version string, data []byte) error {
	cached := schemaFile{
		Version: version,
		Data:    data,
	}

	raw, err := json.Marshal(&cached)
	if err != nil {
		return err
	}

	if err = os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return err
	}

	return os.WriteFile(filename, raw, 0o644)
}
