package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	kustypes "sigs.k8s.io/kustomize/api/types"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/promotion"
	"github.com/akuity/kargo/pkg/x/promotion/runner/builtin"
)

func Test_kustomizeImageSetter_convert(t *testing.T) {
	tests := []validationTestCase{
		{
			name:   "path is not specified",
			config: promotion.Config{},
			expectedProblems: []string{
				"(root): path is required",
			},
		},
		{
			name: "path is empty",
			config: promotion.Config{
				"path": "",
			},
			expectedProblems: []string{
				"path: String length must be greater than or equal to 1",
			},
		},
		{
			name: "image not specified",
			config: promotion.Config{
				"images": []promotion.Config{{}},
			},
			expectedProblems: []string{
				"images.0: image is required",
			},
		},
		{
			name: "image is empty",
			config: promotion.Config{
				"images": []promotion.Config{{
					"image": "",
				}},
			},
			expectedProblems: []string{
				"images.0.image: String length must be greater than or equal to 1",
			},
		},
		{
			name: "digest and tag are both not specified",
			config: promotion.Config{
				"images": []promotion.Config{{
					"image": "fake-image",
				}},
			},
			expectedProblems: []string{
				"images.0: Must validate one and only one schema (oneOf)",
			},
		},
		{
			name: "digest and tag are both empty",
			config: promotion.Config{
				"images": []promotion.Config{{
					"image":  "fake-image",
					"digest": "",
					"tag":    "",
				}},
			},
			expectedProblems: []string{
				"images.0: Must validate one and only one schema (oneOf)",
			},
		},
		{
			name: "digest and tag are both specified",
			// These should be mutually exclusive.
			config: promotion.Config{
				"images": []promotion.Config{{
					"digest": "fake-digest",
					"tag":    "fake-tag",
				}},
			},
			expectedProblems: []string{
				"images.0: Must validate one and only one schema (oneOf)",
			},
		},
		{
			name: "valid kitchen sink",
			config: promotion.Config{
				"path": "fake-path",
				"images": []promotion.Config{
					{
						"image":  "fake-image-2",
						"digest": "fake-digest",
					},
					{
						"image":  "fake-image-3",
						"digest": "fake-digest",
						"tag":    "",
					},
					{
						"image": "fake-image-4",
						"tag":   "fake-tag",
					},
					{
						"image":  "fake-image-5",
						"digest": "",
						"tag":    "fake-tag",
					},
				},
			},
		},
	}

	r := newKustomizeImageSetter(nil)
	runner, ok := r.(*kustomizeImageSetter)
	require.True(t, ok)

	runValidationTests(t, runner.convert, tests)
}

