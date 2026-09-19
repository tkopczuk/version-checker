package version

import (
	"fmt"

	"github.com/jetstack/version-checker/pkg/api"
	"github.com/jetstack/version-checker/pkg/version/semver"
)

// latestSemver will return the latest ImageTag based on the given options
// restriction, using semver. This should not be used if UseSHA has been
// enabled.
func latestSemver(opts *api.Options, tags []api.ImageTag) (*api.ImageTag, error) {
	var (
		latestImageTag *api.ImageTag
		latestV        *semver.SemVer
	)

	for i := range tags {
		candidate, ok := imageForPlatform(&tags[i], opts)
		if !ok {
			continue
		}

		v := semver.Parse(candidate.Tag)

		if shouldSkipTag(opts, v) {
			continue
		}

		if isBetterSemVer(opts, latestV, v, latestImageTag, candidate) {
			latestV = v
			latestImageTag = candidate
		}
	}

	if latestImageTag == nil {
		return nil, fmt.Errorf("no suitable version found")
	}

	return latestImageTag, nil
}

// latestSHA will return the latest ImageTag based on image timestamps.
func latestSHA(opts *api.Options, tags []api.ImageTag) (*api.ImageTag, error) {
	var latestTag *api.ImageTag

	for i := range tags {
		candidate, ok := imageForPlatform(&tags[i], opts)
		if !ok {
			continue
		}

		// Filter out SBOM and Attestation/Sig's...
		if shouldSkipSHA(opts, candidate.Tag) {
			continue
		}

		if latestTag == nil || candidate.Timestamp.After(latestTag.Timestamp) {
			latestTag = candidate
		}
	}

	return latestTag, nil
}

// imageForPlatform keeps only the manifests for the configured platform. A
// shallow copy prevents filtering one check from changing the cached tag.
func imageForPlatform(tag *api.ImageTag, opts *api.Options) (*api.ImageTag, bool) {
	if opts == nil || opts.OS == nil || opts.Architecture == nil {
		return tag, true
	}

	matches := func(image *api.ImageTag) bool {
		// Some registries do not expose platform metadata. Preserve their tags
		// rather than making those registries unusable.
		if image.OS == "" || image.Architecture == "" {
			return true
		}

		return image.OS == *opts.OS && image.Architecture == *opts.Architecture
	}

	if len(tag.Children) == 0 {
		return tag, matches(tag)
	}

	filtered := *tag
	filtered.Children = nil
	for _, child := range tag.Children {
		if matches(child) {
			filtered.Children = append(filtered.Children, child)
		}
	}

	return &filtered, len(filtered.Children) > 0
}
