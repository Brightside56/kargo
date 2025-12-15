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

// retainActiveFreightTags augments the discovered images with tags from active
// Freight to ensure they remain selectable even if they fall outside the
// discovery window. Active Freight is defined as Freight that is currently in
// use by at least one Stage (Status.CurrentlyIn is non-empty).
//
// This function:
// 1. Queries active Freight associated with the Warehouse
// 2. Extracts image tags for the given repository from those Freight
// 3. Merges and deduplicates these tags with the discovered images
func (r *reconciler) retainActiveFreightTags(
	ctx context.Context,
	warehouse *kargoapi.Warehouse,
	sub kargoapi.ImageSubscription,
	selector image.Selector,
	discoveredImages []kargoapi.DiscoveredImageReference,
) ([]kargoapi.DiscoveredImageReference, error) {
	logger := logging.LoggerFromContext(ctx).WithValues(
		"warehouse", warehouse.Name,
		"repo", sub.RepoURL,
	)

	// Query active Freight for this warehouse
	activeFreight, err := api.ListActiveFreightByWarehouse(
		ctx,
		r.client,
		warehouse.Namespace,
		warehouse.Name,
	)
	if err != nil {
		return nil, fmt.Errorf("error listing active Freight: %w", err)
	}

	if len(activeFreight) == 0 {
		logger.Trace("no active Freight found")
		return discoveredImages, nil
	}

	logger.Trace("found active Freight", "count", len(activeFreight))

	// Extract unique tags from active Freight for this image repository
	activeTags := make(map[string]bool)
	for _, freight := range activeFreight {
		for _, img := range freight.Images {
			if img.RepoURL == sub.RepoURL && img.Tag != "" {
				// Only include tags that match the selector criteria
				if selector.MatchesTag(img.Tag) {
					activeTags[img.Tag] = true
				}
			}
		}
	}

	if len(activeTags) == 0 {
		logger.Trace("no active tags found for repository")
		return discoveredImages, nil
	}

	logger.Trace("found active tags", "count", len(activeTags))

	// Build a map of already-discovered tags for deduplication
	discoveredTags := make(map[string]kargoapi.DiscoveredImageReference)
	for _, img := range discoveredImages {
		discoveredTags[img.Tag] = img
	}

	// Add active tags that aren't already in the discovered set
	// Note: We intentionally don't fetch full metadata for these retained tags
	// to avoid unbounded registry lookups. They'll have minimal metadata but
	// will still be selectable for Freight assembly.
	var addedCount int
	for tag := range activeTags {
		if _, exists := discoveredTags[tag]; !exists {
			// Add the tag with minimal metadata
			// The tag is known to exist in active Freight, so it's safe to include
			discoveredImages = append(discoveredImages, kargoapi.DiscoveredImageReference{
				Tag: tag,
				// Digest and CreatedAt are intentionally left empty as we're not
				// fetching full metadata to avoid performance issues
			})
			addedCount++
		}
	}

	if addedCount > 0 {
		logger.Debug(
			"retained active Freight tags",
			"added", addedCount,
			"total", len(discoveredImages),
		)
	}

	return discoveredImages, nil
}

// discoverImages discovers the latest suitable images for the given image
// subscriptions. It returns a list of image discovery results, one for each
// subscription.
//
// For subscriptions using the NewestBuild strategy, this function also includes
// image tags referenced by "active" Freight (Freight currently in use by any Stage)
// to ensure older tags that have fallen outside the discovery window remain
// selectable.
func (r *reconciler) discoverImages(
	ctx context.Context,
	warehouse *kargoapi.Warehouse,
	subs []kargoapi.RepoSubscription,
) ([]kargoapi.ImageDiscoveryResult, error) {
	results := make([]kargoapi.ImageDiscoveryResult, 0, len(subs))

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

		// For NewestBuild strategy, augment the discovered images with tags from
		// active Freight to ensure they remain selectable even if they fall outside
		// the discovery window.
		if sub.ImageSelectionStrategy == kargoapi.ImageSelectionStrategyNewestBuild {
			images, err = r.retainActiveFreightTags(ctx, warehouse, sub, selector, images)
			if err != nil {
				return nil, fmt.Errorf(
					"error retaining active Freight tags for image %q: %w",
					sub.RepoURL,
					err,
				)
			}
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