func Test_kustomizeImageSetter_run(t *testing.T) {
	const testNamespace = "test-project-run"

	scheme := runtime.NewScheme()
	require.NoError(t, kargoapi.AddToScheme(scheme))

	tests := []struct {
		name       string
		setupFiles func(t *testing.T, workDir string)
		cfg        builtin.KustomizeSetImageConfig
		client     client.Client
		stepCtx    *promotion.StepContext
		assertions func(*testing.T, string, promotion.StepResult, error)
	}{
		{
			name: "successfully sets image",
			setupFiles: func(t *testing.T, workDir string) {
				kustomizationContent := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
`
				err := os.WriteFile(filepath.Join(workDir, "kustomization.yaml"), []byte(kustomizationContent), 0o600)
				require.NoError(t, err)
			},
			cfg: builtin.KustomizeSetImageConfig{
				Path:   ".",
				Images: []builtin.Image{{Image: "nginx", Tag: "1.21.0"}},
			},
			client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
				mockWarehouse(testNamespace, "warehouse1", kargoapi.WarehouseSpec{
					Subscriptions: []kargoapi.RepoSubscription{
						{Image: &kargoapi.ImageSubscription{RepoURL: "nginx"}},
					},
				}),
			).Build(),
			stepCtx: &promotion.StepContext{
				Project: testNamespace,
				FreightRequests: []kargoapi.FreightRequest{
					{Origin: kargoapi.FreightOrigin{Name: "warehouse1", Kind: "Warehouse"}},
				},
				Freight: kargoapi.FreightCollection{
					Freight: map[string]kargoapi.FreightReference{
						"Warehouse/warehouse1": {
							Origin: kargoapi.FreightOrigin{Kind: "Warehouse", Name: "warehouse1"},
							Images: []kargoapi.Image{{RepoURL: "nginx", Tag: "1.21.0", Digest: "sha256:123"}},
						},
					},
				},
			},
			assertions: func(t *testing.T, workDir string, result promotion.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, promotion.StepResult{
					Status: kargoapi.PromotionStepStatusSucceeded,
					Output: map[string]any{
						"commitMessage": "Updated . to use new image\n\n- nginx:1.21.0",
					},
				}, result)

				b, err := os.ReadFile(filepath.Join(workDir, "kustomization.yaml"))
				require.NoError(t, err)
				assert.Contains(t, string(b), "newTag: 1.21.0")
			},
		},
		{
			name: "automatically sets image",
			setupFiles: func(t *testing.T, workDir string) {
				kustomizationContent := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
`
				err := os.WriteFile(filepath.Join(workDir, "kustomization.yaml"), []byte(kustomizationContent), 0o600)
				require.NoError(t, err)
			},
			cfg: builtin.KustomizeSetImageConfig{
				Path:   ".",
				Images: nil, // Automatically set all images
			},
			stepCtx: &promotion.StepContext{
				Freight: kargoapi.FreightCollection{
					Freight: map[string]kargoapi.FreightReference{
						"Warehouse/warehouse1": {
							Images: []kargoapi.Image{{RepoURL: "nginx", Digest: "sha256:123"}},
						},
						"Warehouse/warehouse2": {
							Images: []kargoapi.Image{{RepoURL: "redis", Tag: "6.2.5"}},
						},
					},
				},
			},
			assertions: func(t *testing.T, workDir string, result promotion.StepResult, err error) {
				require.NoError(t, err)
				assert.Equal(t, promotion.StepResult{
					Status: kargoapi.PromotionStepStatusSucceeded,
					Output: map[string]any{
						"commitMessage": "Updated . to use new images\n\n- nginx@sha256:123\n- redis:6.2.5",
					},
				}, result)

				b, err := os.ReadFile(filepath.Join(workDir, "kustomization.yaml"))
				require.NoError(t, err)
				assert.Contains(t, string(b), "newTag: 6.2.5")
				assert.Contains(t, string(b), "digest: sha256:123")
			},
		},
		{
			name: "Kustomization file not found",
			cfg: builtin.KustomizeSetImageConfig{
				Path: ".",
				Images: []builtin.Image{
					{Image: "nginx"},
				},
			},
			client:  fake.NewClientBuilder().WithScheme(scheme).Build(),
			stepCtx: &promotion.StepContext{Project: testNamespace},
			assertions: func(t *testing.T, _ string, result promotion.StepResult, err error) {
				require.ErrorContains(t, err, "could not discover kustomization file:")
				assert.Equal(t, promotion.StepResult{Status: kargoapi.PromotionStepStatusErrored}, result)
			},
		},
	}

	runner := &kustomizeImageSetter{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.stepCtx.WorkDir = t.TempDir()
			if tt.setupFiles != nil {
				tt.setupFiles(t, tt.stepCtx.WorkDir)
			}
			result, err := runner.run(context.Background(), tt.stepCtx, tt.cfg)
			tt.assertions(t, tt.stepCtx.WorkDir, result, err)
		})
	}
}

