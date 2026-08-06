## Summary

<!-- One or two sentences describing what this PR does and why. -->

## Type of change

<!-- Check all that apply. -->

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] New provider
- [ ] New plugin
- [ ] Breaking change (fix or feature that would cause existing functionality to change)
- [ ] Performance improvement
- [ ] Refactor / code quality
- [ ] Documentation / comments only
- [ ] CI / tooling

## Related issues

<!-- Link any related issues: "Fixes #123", "Closes #456", "Part of #789" -->

## Changes

<!-- Bullet-point list of the notable changes in this PR. -->

-
-

## Testing

<!-- Describe how you tested this change. -->

- [ ] `make test` passes (unit tests, race detector)
- [ ] `make lint` passes
- [ ] New tests added for the change (if applicable)
- [ ] `make test-integration` passes (if applicable)
- [ ] Manually tested against a live provider (if provider change)

## Provider checklist (fill in only for new/updated providers)

- [ ] Provider file `providers/<id>/<id>.go` with compile-time interface asserts (`var _ core.Provider = (*Provider)(nil)`)
- [ ] `const Name` added and re-exported in `providers/names.go`
- [ ] `ProviderEntry` added to `providers/providers_list.go`
- [ ] Capability matrix entry in `providers/capabilities/matrix.go` for any OpenAI parameter it cannot express (absent ⇒ `Forward`)
- [ ] Conformance fixture in `test/conformance/` (native payload) — or allowlisted in `uncoveredProviders()` with a reason; `TestConformanceCoverage` passes
- [ ] Test file `providers/<id>/<id>_test.go`; `providers/stability_test.go` passes
- [ ] Model metadata updated in `ferro-labs/model-catalog` when catalog coverage changes
- [ ] `config.example.{yaml,json}` and `deploy/compose.yaml` entries added

## Plugin checklist (fill in only for new/updated plugins)

- [ ] Plugin file `plugin/<name>/<name>.go`; factory registered in `init()`; blank-imported in `cmd/ferrogw/main.go`
- [ ] Denies via `pctx.Reject` + `pctx.Reason` (returns `nil`); returns an error only when the plugin itself broke
- [ ] `BuiltinPlugin` entry added to `plugin.Builtins()` (`plugin/catalog.go`); `Settings` match the config keys it reads
- [ ] Multi-stage plugins: one identical-config entry per stage, added to `multiStagePlugins` and both config examples
- [ ] Test coverage added; example config documented

## Breaking change notes

<!-- If this is a breaking change, describe what callers need to update. -->

## Screenshots / output (optional)

<!-- Paste relevant terminal output, benchmark results, or screenshots. -->
