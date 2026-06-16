package firmwareproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const FirmwareBundleArtifactType = "application/vnd.openchami.firmware.bundle.v1+json"

type HTTPStatusError struct {
	StatusCode int
	Message    string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return fmt.Sprintf("http status %d", e.StatusCode)
	}
	return fmt.Sprintf("http status %d: %s", e.StatusCode, e.Message)
}

type payloadLocation struct {
	Repository string
}

type SearchResult struct {
	Reference     string            `json:"reference"`
	PayloadDigest string            `json:"payloadDigest"`
	Annotations   map[string]string `json:"annotations"`
}

var payloadIndex sync.Map

func SearchFirmware(ctx context.Context, registryHost string, filters map[string]string, logf func(format string, args ...any)) ([]SearchResult, error) {
	registryHost = strings.TrimSpace(registryHost)
	if registryHost == "" {
		return nil, &HTTPStatusError{StatusCode: 400, Message: "registry query parameter is required"}
	}

	if logf == nil {
		logf = func(string, ...any) {}
	}

	reg, err := remote.NewRegistry(registryHost)
	if err != nil {
		return nil, fmt.Errorf("create ORAS registry client: %w", err)
	}
	reg.PlainHTTP = isLoopbackRegistry(registryHost)

	if err := reg.Ping(ctx); err != nil {
		return nil, registryUnavailableError(fmt.Errorf("ping registry %q: %w", registryHost, err))
	}

	results := make([]SearchResult, 0)
	err = reg.Repositories(ctx, "", func(repos []string) error {
		for _, repoName := range repos {
			repoResults, scanErr := scanRepository(ctx, registryHost, repoName, filters, logf)
			if scanErr != nil {
				if isNotFound(scanErr) {
					logf("firmware-search: skipping repository %q due to not found: %v", repoName, scanErr)
					continue
				}
				return scanErr
			}
			results = append(results, repoResults...)
		}
		return nil
	})
	if err != nil {
		if isNotFound(err) {
			logf("firmware-search: catalog changed during scan, continuing with partial results: %v", err)
			return results, nil
		}
		return nil, registryUnavailableError(fmt.Errorf("list repositories: %w", err))
	}

	return results, nil
}

func ResolvePayload(ctx context.Context, ociReference string) (string, error) {
	parsed, err := registry.ParseReference(ociReference)
	if err != nil {
		return "", fmt.Errorf("parse OCI reference: %w", err)
	}

	repo, err := remote.NewRepository(parsed.Registry + "/" + parsed.Repository)
	if err != nil {
		return "", fmt.Errorf("create ORAS repository client: %w", err)
	}
	repo.PlainHTTP = isLoopbackRegistry(parsed.Registry)

	reference := parsed.ReferenceOrDefault()
	_, manifestBytes, err := oras.FetchBytes(ctx, repo, reference, oras.FetchBytesOptions{})
	if err != nil {
		return "", classifyORASError(fmt.Errorf("fetch manifest for %q: %w", reference, err))
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return "", fmt.Errorf("decode OCI manifest: %w", err)
	}
	if manifest.ArtifactType != FirmwareBundleArtifactType {
		return "", &HTTPStatusError{
			StatusCode: 400,
			Message:    fmt.Sprintf("unexpected artifactType %q (expected %q)", manifest.ArtifactType, FirmwareBundleArtifactType),
		}
	}
	if len(manifest.Layers) == 0 {
		return "", &HTTPStatusError{StatusCode: 400, Message: "firmware bundle has no layers"}
	}

	payloadDigest := manifest.Layers[0].Digest.String()
	payloadIndex.Store(payloadDigest, payloadLocation{Repository: parsed.Registry + "/" + parsed.Repository})

	return payloadDigest, nil
}

func scanRepository(ctx context.Context, registryHost, repoName string, filters map[string]string, logf func(format string, args ...any)) ([]SearchResult, error) {
	repo, err := remote.NewRepository(registryHost + "/" + repoName)
	if err != nil {
		return nil, fmt.Errorf("create repository client for %q: %w", repoName, err)
	}
	repo.PlainHTTP = isLoopbackRegistry(registryHost)

	results := make([]SearchResult, 0)
	err = repo.Tags(ctx, "", func(tags []string) error {
		for _, tag := range tags {
			result, ok, fetchErr := scanTag(ctx, repo, registryHost, repoName, tag, filters)
			if fetchErr != nil {
				if isNotFound(fetchErr) {
					logf("firmware-search: skipping %s/%s:%s due to not found: %v", registryHost, repoName, tag, fetchErr)
					continue
				}
				return fetchErr
			}
			if ok {
				results = append(results, result)
			}
		}
		return nil
	})
	if err != nil {
		if isNotFound(err) {
			logf("firmware-search: tags disappeared in repository %q: %v", repoName, err)
			return results, nil
		}
		return nil, err
	}

	return results, nil
}

