package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/pkg/promotion"
	"github.com/akuity/kargo/pkg/x/promotion/runner/builtin"
)

func Test_fileDeleter_convert(t *testing.T) {
	tests := []validationTestCase{
		{
			name:   "path not specified",
			config: promotion.Config{},
			expectedProblems: []string{
				"(root): path is required",
			},
		},
		{
			name: "path is empty string",
			config: promotion.Config{
				"path": "",
			},
			expectedProblems: []string{
				"path: String length must be greater than or equal to 1",
			},
		},
		{
			name: "strict is not specified",
			config: promotion.Config{
				"path": "/path/to/delete",
			},
			// No expected problems because strict is optional with default: false
			expectedProblems: nil,
		},
		{
			name: "valid minimal config",
			config: promotion.Config{
				"path": "/path/to/delete",
			},
			expectedProblems: nil,
		},
		{
			name: "valid config with strict=false",
			config: promotion.Config{
				"path":   "/path/to/delete",
				"strict": false,
			},
			expectedProblems: nil,
		},
		{
			name: "valid config with strict=true",
			config: promotion.Config{
				"path":   "/path/to/delete",
				"strict": true,
			},
			expectedProblems: nil,
		},
		{
			name: "valid kitchen sink",
			config: promotion.Config{
				"path":   "/path/to/file/or/directory/to/delete",
				"strict": true,
			},
			expectedProblems: nil,
		},
	}

	r := newFileDeleter()
	runner, ok := r.(*fileDeleter)
	require.True(t, ok)

	runValidationTests(t, runner.convert, tests)
}

func Test_fileDeleter_run(t *testing.T) {
	tests := []struct {
		name       string
		setupFiles func(*testing.T) string
		cfg        builtin.DeleteConfig
		assertions func(*testing.T, string, promotion.StepResult, error)
	}{
		{
			name: "succeeds deleting file",
			setupFiles: func(t *testing.T) string {
				tmpDir := t.TempDir()

				path := filepath.Join(tmpDir, "input.txt")
				require.NoError(t, os.WriteFile(path, []byte("test content"), 0o600))

				return tmpDir
			},
			cfg: builtin.DeleteConfig{
				Path: "input.txt",
			},
			assertions: func(t *testing.T, _ string, result promotion.StepResult, err error) {
				assert.NoError(t, err)
				assert.Equal(t, promotion.StepResult{Status: kargoapi.PromotionStepStatusSucceeded}, result)

				_, statError := os.Stat("input.txt")
				assert.True(t, os.IsNotExist(statError))
			},
		},
		{
			name: "succeeds deleting directory",
			setupFiles: func(t *testing.T) string {
				tmpDir := t.TempDir()
				dirPath := filepath.Join(tmpDir, "dirToDelete")
				require.NoError(t, os.Mkdir(dirPath, 0o700))
				return tmpDir
			},
			cfg: builtin.DeleteConfig{
				Path: "dirToDelete",
			},
			assertions: func(t *testing.T, workDir string, result promotion.StepResult, err error) {
				assert.NoError(t, err)
				assert.Equal(t, promotion.StepResult{Status: kargoapi.PromotionStepStatusSucceeded}, result)

				_, statErr := os.Stat(filepath.Join(workDir, "dirToDelete"))
				assert.True(t, os.IsNotExist(statErr))
			},
		},
		{
			name: "fails for non-existent path when strict is true",
			setupFiles: func(t *testing.T) string {
				return t.TempDir()
			},
			cfg: builtin.DeleteConfig{
				Path:   "nonExistentFile.txt",
				Strict: true,
			},
			assertions: func(t *testing.T, _ string, result promotion.StepResult, err error) {
				assert.Error(t, err)
				assert.Equal(t, promotion.StepResult{Status: kargoapi.PromotionStepStatusErrored}, result)
			},
		},
		{
			name: "succeeds for non-existent path when strict is false",
			setupFiles: func(t *testing.T) string {
				return t.TempDir()
			},
			cfg: builtin.DeleteConfig{
				Path:   "nonExistentFile.txt",
				Strict: false,
			},
			assertions: func(t *testing.T, _ string, result promotion.StepResult, err error) {
				assert.NoError(t, err)
				assert.Equal(t, promotion.StepResult{Status: kargoapi.PromotionStepStatusSucceeded}, result)
			},
		},
		{
			name: "removes symlink only",
			setupFiles: func(t *testing.T) string {
				tmpDir := t.TempDir()

				inDir := filepath.Join(tmpDir, "input")
				require.NoError(t, os.Mkdir(inDir, 0o755))

				filePath := filepath.Join(inDir, "input.txt")
				require.NoError(t, os.WriteFile(filePath, []byte("test content"), 0o600))

				symlinkPath := filepath.Join(inDir, "symlink.txt")
				require.NoError(t, os.Symlink("input.txt", symlinkPath))

				return tmpDir
			},
			cfg: builtin.DeleteConfig{
				Path: "input/symlink.txt",
			},
			assertions: func(t *testing.T, workDir string, result promotion.StepResult, err error) {
				assert.NoError(t, err)
				require.Equal(t, promotion.StepResult{Status: kargoapi.PromotionStepStatusSucceeded}, result)

				_, statErr := os.Stat(filepath.Join(workDir, "input", "input.txt"))
				assert.NoError(t, statErr)

				_, statErr = os.Lstat(filepath.Join(workDir, "input", "symlink.txt"))
				assert.Error(t, statErr)
				assert.True(t, os.IsNotExist(statErr))
			},
		},
		{
			name: "removes a file within a symlink",
			setupFiles: func(t *testing.T) string {
				tmpDir := t.TempDir()

				inDir := filepath.Join(tmpDir, "bar")
				require.NoError(t, os.Mkdir(inDir, 0o755))

				filePath := filepath.Join(inDir, "file.txt")
				require.NoError(t, os.WriteFile(filePath, []byte("test content"), 0o600))

				symlinkPath := filepath.Join(tmpDir, "foo")
				require.NoError(t, os.Symlink(inDir, symlinkPath))

				return tmpDir
			},
			cfg: builtin.DeleteConfig{
				Path: "foo/",
			},
			assertions: func(t *testing.T, workDir string, result promotion.StepResult, err error) {
				assert.NoError(t, err)
				require.Equal(t, promotion.StepResult{Status: kargoapi.PromotionStepStatusSucceeded}, result)

				_, statErr := os.Stat(filepath.Join(workDir, "foo", "file.txt"))
				assert.Error(t, statErr)
				assert.True(t, os.IsNotExist(statErr))

				_, statErr = os.Stat(filepath.Join(workDir, "bar", "file.txt"))
				assert.NoError(t, statErr)
			},
		},
	}
	runner := &fileDeleter{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := tt.setupFiles(t)
			result, err := runner.run(
				context.Background(),
				&promotion.StepContext{WorkDir: workDir},
				tt.cfg,
			)
			tt.assertions(t, workDir, result, err)
		})
	}
}
