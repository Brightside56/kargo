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
// This function supports two modes:
// 1. Fresh discovery: when activeFreight is provided (non-nil), it queries active tags
//    from the Freight and marks them as retained in the output.
// 2. Cached mode: when activeFreight is nil, it reuses previously retained tags from
//    previousDiscovery to avoid Kubernetes API calls.
//
// The retained tags are stored in the Warehouse CR status and reused across
// reconciliations until a fresh discovery is triggered.
func (r *reconciler) retainActiveFreightTags(
	ctx context.Context,
	sub kargoapi.ImageSubscription,
	selector image.Selector,
	discoveredImages []kargoapi.DiscoveredImageReference,
	activeFreight []kargoapi.Freight,
	previousDiscovery []kargoapi.DiscoveredImageReference,
) ([]kargoapi.DiscoveredImageReference, error) {
	logger := logging.LoggerFromContext(ctx).WithValues("repo", sub.RepoURL)

	// Build a map of already-discovered tags for deduplication
	discoveredTags := make(map[string]kargoapi.DiscoveredImageReference)
	for _, img := range discoveredImages {
		discoveredTags[img.Tag] = img
	}

	var activeTags map[string]bool
	var addedCount int

	// Mode 1: Fresh discovery from active Freight (when activeFreight is provided)
	if activeFreight != nil {
		logger.Trace("querying active tags from Freight", "freightCount", len(activeFreight))

		// Extract unique tags from active Freight for this image repository
		activeTags = make(map[string]bool)
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
			logger.Trace("no active tags found in Freight")
			return discoveredImages, nil
		}

		logger.Trace("found active tags from Freight", "count", len(activeTags))

		// Add active tags that aren't already in the discovered set
		for tag := range activeTags {
			if _, exists := discoveredTags[tag]; !exists {
				discoveredImages = append(discoveredImages, kargoapi.DiscoveredImageReference{
					Tag:                       tag,
					RetainedFromActiveFreight: true,
					// Digest and CreatedAt are intentionally left empty as we're not
					// fetching full metadata to avoid performance issues
				})
				addedCount++
			}
		}
	} else {
		// Mode 2: Reuse cached retained tags from previous discovery
		logger.Trace("reusing cached retained tags from previous discovery")

		for _, prevImg := range previousDiscovery {
			// Only reuse tags that were marked as retained and still match selector
			if prevImg.RetainedFromActiveFreight && selector.MatchesTag(prevImg.Tag) {
				if _, exists := discoveredTags[prevImg.Tag]; !exists {
					discoveredImages = append(discoveredImages, kargoapi.DiscoveredImageReference{
						Tag:                       prevImg.Tag,
						RetainedFromActiveFreight: true,
						Digest:                    prevImg.Digest,
						CreatedAt:                 prevImg.CreatedAt,
						Annotations:               prevImg.Annotations,
					})
					addedCount++
				}
			}
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
// This function includes image tags referenced by "active" Freight (Freight
// currently in use by any Stage) to ensure older tags that have fallen outside
// the discovery window remain selectable. This applies to all image selection
// strategies to prevent situations where an actively deployed version becomes
// unavailable for rollback or re-deployment.
//
// To minimize Kubernetes API calls, this function leverages cached information
// from the previous reconciliation stored in the Warehouse CR's status. It only
// queries active Freight from the API when fresh discovery is triggered or when
// no cached data exists.
func (r *reconciler) discoverImages(
	ctx context.Context,
	warehouse *kargoapi.Warehouse,
	subs []kargoapi.RepoSubscription,
) ([]kargoapi.ImageDiscoveryResult, error) {
	results := make([]kargoapi.ImageDiscoveryResult, 0, len(subs))

	// Determine if we need to query active Freight from the API. We do this when:
	// 1. This is a fresh discovery (no previous discovered artifacts)
	// 2. The warehouse spec changed (observed generation differs)
	needsFreshActiveFreight := warehouse.Status.DiscoveredArtifacts == nil ||
		warehouse.Status.ObservedGeneration != warehouse.Generation

	var activeFreight []kargoapi.Freight
	var err error

	// Only query the API if we need fresh data. Otherwise, we'll reuse the
	// retained tags from the previous reconciliation that are already cached
	// in the Warehouse CR status.
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

		// Find previous discovery results for this repo URL to reuse cached retained tags
		var previousDiscovery []kargoapi.DiscoveredImageReference
		if warehouse.Status.DiscoveredArtifacts != nil {
			for _, prevResult := range warehouse.Status.DiscoveredArtifacts.Images {
				if prevResult.RepoURL == sub.RepoURL {
					previousDiscovery = prevResult.References
					break
				}
			}
		}

		// Augment the discovered images with tags from active Freight to ensure
		// they remain selectable even if they fall outside the discovery window.
		// This applies to all strategies to prevent situations where an actively
		// deployed version becomes unavailable for rollback or re-deployment.
		//
		// If activeFreight is nil (cached mode), the function will reuse retained
		// tags from previousDiscovery without querying the Kubernetes API.
		images, err = r.retainActiveFreightTags(ctx, sub, selector, images, activeFreight, previousDiscovery)
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