func scanTag(ctx context.Context, repo *remote.Repository, registryHost, repoName, tag string, filters map[string]string) (SearchResult, bool, error) {
	_, manifestBytes, err := oras.FetchBytes(ctx, repo, tag, oras.FetchBytesOptions{})
	if err != nil {
		return SearchResult{}, false, err
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return SearchResult{}, false, fmt.Errorf("decode manifest for %s/%s:%s: %w", registryHost, repoName, tag, err)
	}

	if manifest.ArtifactType != FirmwareBundleArtifactType {
		return SearchResult{}, false, nil
	}
	if len(manifest.Layers) == 0 {
		return SearchResult{}, false, nil
	}

	annotations := map[string]string{}
	for key, value := range manifest.Annotations {
		annotations[key] = value
	}

	if !annotationsMatch(annotations, filters) {
		return SearchResult{}, false, nil
	}

	return SearchResult{
		Reference:     fmt.Sprintf("%s/%s:%s", registryHost, repoName, tag),
		PayloadDigest: manifest.Layers[0].Digest.String(),
		Annotations:   annotations,
	}, true, nil
}

func annotationsMatch(annotations, filters map[string]string) bool {
	for key, expected := range filters {
		if actual, ok := annotations[key]; !ok || actual != expected {
			return false
		}
	}
	return true
}

func StreamPayloadLayer(ctx context.Context, digestStr string) (io.ReadCloser, int64, error) {
	if _, parseErr := digest.Parse(digestStr); parseErr != nil {
		return nil, 0, &HTTPStatusError{StatusCode: 400, Message: fmt.Sprintf("invalid digest %q", digestStr)}
	}

	locAny, found := payloadIndex.Load(digestStr)
	if !found {
		return nil, 0, &HTTPStatusError{StatusCode: 404, Message: "unknown payload digest"}
	}
	loc, ok := locAny.(payloadLocation)
	if !ok {
		return nil, 0, fmt.Errorf("invalid payload index entry for digest %q", digestStr)
	}

	repo, err := remote.NewRepository(loc.Repository)
	if err != nil {
		return nil, 0, fmt.Errorf("create ORAS repository client: %w", err)
	}
	repo.PlainHTTP = isLoopbackRegistry(repo.Reference.Registry)

	desc, err := repo.Blobs().Resolve(ctx, digestStr)
	if err != nil {
		return nil, 0, classifyORASError(fmt.Errorf("resolve payload layer %q: %w", digestStr, err))
	}

	rc, err := repo.Blobs().Fetch(ctx, desc)
	if err != nil {
		return nil, 0, classifyORASError(fmt.Errorf("stream payload layer %q: %w", digestStr, err))
	}

	return rc, desc.Size, nil
}

func isLoopbackRegistry(registryHost string) bool {
	host := registryHost
	if strings.HasPrefix(host, "[") && strings.Contains(host, "]") {
		trimmed := strings.TrimPrefix(host, "[")
		host = strings.SplitN(trimmed, "]", 2)[0]
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	host = strings.TrimSpace(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func isNotFound(err error) bool {
	if errors.Is(err, errdef.ErrNotFound) {
		return true
	}

	statusCode, ok := extractStatusCode(err)
	return ok && statusCode == 404
}

func registryUnavailableError(err error) error {
	if err == nil {
		return nil
	}
	return &HTTPStatusError{StatusCode: 503, Message: err.Error()}
}

func extractStatusCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}

	message := strings.ToLower(err.Error())
	marker := "status code "
	idx := strings.Index(message, marker)
	if idx < 0 {
		return 0, false
	}

	remainder := message[idx+len(marker):]
	number := strings.Builder{}
	for _, ch := range remainder {
		if ch < '0' || ch > '9' {
			break
		}
		number.WriteRune(ch)
	}
	if number.Len() == 0 {
		return 0, false
	}

	code, convErr := strconv.Atoi(number.String())
	if convErr != nil {
		return 0, false
	}
	return code, true
}

func classifyORASError(err error) error {
	if err == nil {
		return nil
	}

	if isNotFound(err) {
		return &HTTPStatusError{StatusCode: 400, Message: err.Error()}
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &HTTPStatusError{StatusCode: 503, Message: err.Error()}
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "status code 400") ||
		strings.Contains(msg, "status code 401") ||
		strings.Contains(msg, "status code 403") ||
		strings.Contains(msg, "status code 405") ||
		strings.Contains(msg, "status code 409") {
		return &HTTPStatusError{StatusCode: 400, Message: err.Error()}
	}

	if strings.Contains(msg, "status code 429") ||
		strings.Contains(msg, "status code 500") ||
		strings.Contains(msg, "status code 502") ||
		strings.Contains(msg, "status code 503") ||
		strings.Contains(msg, "status code 504") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "temporary") {
		return &HTTPStatusError{StatusCode: 503, Message: err.Error()}
	}

	return err
}