func Test_kustomizeImageSetter_buildTargetImages(t *testing.T) {
	tests := []struct {
		name       string
		images     []builtin.Image
		assertions func(*testing.T, map[string]kustypes.Image)
	}{
		{
			name: "digest or tag specified",
			images: []builtin.Image{
				{
					Image: "nginx",
					Tag:   "fake-tag",
				},
				{
					Image:  "redis",
					Digest: "fake-digest",
				},
			},
			assertions: func(t *testing.T, result map[string]kustypes.Image) {
				assert.Equal(t, map[string]kustypes.Image{
					"nginx": {Name: "nginx", NewTag: "fake-tag"},
					"redis": {Name: "redis", Digest: "fake-digest"},
				}, result)
			},
		},
	}

	runner := &kustomizeImageSetter{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := runner.buildTargetImagesFromConfig(tt.images)
			tt.assertions(t, result)
		})
	}
}

func Test_kustomizeImageSetter_buildTargetImagesAutomatically(t *testing.T) {
	const testNamespace = "test-project"

	tests := []struct {
		name              string
		freightReferences map[string]kargoapi.FreightReference
		freightRequests   []kargoapi.FreightRequest
		objects           []runtime.Object
		assertions        func(*testing.T, map[string]kustypes.Image, error)
	}{
		{
			name: "successfully builds target images",
			freightReferences: map[string]kargoapi.FreightReference{
				"Warehouse/warehouse1": {
					Images: []kargoapi.Image{
						{RepoURL: "nginx", Tag: "1.21.0", Digest: "sha256:abcdef1234567890"},
					},
				},
				"Warehouse/warehouse2": {
					Images: []kargoapi.Image{
						{RepoURL: "redis", Tag: "6.2.5"},
					},
				},
				"Warehouse/warehouse3": {
					Images: []kargoapi.Image{
						{RepoURL: "postgres", Digest: "sha256:abcdef1234567890"},
					},
				},
			},
			freightRequests: []kargoapi.FreightRequest{
				{Origin: kargoapi.FreightOrigin{Name: "warehouse1", Kind: kargoapi.FreightOriginKindWarehouse}},
				{Origin: kargoapi.FreightOrigin{Name: "warehouse2", Kind: kargoapi.FreightOriginKindWarehouse}},
				{Origin: kargoapi.FreightOrigin{Name: "warehouse3", Kind: kargoapi.FreightOriginKindWarehouse}},
			},
			objects: []runtime.Object{
				mockWarehouse(testNamespace, "warehouse1", kargoapi.WarehouseSpec{
					Subscriptions: []kargoapi.RepoSubscription{
						{Image: &kargoapi.ImageSubscription{RepoURL: "nginx"}},
					},
				}),
				mockWarehouse(testNamespace, "warehouse2", kargoapi.WarehouseSpec{
					Subscriptions: []kargoapi.RepoSubscription{
						{Image: &kargoapi.ImageSubscription{RepoURL: "redis"}},
					},
				}),
				mockWarehouse(testNamespace, "warehouse3", kargoapi.WarehouseSpec{
					Subscriptions: []kargoapi.RepoSubscription{
						{Image: &kargoapi.ImageSubscription{RepoURL: "postgres"}},
					},
				}),
			},
			assertions: func(t *testing.T, result map[string]kustypes.Image, err error) {
				require.NoError(t, err)
				assert.Equal(t, map[string]kustypes.Image{
					"nginx":    {Name: "nginx", NewTag: "1.21.0", Digest: "sha256:abcdef1234567890"},
					"redis":    {Name: "redis", NewTag: "6.2.5"},
					"postgres": {Name: "postgres", Digest: "sha256:abcdef1234567890"},
				}, result)
			},
		},
		{
			name: "error on ambiguous image match",
			freightReferences: map[string]kargoapi.FreightReference{
				"Warehouse/warehouse1": {
					Images: []kargoapi.Image{
						{RepoURL: "nginx", Tag: "1.21.0", Digest: "sha256:abcdef1234567890"},
					},
				},
				"Warehouse/warehouse2": {
					Images: []kargoapi.Image{
						{RepoURL: "nginx", Tag: "1.21.0", Digest: "sha256:abcdef1234567890"},
					},
				},
			},
			freightRequests: []kargoapi.FreightRequest{
				{Origin: kargoapi.FreightOrigin{Name: "warehouse1", Kind: kargoapi.FreightOriginKindWarehouse}},
				{Origin: kargoapi.FreightOrigin{Name: "warehouse2", Kind: kargoapi.FreightOriginKindWarehouse}},
			},
			objects: []runtime.Object{
				mockWarehouse(testNamespace, "warehouse1", kargoapi.WarehouseSpec{
					Subscriptions: []kargoapi.RepoSubscription{
						{Image: &kargoapi.ImageSubscription{RepoURL: "nginx"}},
					},
				}),
				mockWarehouse(testNamespace, "warehouse2", kargoapi.WarehouseSpec{
					Subscriptions: []kargoapi.RepoSubscription{
						{Image: &kargoapi.ImageSubscription{RepoURL: "nginx"}},
					},
				}),
			},
			assertions: func(t *testing.T, _ map[string]kustypes.Image, err error) {
				require.ErrorContains(t, err, "manual configuration required due to ambiguous result")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, kargoapi.AddToScheme(scheme))
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(tt.objects...).Build()

			runner := &kustomizeImageSetter{
				kargoClient: fakeClient,
			}
			stepCtx := &promotion.StepContext{
				Project:         testNamespace,
				FreightRequests: tt.freightRequests,
				Freight: kargoapi.FreightCollection{
					Freight: tt.freightReferences,
				},
			}

			result, err := runner.buildTargetImagesAutomatically(context.Background(), stepCtx)
			tt.assertions(t, result, err)
		})
	}
}

