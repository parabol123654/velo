## Summary

<!-- What changes and why, in a few sentences. -->

## Issue or Known gap

<!-- e.g. Fixes #12, or Known gap 3 in SPEC.md. -->

## Assumptions

<!-- Anything you decided that the issue did not say. -->

## Files changed

<!-- Main files and what changed in each. -->

## Tests added

<!-- Test names, and what each one proves. For a bug: the test that failed before the fix. -->

## Commands run

<!-- Paste each command and its real result. Say if a check could not run, and why. -->

```text
make build && go vet ./... && make test && (cd web && npm test && npm run build)
test -z "$(gofmt -l $(git ls-files '*.go'))"
CGO_ENABLED=1 go test -race ./...
```

## code-reviewer

<!-- Verdict (APPROVED is required to merge), and each Medium finding left open with the reason. -->

## qa-tester

<!-- Verdict (QA PASS or QA FAIL), scenarios tried, tests added. -->

## Docs

- [ ] SPEC.md updated, or not needed because:
- [ ] README.md updated, or not needed because:
- [ ] Known gaps entry removed (if this fixes one)

## Remaining limits

<!-- What this PR does not do, and known follow-ups. -->
