package builtin

import (
	"context"
	"fmt"
	"maps"

	"github.com/xeipuuv/gojsonschema"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	"github.com/akuity/kargo/internal/promotion"
	pkgPromotion "github.com/akuity/kargo/pkg/promotion"
	"github.com/akuity/kargo/pkg/x/promotion/runner/builtin"
)

// outputComposer is an implementation of the promotion.StepRunner interface
// that allows composing outputs from previous steps into new outputs.
//
// It works based on the promotion.StepContext.Config field allowing to an
// arbitrary number of key-value pairs to be exported as outputs.
// Because the values are allowed to be expressions and can contain
// references to outputs from previous steps, this allows for remapping
// the outputs of previous steps to new keys, or even combining them
// into new structures.
//
// An example configuration for this step would look like this:
//
//	step: compose-output
//	as: custom-outputs
//	config:
//	  prURL: ${{ vars.repoURL }}/pull/${{ outputs['open-pr'].pr.id }}
//	  mergeCommit: ${{ outputs['wait-for-pr'].commit }}
//
// This would create a new output named `custom-outputs` with the keys
// `prURL` and `mergeCommit`, which could be used in subsequent steps
// using e.g. `${{ outputs.custom-outputs.prURL }}`.
type outputComposer struct {
	schemaLoader gojsonschema.JSONLoader
}

// newOutputComposer returns an implementation of the promotion.StepRunner
// interface that composes output from previous steps into new output.
func newOutputComposer() pkgPromotion.StepRunner {
	r := &outputComposer{}
	r.schemaLoader = getConfigSchemaLoader(r.Name())
	return r
}

// Name implements the promotion.StepRunner interface.
func (c *outputComposer) Name() string {
	return promotion.ComposeOutputStepKind
}

// Run implements the promotion.StepRunner interface.
func (c *outputComposer) Run(
	_ context.Context,
	stepCtx *pkgPromotion.StepContext,
) (pkgPromotion.StepResult, error) {
	// Validate the configuration against the JSON Schema.
	if err := validate(c.schemaLoader, gojsonschema.NewGoLoader(stepCtx.Config), c.Name()); err != nil {
		return pkgPromotion.StepResult{Status: kargoapi.PromotionStepStatusErrored}, err
	}

	// Convert the configuration into a typed object.
	cfg, err := pkgPromotion.ConfigToStruct[builtin.ComposeOutput](stepCtx.Config)
	if err != nil {
		return pkgPromotion.StepResult{Status: kargoapi.PromotionStepStatusErrored},
			fmt.Errorf("could not convert config into %s config: %w", c.Name(), err)
	}

	return c.run(cfg)
}

func (c *outputComposer) run(
	cfg builtin.ComposeOutput,
) (pkgPromotion.StepResult, error) {
	return pkgPromotion.StepResult{
		Status: kargoapi.PromotionStepStatusSucceeded,
		Output: maps.Clone(cfg),
	}, nil
}