func Test_kustomizeImageSetter_generateCommitMessage(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		images     map[string]kustypes.Image
		assertions func(*testing.T, string)
	}{
		{
			name:   "empty images",
			path:   "path/to/kustomization",
			images: map[string]kustypes.Image{},
			assertions: func(t *testing.T, got string) {
				assert.Empty(t, got)
			},
		},
		{
			name: "single image update",
			path: "path/to/kustomization",
			images: map[string]kustypes.Image{
				"image1": {Name: "nginx", NewTag: "1.19"},
			},
			assertions: func(t *testing.T, got string) {
				assert.Contains(t, got, "Updated path/to/kustomization to use new image")
				assert.Contains(t, got, "- nginx:1.19")
				assert.Equal(t, 2, strings.Count(got, "\n"))
			},
		},
		{
			name: "multiple image updates",
			path: "path/to/kustomization",
			images: map[string]kustypes.Image{
				"image1": {Name: "nginx", NewTag: "1.19"},
				"image2": {Name: "redis", NewTag: "6.0"},
			},
			assertions: func(t *testing.T, got string) {
				assert.Contains(t, got, "Updated path/to/kustomization to use new images")
				assert.Contains(t, got, "- nginx:1.19")
				assert.Contains(t, got, "- redis:6.0")
				assert.Equal(t, 3, strings.Count(got, "\n"))
			},
		},
		{
			name: "image update with new name",
			path: "path/to/kustomization",
			images: map[string]kustypes.Image{
				"image1": {Name: "nginx", NewName: "custom-nginx", NewTag: "1.19"},
			},
			assertions: func(t *testing.T, got string) {
				assert.Contains(t, got, "Updated path/to/kustomization to use new image")
				assert.Contains(t, got, "- custom-nginx:1.19")
				assert.Equal(t, 2, strings.Count(got, "\n"))
			},
		},
		{
			name: "image update with digest",
			path: "path/to/kustomization",
			images: map[string]kustypes.Image{
				"image1": {Name: "nginx", Digest: "sha256:abcdef1234567890"},
			},
			assertions: func(t *testing.T, got string) {
				assert.Contains(t, got, "Updated path/to/kustomization to use new image")
				assert.Contains(t, got, "- nginx@sha256:abcdef1234567890")
				assert.Equal(t, 2, strings.Count(got, "\n"))
			},
		},
		{
			name: "mixed image updates",
			path: "path/to/kustomization",
			images: map[string]kustypes.Image{
				"image1": {Name: "nginx", NewTag: "1.19"},
				"image2": {Name: "redis", NewName: "custom-redis", NewTag: "6.0"},
				"image3": {Name: "postgres", Digest: "sha256:abcdef1234567890"},
			},
			assertions: func(t *testing.T, got string) {
				assert.Contains(t, got, "Updated path/to/kustomization to use new images")
				assert.Contains(t, got, "- nginx:1.19")
				assert.Contains(t, got, "- custom-redis:6.0")
				assert.Contains(t, got, "- postgres@sha256:abcdef1234567890")
				assert.Equal(t, 4, strings.Count(got, "\n"))
			},
		},
	}

	runner := &kustomizeImageSetter{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runner.generateCommitMessage(tt.path, tt.images)
			tt.assertions(t, got)
		})
	}
}

