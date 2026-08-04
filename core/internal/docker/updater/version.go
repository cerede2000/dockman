package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxRegistryTags        = 1000
	maxRegistryTagPages    = 10
	versionScanConcurrency = 4
)

var versionTagPattern = regexp.MustCompile(`^[vV]?(\d+)(?:\.(\d+))?(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

type VersionDiscoveryTarget struct {
	ContainerID     string
	Image           string
	Policy          string
	AllowPrerelease bool
}

type VersionDiscoveryResult struct {
	ContainerID string
	CurrentTag  string
	LatestTag   string
	Policy      string
	Available   bool
	Reason      string
}

type versionCacheEntry struct {
	tags      []string
	expiresAt time.Time
}

type versionInflight struct {
	done chan struct{}
	tags []string
	err  error
}

// RegistryVersionDiscovery keeps tag catalog traffic outside the digest
// scanner. Successful public catalogs are cached and bounded; no image is
// pulled and no Compose reference is ever modified by discovery.
type RegistryVersionDiscovery struct {
	ttl      time.Duration
	mu       sync.Mutex
	cache    map[string]versionCacheEntry
	inflight map[string]*versionInflight
}

func NewRegistryVersionDiscovery(ttl time.Duration) *RegistryVersionDiscovery {
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	return &RegistryVersionDiscovery{ttl: ttl, cache: make(map[string]versionCacheEntry), inflight: make(map[string]*versionInflight)}
}

func (d *RegistryVersionDiscovery) Discover(ctx context.Context, _ string, targets []VersionDiscoveryTarget) []VersionDiscoveryResult {
	if len(targets) == 0 {
		return []VersionDiscoveryResult{}
	}
	results := make([]VersionDiscoveryResult, len(targets))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(versionScanConcurrency, len(targets)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				target := targets[index]
				results[index] = d.discoverOne(ctx, target)
			}
		}()
	}
	for index := range targets {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return results
}

func (d *RegistryVersionDiscovery) discoverOne(ctx context.Context, target VersionDiscoveryTarget) VersionDiscoveryResult {
	result := VersionDiscoveryResult{ContainerID: target.ContainerID, Policy: target.Policy}
	ref, err := parseRegistryImageReference(target.Image)
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	result.CurrentTag = ref.tag
	current, ok := parseVersionTag(ref.tag)
	if !ok {
		result.Reason = "current tag is not a comparable semantic version"
		return result
	}
	tags, err := d.tags(ctx, ref)
	if err != nil {
		result.Reason = "version catalog: " + err.Error()
		return result
	}
	latest, found := newestAllowedTag(current, tags, target.Policy, target.AllowPrerelease)
	if !found {
		result.Reason = "no newer matching version tag"
		return result
	}
	result.LatestTag = latest.raw
	result.Available = true
	result.Reason = fmt.Sprintf("newer %s version tag available", target.Policy)
	return result
}

func (d *RegistryVersionDiscovery) tags(ctx context.Context, ref registryImageReference) ([]string, error) {
	key := ref.registry + "/" + ref.repo
	now := time.Now()
	d.mu.Lock()
	entry, found := d.cache[key]
	if found && now.Before(entry.expiresAt) {
		d.mu.Unlock()
		return slices.Clone(entry.tags), nil
	}
	if pending, running := d.inflight[key]; running {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending.done:
			return slices.Clone(pending.tags), pending.err
		}
	}
	pending := &versionInflight{done: make(chan struct{})}
	d.inflight[key] = pending
	d.mu.Unlock()
	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tags, err := RegistryTags(lookupCtx, ref)
	d.mu.Lock()
	if err == nil && len(d.cache) >= 256 {
		for existing := range d.cache {
			delete(d.cache, existing)
			break
		}
	}
	if err == nil {
		d.cache[key] = versionCacheEntry{tags: slices.Clone(tags), expiresAt: now.Add(d.ttl)}
	}
	pending.tags, pending.err = slices.Clone(tags), err
	delete(d.inflight, key)
	close(pending.done)
	d.mu.Unlock()
	return tags, err
}

func RegistryTags(ctx context.Context, ref registryImageReference) ([]string, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || req.URL.Scheme != "https" {
				return errors.New("unsafe or excessive registry redirect")
			}
			return nil
		},
	}
	return registryTagsWithClient(ctx, ref, client)
}

func registryTagsWithClient(ctx context.Context, ref registryImageReference, client *http.Client) ([]string, error) {
	endpoint := url.URL{Scheme: "https", Host: ref.registry, Path: "/v2/" + escapeRepositoryPath(ref.repo) + "/tags/list"}
	query := endpoint.Query()
	query.Set("n", "100")
	endpoint.RawQuery = query.Encode()
	authorization := ""
	var tags []string
	for page := 0; page < maxRegistryTagPages && endpoint.String() != ""; page++ {
		pageTags, challenge, next, status, err := requestRegistryTagsPage(ctx, client, endpoint.String(), authorization)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized && authorization == "" {
			token, tokenErr := anonymousBearerToken(ctx, client, challenge, ref.repo)
			if tokenErr != nil {
				return nil, tokenErr
			}
			authorization = "Bearer " + token
			page--
			continue
		}
		if status != http.StatusOK {
			return nil, registryStatusError(status)
		}
		for _, tag := range pageTags {
			if tag = strings.TrimSpace(tag); tag != "" && !slices.Contains(tags, tag) {
				tags = append(tags, tag)
				if len(tags) >= maxRegistryTags {
					return tags, nil
				}
			}
		}
		if next == "" {
			break
		}
		nextURL, err := endpoint.Parse(next)
		if err != nil || nextURL.Scheme != "https" || nextURL.Host != ref.registry {
			return nil, errors.New("registry returned an unsafe tag pagination link")
		}
		endpoint = *nextURL
	}
	return tags, nil
}

func requestRegistryTagsPage(ctx context.Context, client *http.Client, endpoint, authorization string) ([]string, string, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", "", 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Dockman/1.0")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, resp.Header.Get("WWW-Authenticate"), "", resp.StatusCode, nil
	}
	var payload struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, "", "", resp.StatusCode, fmt.Errorf("decode registry tag catalog: %w", err)
	}
	return payload.Tags, "", parseNextLink(resp.Header.Get("Link")), resp.StatusCode, nil
}

func parseNextLink(value string) string {
	for _, part := range strings.Split(value, ",") {
		sections := strings.Split(part, ";")
		relations := strings.ToLower(strings.Join(sections[1:], ";"))
		if len(sections) < 2 || (!strings.Contains(relations, `rel="next"`) && !strings.Contains(relations, `rel=next`)) {
			continue
		}
		return strings.Trim(strings.TrimSpace(sections[0]), "<>")
	}
	return ""
}

type semanticTag struct {
	raw        string
	major      uint64
	minor      uint64
	patch      uint64
	precision  int
	prerelease string
}

func parseVersionTag(value string) (semanticTag, bool) {
	match := versionTagPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return semanticTag{}, false
	}
	parts := [3]uint64{}
	precision := 1
	for index := range 3 {
		if match[index+1] == "" {
			continue
		}
		parsed, err := strconv.ParseUint(match[index+1], 10, 64)
		if err != nil {
			return semanticTag{}, false
		}
		parts[index] = parsed
		precision = index + 1
	}
	return semanticTag{raw: strings.TrimSpace(value), major: parts[0], minor: parts[1], patch: parts[2], precision: precision, prerelease: match[4]}, true
}

func newestAllowedTag(current semanticTag, tags []string, policy string, allowPrerelease bool) (semanticTag, bool) {
	var latest semanticTag
	found := false
	for _, value := range tags {
		candidate, ok := parseVersionTag(value)
		if !ok || (!allowPrerelease && candidate.prerelease != "") || compareSemanticTag(candidate, current) <= 0 || !versionPolicyAllows(current, candidate, policy) {
			continue
		}
		if !found || compareSemanticTag(candidate, latest) > 0 {
			latest, found = candidate, true
		}
	}
	return latest, found
}

func versionPolicyAllows(current, candidate semanticTag, policy string) bool {
	switch policy {
	case VersionPolicyPatch:
		if candidate.major != current.major {
			return false
		}
		return current.precision < 2 || candidate.minor == current.minor
	case VersionPolicyMinor:
		return candidate.major == current.major
	case VersionPolicyMajor:
		return true
	default:
		return false
	}
}

func compareSemanticTag(a, b semanticTag) int {
	for _, pair := range [][2]uint64{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if a.prerelease == b.prerelease {
		return 0
	}
	if a.prerelease == "" {
		return 1
	}
	if b.prerelease == "" {
		return -1
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

func comparePrerelease(a, b string) int {
	aParts, bParts := strings.Split(a, "."), strings.Split(b, ".")
	for index := 0; index < max(len(aParts), len(bParts)); index++ {
		if index >= len(aParts) {
			return -1
		}
		if index >= len(bParts) {
			return 1
		}
		aNumber, aErr := strconv.ParseUint(aParts[index], 10, 64)
		bNumber, bErr := strconv.ParseUint(bParts[index], 10, 64)
		switch {
		case aErr == nil && bErr == nil && aNumber != bNumber:
			if aNumber < bNumber {
				return -1
			}
			return 1
		case aErr == nil && bErr != nil:
			return -1
		case aErr != nil && bErr == nil:
			return 1
		case aParts[index] < bParts[index]:
			return -1
		case aParts[index] > bParts[index]:
			return 1
		}
	}
	return 0
}
