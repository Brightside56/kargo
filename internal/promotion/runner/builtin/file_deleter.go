package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/xeipuuv/gojsonschema"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/internal/io/fs"
	"github.com/akuity/kargo/pkg/promotion"
	"github.com/akuity/kargo/pkg/x/promotion/runner/builtin"
)

// fileDeleter is an implementation of the promotion.StepRunner interface that
// deletes a file or directory.
type fileDeleter struct {
	schemaLoader gojsonschema.JSONLoader
}

// newFileDeleter returns an implementation of the promotion.StepRunner interface
// that deletes a file or directory.
func newFileDeleter() promotion.StepRunner {
	r := &fileDeleter{}
	r.schemaLoader = getConfigSchemaLoader(r.Name())
	return r
}

// Name implements the promotion.StepRunner interface
func (f *fileDeleter) Name() string {
	return "delete"
}

// Run implements the promotion.StepRunner interface.
func (f *fileDeleter) Run(
	ctx context.Context,
	stepCtx *promotion.StepContext,
) (promotion.StepResult, error) {
	cfg, err := f.convert(stepCtx.Config)
	if err != nil {
		return promotion.StepResult{
			Status: kargoapi.PromotionStepStatusFailed,
		}, &promotion.TerminalError{Err: err}
	}
	return f.run(ctx, stepCtx, cfg)
}

// convert validates fileDeleter configuration against a JSON schema and
// converts it into a builtin.DeleteConfig struct.
func (f *fileDeleter) convert(cfg promotion.Config) (builtin.DeleteConfig, error) {
	return validateAndConvert[builtin.DeleteConfig](f.schemaLoader, cfg, f.Name())
}

func (f *fileDeleter) run(
	_ context.Context,
	stepCtx *promotion.StepContext,
	cfg builtin.DeleteConfig,
) (promotion.StepResult, error) {
	absPath, err := f.resolveAbsPath(stepCtx.WorkDir, cfg.Path)
	if err != nil {
		return promotion.StepResult{Status: kargoapi.PromotionStepStatusErrored},
			fmt.Errorf("could not secure join path %q: %w", cfg.Path, err)
	}

	symlink, err := f.isSymlink(absPath)
	if err != nil {
		return promotion.StepResult{Status: kargoapi.PromotionStepStatusErrored}, err
	}

	if symlink {
		if f.ignoreNotExist(cfg.Strict, os.Remove(absPath)) != nil {
			return promotion.StepResult{Status: kargoapi.PromotionStepStatusErrored}, err
		}
	} else {
		// Secure join the paths to prevent path traversal.
		pathToDelete, err := securejoin.SecureJoin(stepCtx.WorkDir, cfg.Path)
		if err != nil {
			return promotion.StepResult{Status: kargoapi.PromotionStepStatusErrored},
				fmt.Errorf("could not secure join path %q: %w", cfg.Path, err)
		}

		if err = f.ignoreNotExist(
			cfg.Strict,
			removePath(pathToDelete),
		); err != nil {
			return promotion.StepResult{Status: kargoapi.PromotionStepStatusErrored},
				fmt.Errorf("failed to delete %q: %w", cfg.Path, fs.SanitizePathError(err, stepCtx.WorkDir))
		}
	}

	return promotion.StepResult{Status: kargoapi.PromotionStepStatusSucceeded}, nil
}

// isSymlink checks if a path is a symlink.
func (f *fileDeleter) isSymlink(path string) (bool, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		// If file doesn't exist, it's not a symlink
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return fi.Mode()&os.ModeSymlink != 0, nil
}

// resolveAbsPath resolves the absolute path from the workDir base path.
func (f *fileDeleter) resolveAbsPath(workDir string, path string) (string, error) {
	absBase, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}

	fullPath := filepath.Join(workDir, path)
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		return "", err
	}

	// Get the relative path from base to the requested path
	// If the requested path tries to escape, this will return
	// an error or a path starting with "../"
	relPath, err := filepath.Rel(absBase, absPath)
	if err != nil {
		return "", err
	}

	// Check if path attempts to escape
	if strings.HasPrefix(relPath, "..") {
		return "", errors.New("path attempts to traverse outside the working directory")
	}

	return absPath, nil
}

// ignoreNotExist ignores os.IsNotExist errors depending on the strict
// flag. If strict is false and the error is os.IsNotExist, it returns
// nil.
func (f *fileDeleter) ignoreNotExist(strict bool, err error) error {
	if !strict && os.IsNotExist(err) {
		return nil
	}
	return err
}

func removePath(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}

	if fi.IsDir() {
		return os.RemoveAll(path)
	}

	return os.Remove(path)
}