func Test_updateKustomizationFile(t *testing.T) {
	tests := []struct {
		name         string
		initialYAML  string
		targetImages map[string]kustypes.Image
		assertions   func(*testing.T, string, error)
	}{
		{
			name: "update existing images",
			initialYAML: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
- name: nginx
  newTag: 1.19.0
`,
			targetImages: map[string]kustypes.Image{
				"nginx": {Name: "nginx", NewTag: "1.21.0"},
			},
			assertions: func(t *testing.T, kusPath string, err error) {
				require.NoError(t, err)

				b, readErr := os.ReadFile(kusPath)
				require.NoError(t, readErr)

				var node yaml.Node
				require.NoError(t, yaml.Unmarshal(b, &node))

				images, getErr := getCurrentImages(&node)
				require.NoError(t, getErr)

				assert.Len(t, images, 1)
				assert.Equal(t, "nginx", images[0].Name)
				assert.Equal(t, "1.21.0", images[0].NewTag)
			},
		},
		{
			name: "add new image",
			initialYAML: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
`,
			targetImages: map[string]kustypes.Image{
				"nginx": {Name: "nginx", NewTag: "1.21.0"},
			},
			assertions: func(t *testing.T, kusPath string, err error) {
				assert.NoError(t, err)

				b, err := os.ReadFile(kusPath)
				require.NoError(t, err)

				var node yaml.Node
				require.NoError(t, yaml.Unmarshal(b, &node))

				images, getErr := getCurrentImages(&node)
				require.NoError(t, getErr)

				assert.Len(t, images, 1)
				assert.Equal(t, "nginx", images[0].Name)
				assert.Equal(t, "1.21.0", images[0].NewTag)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			kusPath := filepath.Join(tmpDir, "kustomization.yaml")
			err := os.WriteFile(kusPath, []byte(tt.initialYAML), 0o600)
			require.NoError(t, err)

			err = updateKustomizationFile(kusPath, tt.targetImages)
			tt.assertions(t, kusPath, err)
		})
	}
}

func Test_readKustomizationFile(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		assertions func(*testing.T, *yaml.Node, error)
	}{
		{
			name: "valid YAML",
			content: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
- name: nginx
  newTag: 1.21.0
`,
			assertions: func(t *testing.T, node *yaml.Node, err error) {
				require.NoError(t, err)
				assert.NotNil(t, node)
				assert.Equal(t, yaml.DocumentNode, node.Kind)
				assert.Len(t, node.Content, 1)
			},
		},
		{
			name: "invalid YAML",
			content: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
images:
- name: nginx
  newTag: 1.21.0
  - invalid
`,
			assertions: func(t *testing.T, node *yaml.Node, err error) {
				require.Error(t, err)
				assert.Nil(t, node)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			kusPath := filepath.Join(tmpDir, "kustomization.yaml")
			require.NoError(t, os.WriteFile(kusPath, []byte(tt.content), 0o600))

			node, err := readKustomizationFile(kusPath)
			tt.assertions(t, node, err)
		})
	}
}

