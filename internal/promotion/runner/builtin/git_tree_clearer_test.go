package builtin

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sosedoff/gitkit"
	"github.com/stretchr/testify/require"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/internal/controller/git"
	"github.com/akuity/kargo/pkg/promotion"
	"github.com/akuity/kargo/pkg/x/promotion/runner/builtin"
)

func Test_gitTreeOverwriter_convert(t *testing.T) {
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
	}

	r := newGitTreeClearer()
	runner, ok := r.(*gitTreeClearer)
	require.True(t, ok)

	runValidationTests(t, runner.convert, tests)
}

func Test_gitTreeOverwriter_run(t *testing.T) {
	// Set up a test Git server in-process
	service := gitkit.New(
		gitkit.Config{
			Dir:        t.TempDir(),
			AutoCreate: true,
		},
	)
	require.NoError(t, service.Setup())
	server := httptest.NewServer(service)
	defer server.Close()

	// This is the URL of the "remote" repository
	testRepoURL := fmt.Sprintf("%s/test.git", server.URL)

	workDir := t.TempDir()

	// Finagle a local bare repo and working tree into place the way that
	// gitCloner might have so we can verify gitPusher's ability to reload the
	// working tree from the file system.
	repo, err := git.CloneBare(
		testRepoURL,
		nil,
		&git.BareCloneOptions{
			BaseDir: workDir,
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = repo.Close()
	})
	// "master" is still the default branch name for a new repository
	// unless you configure it otherwise.
	workTreePath := filepath.Join(workDir, "master")
	workTree, err := repo.AddWorkTree(
		workTreePath,
		&git.AddWorkTreeOptions{Orphan: true},
	)
	require.NoError(t, err)

	// Write a file. Later, we will expect to see this has been deleted.
	err = os.WriteFile(filepath.Join(workTree.Dir(), "original.txt"), []byte("foo"), 0600)
	require.NoError(t, err)

	// Run the directive
	r := newGitTreeClearer()
	runner, ok := r.(*gitTreeClearer)
	require.True(t, ok)

	res, err := runner.run(
		context.Background(),
		&promotion.StepContext{
			Project: "fake-project",
			Stage:   "fake-stage",
			WorkDir: workDir,
		},
		builtin.GitClearConfig{
			Path: "master",
		},
	)
	require.NoError(t, err)
	require.Equal(t, kargoapi.PromotionStepStatusSucceeded, res.Status)

	// Make sure old files are gone
	_, err = os.Stat(filepath.Join(workTree.Dir(), "original.txt"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))
	// Make sure the .git directory is still there
	_, err = os.Stat(filepath.Join(workTree.Dir(), ".git"))
	require.NoError(t, err)
}
