package warehouses

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/internal/credentials"
	"github.com/akuity/kargo/internal/image"
	"github.com/akuity/kargo/internal/indexer"
)

// fakeSelector implements the image.Selector interface for testing
type fakeSelector struct {
	matchesFn func(string) bool
	selectFn  func(context.Context) ([]kargoapi.DiscoveredImageReference, error)
}

func (f *fakeSelector) MatchesTag(tag string) bool {
	if f.matchesFn != nil {
		return f.matchesFn(tag)
	}
	return true
}

func (f *fakeSelector) Select(ctx context.Context) ([]kargoapi.DiscoveredImageReference, error) {
	if f.selectFn != nil {
		return f.selectFn(ctx)
	}
	return nil, nil
}

func TestRetainActiveFreightTags(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kargoapi.AddToScheme(scheme))

	testCases := []struct {
		name              string
		warehouse         *kargoapi.Warehouse
		subscription      kargoapi.ImageSubscription
		selector          image.Selector
		discoveredImages  []kargoapi.DiscoveredImageReference
		freightInCluster  []client.Object
		assertions        func(*testing.T, []kargoapi.DiscoveredImageReference, error)
	}{
		{
			name: "no active Freight",
			warehouse: &kargoapi.Warehouse{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-warehouse",
					Namespace: "test-namespace",
				},
			},
			subscription: kargoapi.ImageSubscription{
				RepoURL: "example.com/my-image",
			},
			selector: &fakeSelector{},
			discoveredImages: []kargoapi.DiscoveredImageReference{
				{Tag: "v1.0.0"},
				{Tag: "v1.0.1"},
			},
			freightInCluster: []client.Object{
				// Freight exists but is not active (no CurrentlyIn)
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-1",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v0.9.0"},
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: nil, // Not active
					},
				},
			},
			assertions: func(t *testing.T, images []kargoapi.DiscoveredImageReference, err error) {
				require.NoError(t, err)
				require.Len(t, images, 2)
				require.Equal(t, "v1.0.0", images[0].Tag)
				require.Equal(t, "v1.0.1", images[1].Tag)
			},
		},
		{
			name: "active Freight tag outside discovery window",
			warehouse: &kargoapi.Warehouse{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-warehouse",
					Namespace: "test-namespace",
				},
			},
			subscription: kargoapi.ImageSubscription{
				RepoURL: "example.com/my-image",
			},
			selector: &fakeSelector{},
			discoveredImages: []kargoapi.DiscoveredImageReference{
				{Tag: "v1.0.0", Digest: "sha256:abc123"},
				{Tag: "v1.0.1", Digest: "sha256:def456"},
			},
			freightInCluster: []client.Object{
				// Active Freight with an older tag not in discovered images
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-active",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v0.9.0", Digest: "sha256:old123"},
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"prod-stage": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
			},
			assertions: func(t *testing.T, images []kargoapi.DiscoveredImageReference, err error) {
				require.NoError(t, err)
				require.Len(t, images, 3, "Should include 2 discovered + 1 retained active tag")
				
				// Check that the retained tag is present
				tags := make([]string, len(images))
				for i, img := range images {
					tags[i] = img.Tag
				}
				require.Contains(t, tags, "v0.9.0", "Should retain active tag v0.9.0")
				require.Contains(t, tags, "v1.0.0")
				require.Contains(t, tags, "v1.0.1")
			},
		},
		{
			name: "active Freight tag already in discovered images",
			warehouse: &kargoapi.Warehouse{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-warehouse",
					Namespace: "test-namespace",
				},
			},
			subscription: kargoapi.ImageSubscription{
				RepoURL: "example.com/my-image",
			},
			selector: &fakeSelector{},
			discoveredImages: []kargoapi.DiscoveredImageReference{
				{Tag: "v1.0.0", Digest: "sha256:abc123"},
				{Tag: "v1.0.1", Digest: "sha256:def456"},
			},
			freightInCluster: []client.Object{
				// Active Freight with a tag that's already in discovered images
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-active",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v1.0.0", Digest: "sha256:abc123"},
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"prod-stage": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
			},
			assertions: func(t *testing.T, images []kargoapi.DiscoveredImageReference, err error) {
				require.NoError(t, err)
				require.Len(t, images, 2, "Should not duplicate already-discovered tags")
				require.Equal(t, "v1.0.0", images[0].Tag)
				require.Equal(t, "v1.0.1", images[1].Tag)
			},
		},
		{
			name: "multiple active Freight with different tags",
			warehouse: &kargoapi.Warehouse{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-warehouse",
					Namespace: "test-namespace",
				},
			},
			subscription: kargoapi.ImageSubscription{
				RepoURL: "example.com/my-image",
			},
			selector: &fakeSelector{},
			discoveredImages: []kargoapi.DiscoveredImageReference{
				{Tag: "v1.0.2", Digest: "sha256:latest"},
			},
			freightInCluster: []client.Object{
				// Active Freight in stage A
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-stage-a",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v1.0.0", Digest: "sha256:old1"},
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"stage-a": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
				// Active Freight in stage B
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-stage-b",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v1.0.1", Digest: "sha256:old2"},
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"stage-b": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
			},
			assertions: func(t *testing.T, images []kargoapi.DiscoveredImageReference, err error) {
				require.NoError(t, err)
				require.Len(t, images, 3, "Should include 1 discovered + 2 retained active tags")
				
				tags := make([]string, len(images))
				for i, img := range images {
					tags[i] = img.Tag
				}
				require.Contains(t, tags, "v1.0.0")
				require.Contains(t, tags, "v1.0.1")
				require.Contains(t, tags, "v1.0.2")
			},
		},
		{
			name: "active Freight from different warehouse ignored",
			warehouse: &kargoapi.Warehouse{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-warehouse",
					Namespace: "test-namespace",
				},
			},
			subscription: kargoapi.ImageSubscription{
				RepoURL: "example.com/my-image",
			},
			selector: &fakeSelector{},
			discoveredImages: []kargoapi.DiscoveredImageReference{
				{Tag: "v1.0.0"},
			},
			freightInCluster: []client.Object{
				// Active Freight from a different warehouse
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-other",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "other-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v0.9.0"},
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"prod-stage": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
			},
			assertions: func(t *testing.T, images []kargoapi.DiscoveredImageReference, err error) {
				require.NoError(t, err)
				require.Len(t, images, 1, "Should not include tags from other warehouses")
				require.Equal(t, "v1.0.0", images[0].Tag)
			},
		},
		{
			name: "active Freight with different image repo ignored",
			warehouse: &kargoapi.Warehouse{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-warehouse",
					Namespace: "test-namespace",
				},
			},
			subscription: kargoapi.ImageSubscription{
				RepoURL: "example.com/my-image",
			},
			selector: &fakeSelector{},
			discoveredImages: []kargoapi.DiscoveredImageReference{
				{Tag: "v1.0.0"},
			},
			freightInCluster: []client.Object{
				// Active Freight with a different image repository
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-active",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/different-image", Tag: "v0.9.0"},
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"prod-stage": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
			},
			assertions: func(t *testing.T, images []kargoapi.DiscoveredImageReference, err error) {
				require.NoError(t, err)
				require.Len(t, images, 1, "Should not include tags from different repos")
				require.Equal(t, "v1.0.0", images[0].Tag)
			},
		},
		{
			name: "active Freight tag filtered by selector",
			warehouse: &kargoapi.Warehouse{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-warehouse",
					Namespace: "test-namespace",
				},
			},
			subscription: kargoapi.ImageSubscription{
				RepoURL: "example.com/my-image",
			},
			selector: &fakeSelector{
				matchesFn: func(tag string) bool {
					// Only match tags starting with "v1."
					return len(tag) >= 3 && tag[:3] == "v1."
				},
			},
			discoveredImages: []kargoapi.DiscoveredImageReference{
				{Tag: "v1.0.0"},
			},
			freightInCluster: []client.Object{
				// Active Freight with tags that don't match selector
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-active-1",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v2.0.0"}, // Doesn't match
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"stage-a": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
				// Active Freight with tag that matches selector
				&kargoapi.Freight{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "freight-active-2",
						Namespace: "test-namespace",
					},
					Origin: kargoapi.FreightOrigin{
						Kind: kargoapi.FreightOriginKindWarehouse,
						Name: "test-warehouse",
					},
					Images: []kargoapi.Image{
						{RepoURL: "example.com/my-image", Tag: "v1.5.0"}, // Matches
					},
					Status: kargoapi.FreightStatus{
						CurrentlyIn: map[string]kargoapi.CurrentStage{
							"stage-b": {Since: &metav1.Time{Time: metav1.Now().Time}},
						},
					},
				},
			},
			assertions: func(t *testing.T, images []kargoapi.DiscoveredImageReference, err error) {
				require.NoError(t, err)
				require.Len(t, images, 2, "Should only retain tags matching selector")
				
				tags := make([]string, len(images))
				for i, img := range images {
					tags[i] = img.Tag
				}
				require.Contains(t, tags, "v1.0.0")
				require.Contains(t, tags, "v1.5.0")
				require.NotContains(t, tags, "v2.0.0")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create fake client with the Freight objects
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tc.freightInCluster...).
				WithIndex(
					&kargoapi.Freight{},
					indexer.FreightByWarehouseField,
					indexer.FreightByWarehouse,
				).
				Build()

			r := &reconciler{
				client:        fakeClient,
				credentialsDB: &credentials.FakeDB{},
			}

			images, err := r.retainActiveFreightTags(
				context.Background(),
				tc.warehouse,
				tc.subscription,
				tc.selector,
				tc.discoveredImages,
			)

			tc.assertions(t, images, err)
		})
	}
}
