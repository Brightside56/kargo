package warehouses

import (
	"context"
	"fmt"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/internal/api"
	"github.com/akuity/kargo/internal/credentials"
	"github.com/akuity/kargo/internal/image"
	"github.com/akuity/kargo/internal/logging"
)

// retainActiveFreightTags adds tags from active Freight to discovered images.
// Active Freight = currently deployed (Status.CurrentlyIn non-empty).
func (r *reconciler) retainActiveFreightTags(
	ctx context.Context,
	sub kargoapi.ImageSubscription,
	selector image.Selector,
	discoveredImages []kargoapi.DiscoveredImageReference,
	activeFreight []kargoapi.Freight,
) ([]kargoapi.DiscoveredImageReference, error) {
	if len(activeFreight) == 0 {
		return discoveredImages, nil
	}

	logger := logging.LoggerFromContext(ctx).WithValues("repo", sub.RepoURL)

	// Build a map of already-discovered tags for deduplication
	discoveredTags := make(map[string]struct{})
	for _, img := range discoveredImages {
		discoveredTags[img.Tag] = struct{}{}
	}

	// Extract unique tags from active Freight for this image repository
	activeTags := make(map[string]bool)
	for _, freight := range activeFreight {
		for _, img := range freight.Images {
			if img.RepoURL == sub.RepoURL && img.Tag != "" && selector.MatchesTag(img.Tag) {
				activeTags[img.Tag] = true
			}
		}
	}

	if len(activeTags) == 0 {
		return discoveredImages, nil
	}

	// Add active tags that aren't already in the discovered set
	var addedCount int
	for tag := range activeTags {
		if _, exists := discoveredTags[tag]; !exists {
			discoveredImages = append(discoveredImages, kargoapi.DiscoveredImageReference{
				Tag:               tag,
				FromActiveFreight: true,
				// Digest/CreatedAt omitted to avoid registry lookups
			})
			addedCount++
		}
	}

	if addedCount > 0 {
		logger.Debug("retained active Freight tags", "added", addedCount, "total", len(discoveredImages))
	}

	return discoveredImages, nil
}

// discoverImages discovers images for subscriptions, retaining tags from
// active Freight to ensure deployed versions remain available for rollback.
// Uses CR-based caching to minimize K8s API calls.
func (r *reconciler) discoverImages(
	ctx context.Context,
	warehouse *kargoapi.Warehouse,
	subs []kargoapi.RepoSubscription,
) ([]kargoapi.ImageDiscoveryResult, error) {
	results := make([]kargoapi.ImageDiscoveryResult, 0, len(subs))

	// Query active Freight only when warehouse would do full discovery refresh
	// (aligns with shouldDiscoverArtifacts logic: first time, spec change, or interval elapsed)
	needsFreshActiveFreight := warehouse.Status.DiscoveredArtifacts == nil ||
		warehouse.Status.ObservedGeneration != warehouse.Generation

	var activeFreight []kargoapi.Freight
	var err error

	if needsFreshActiveFreight {
		activeFreight, err = api.ListActiveFreightByWarehouse(
			ctx,
			r.client,
			warehouse.Namespace,
			warehouse.Name,
		)
		if err != nil {
			return nil, fmt.Errorf("error listing active Freight: %w", err)
		}
	}

	for _, s := range subs {
		if s.Image == nil {
			continue
		}
		sub := *s.Image

		logger := logging.LoggerFromContext(ctx).WithValues("repo", sub.RepoURL)

		// Obtain credentials for the image repository.
		creds, err := r.credentialsDB.Get(ctx, warehouse.Namespace, credentials.TypeImage, sub.RepoURL)
		if err != nil {
			return nil, fmt.Errorf(
				"error obtaining credentials for image repo %q: %w",
				sub.RepoURL,
				err,
			)
		}
		var regCreds *image.Credentials
		if creds != nil {
			regCreds = &image.Credentials{
				Username: creds.Username,
				Password: creds.Password,
			}
			logger.Debug("obtained credentials for image repo")
		} else {
			logger.Debug("found no credentials for image repo")
		}

		selector, err := image.NewSelector(sub, regCreds)
		if err != nil {
			return nil, fmt.Errorf(
				"error obtaining selector for image %q: %w",
				sub.RepoURL,
				err,
			)
		}
		images, err := selector.Select(ctx)
		if err != nil {
			return nil, fmt.Errorf(
				"error discovering newest applicable images %q: %w",
				sub.RepoURL,
				err,
			)
		}

		// Retain active Freight tags
		images, err = r.retainActiveFreightTags(ctx, sub, selector, images, activeFreight)
		if err != nil {
			return nil, fmt.Errorf(
				"error retaining active Freight tags for image %q: %w",
				sub.RepoURL,
				err,
			)
		}

		if len(images) == 0 {
			results = append(results, kargoapi.ImageDiscoveryResult{
				RepoURL:  sub.RepoURL,
				Platform: sub.Platform,
			})
			logger.Debug("discovered no images")
			continue
		}

		results = append(results, kargoapi.ImageDiscoveryResult{
			RepoURL:    sub.RepoURL,
			Platform:   sub.Platform,
			References: images,
		})
		logger.Debug(
			"discovered images",
			"count", len(images),
		)
	}

	return results, nil
}
