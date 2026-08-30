# Task 1 correction report

## Outcome

Strict JSON config ingestion now rejects duplicate object keys, including duplicate
`targets[].model_map` keys, and names the duplicate key. YAML continues to reject the
same ambiguity through `yaml.v3`. The typed `encoding/json` decoder remains responsible
for values, schema errors, and trailing-document behavior.

## RED

Command:

```text
go test ./config -run '^TestLoadConfig_RejectsDuplicateModelMapKeys$' -count=1
```

Observed result before the implementation:

```text
--- FAIL: TestLoadConfig_RejectsDuplicateModelMapKeys/JSON
    load_test.go:74: expected duplicate model_map key to be rejected
FAIL github.com/ferro-labs/ai-gateway/config
```

The YAML regression passed while JSON failed, reproducing the decoder gap.

## GREEN

```text
go test ./config -run 'TestLoadConfig_(RejectsDuplicateModelMapKeys|TargetModelMap|JSONKeepsStdlibErrorForNonSchemaFailures|JSONValueSemanticsAreUnchanged|JSONRejectsTrailingData|YAMLRejectsSecondDocument|RejectsUnknownKeys)$' -count=1
ok github.com/ferro-labs/ai-gateway/config

go test ./config ./internal/strategies .
ok github.com/ferro-labs/ai-gateway/config
ok github.com/ferro-labs/ai-gateway/internal/strategies
ok github.com/ferro-labs/ai-gateway

git diff --check
(no output; exit 0)
```

## Changed files

- `config/load.go`
- `config/load_test.go`
- `.superpowers/sdd/2026-08-30-ai-gateway-routing-runtime/task-1-fix-report.md`

## Commit

One local commit contains this correction and report. Its SHA is the repository `HEAD`
for this report and is recorded in the task return (a commit cannot embed its own SHA
because changing the file changes that SHA).

## Blockers and concerns

None. The duplicate-key scan is intentionally general across JSON objects; it does not
add schema interpretation or materialize free-form values.