func Test_getCurrentImages(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		assertions func(*testing.T, []kustypes.Image, error)
	}{
		{
			name: "valid images field",
			yaml: `images:
- name: nginx
  newTag: 1.21.0
`,
			assertions: func(t *testing.T, images []kustypes.Image, err error) {
				require.NoError(t, err)
				assert.Len(t, images, 1)
				assert.Equal(t, "nginx", images[0].Name)
				assert.Equal(t, "1.21.0", images[0].NewTag)
			},
		},
		{
			name: "no images field",
			yaml: `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
`,
			assertions: func(t *testing.T, images []kustypes.Image, err error) {
				require.NoError(t, err)
				assert.Empty(t, images)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var node yaml.Node
			err := yaml.Unmarshal([]byte(tt.yaml), &node)
			require.NoError(t, err)

			images, err := getCurrentImages(&node)
			tt.assertions(t, images, err)
		})
	}
}

func Test_mergeImages(t *testing.T) {
	tests := []struct {
		name          string
		currentImages []kustypes.Image
		targetImages  map[string]kustypes.Image
		assertions    func(*testing.T, []kustypes.Image)
	}{
		{
			name: "merge new and existing images",
			currentImages: []kustypes.Image{
				{Name: "nginx", NewTag: "1.19.0"},
			},
			targetImages: map[string]kustypes.Image{
				"nginx": {Name: "nginx", NewTag: "1.21.0"},
				"redis": {Name: "redis", NewTag: "6.2.5"},
			},
			assertions: func(t *testing.T, merged []kustypes.Image) {
				assert.Len(t, merged, 2)
				assert.Equal(t, []kustypes.Image{
					{Name: "nginx", NewTag: "1.21.0"},
					{Name: "redis", NewTag: "6.2.5"},
				}, merged)
			},
		},
		{
			name: "preserve existing images not in target",
			currentImages: []kustypes.Image{
				{Name: "nginx", NewTag: "1.19.0"},
				{Name: "mysql", NewTag: "8.0.0"},
			},
			targetImages: map[string]kustypes.Image{
				"nginx": {Name: "nginx", NewTag: "1.21.0"},
			},
			assertions: func(t *testing.T, merged []kustypes.Image) {
				assert.Len(t, merged, 2)
				assert.Equal(t, []kustypes.Image{
					{Name: "mysql", NewTag: "8.0.0"},
					{Name: "nginx", NewTag: "1.21.0"},
				}, merged)
			},
		},
		{
			name: "handle asterisk separator",
			currentImages: []kustypes.Image{
				{Name: "nginx", NewName: "custom-nginx", NewTag: "1.19.0"},
			},
			targetImages: map[string]kustypes.Image{
				"nginx": {Name: "nginx", NewName: preserveSeparator, NewTag: "1.21.0"},
			},
			assertions: func(t *testing.T, merged []kustypes.Image) {
				assert.Len(t, merged, 1)
				assert.Equal(t, []kustypes.Image{
					{Name: "nginx", NewName: "custom-nginx", NewTag: "1.21.0"},
				}, merged)
			},
		},
		{
			name: "sort images by name",
			currentImages: []kustypes.Image{
				{Name: "nginx", NewTag: "1.19.0"},
				{Name: "mysql", NewTag: "8.0.0"},
			},
			targetImages: map[string]kustypes.Image{
				"redis": {Name: "redis", NewTag: "6.2.5"},
			},
			assertions: func(t *testing.T, merged []kustypes.Image) {
				assert.Len(t, merged, 3)
				assert.Equal(t, "mysql", merged[0].Name)
				assert.Equal(t, "nginx", merged[1].Name)
				assert.Equal(t, "redis", merged[2].Name)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged := mergeImages(tt.currentImages, tt.targetImages)
			tt.assertions(t, merged)
		})
	}
}

func Test_writeKustomizationFile(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) (string, *yaml.Node)
		assertions func(*testing.T, string, error)
	}{
		{
			name: "write valid Kustomization file",
			setup: func(t *testing.T) (string, *yaml.Node) {
				dir := t.TempDir()
				kusPath := filepath.Join(dir, "kustomization.yaml")
				node := &yaml.Node{
					Kind: yaml.DocumentNode,
					Content: []*yaml.Node{
						{
							Kind: yaml.MappingNode,
							Content: []*yaml.Node{
								{Kind: yaml.ScalarNode, Value: "apiVersion"},
								{Kind: yaml.ScalarNode, Value: "kustomize.config.k8s.io/v1beta1"},
								{Kind: yaml.ScalarNode, Value: "kind"},
								{Kind: yaml.ScalarNode, Value: "Kustomization"},
							},
						},
					},
				}
				return kusPath, node
			},
			assertions: func(t *testing.T, kusPath string, err error) {
				require.NoError(t, err)

				assert.FileExists(t, kusPath)

				b, _ := os.ReadFile(kusPath)
				assert.Contains(t, string(b), "apiVersion: kustomize.config.k8s.io/v1beta1")
				assert.Contains(t, string(b), "kind: Kustomization")
			},
		},
		{
			name: "write to non-existent directory",
			setup: func(t *testing.T) (string, *yaml.Node) {
				dir := t.TempDir()
				kusPath := filepath.Join(dir, "non-existent-dir", "kustomization.yaml")
				node := &yaml.Node{
					Kind: yaml.DocumentNode,
					Content: []*yaml.Node{
						{Kind: yaml.MappingNode, Content: []*yaml.Node{}},
					},
				}
				return kusPath, node
			},
			assertions: func(t *testing.T, _ string, err error) {
				require.ErrorContains(t, err, "could not write updated Kustomization file")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kusPath, node := tt.setup(t)
			err := writeKustomizationFile(kusPath, node)
			tt.assertions(t, kusPath, err)
		})
	}
}

func Test_findKustomization(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) (workDir string, cleanup func())
		path       string
		assertions func(*testing.T, string, error)
	}{
		{
			name: "single kustomization.yaml file",
			setup: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte{}, 0o600)
				require.NoError(t, err)
				return dir, func() {}
			},
			assertions: func(t *testing.T, result string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "kustomization.yaml", filepath.Base(result))
			},
		},
		{
			name: "single Kustomization file",
			setup: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				err := os.WriteFile(filepath.Join(dir, "Kustomization"), []byte{}, 0o600)
				require.NoError(t, err)
				return dir, func() {}
			},
			assertions: func(t *testing.T, result string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "Kustomization", filepath.Base(result))
			},
		},
		{
			name: "multiple Kustomization files",
			setup: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte{}, 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "Kustomization"), []byte{}, 0o600))
				return dir, func() {}
			},
			path: ".",
			assertions: func(t *testing.T, result string, err error) {
				require.ErrorContains(t, err, "ambiguous result")
				assert.Empty(t, result)
			},
		},
		{
			name: "no Kustomization files",
			setup: func(t *testing.T) (string, func()) {
				return t.TempDir(), func() {}
			},
			path: ".",
			assertions: func(t *testing.T, result string, err error) {
				require.ErrorContains(t, err, "could not find any Kustomization files")
				assert.Empty(t, result)
			},
		},
		{
			name: "Kustomization file in subdirectory",
			setup: func(t *testing.T) (string, func()) {
				dir := t.TempDir()
				subdir := filepath.Join(dir, "subdir")
				assert.NoError(t, os.Mkdir(subdir, 0755))
				assert.NoError(t, os.WriteFile(filepath.Join(subdir, "kustomization.yaml"), []byte{}, 0o600))
				return dir, func() {}
			},
			path: "subdir",
			assertions: func(t *testing.T, result string, err error) {
				require.NoError(t, err)
				assert.Equal(t, "kustomization.yaml", filepath.Base(result))
				assert.Contains(t, result, "subdir")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir, cleanup := tt.setup(t)
			defer cleanup()

			result, err := findKustomization(workDir, tt.path)
			tt.assertions(t, result, err)
		})
	}
}

func mockWarehouse(namespace, name string, spec kargoapi.WarehouseSpec) *kargoapi.Warehouse {
	return &kargoapi.Warehouse{
		TypeMeta: metav1.TypeMeta{
			APIVersion: kargoapi.GroupVersion.String(),
			Kind:       "Warehouse",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: spec,
	}
}
