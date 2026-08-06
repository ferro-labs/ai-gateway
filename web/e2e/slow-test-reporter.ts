import type { Reporter, TestCase, TestResult } from '@playwright/test/reporter'

/**
 * Names the tests that spent most of their own timeout.
 *
 * The suite already printed every test's duration and had nothing to compare it
 * against, so two tests reached 23s and 21s of a 30s budget in plain sight and
 * were noticed only once whole-suite load pushed them over. A duration is not
 * information on its own; the share of the budget it used is.
 *
 * It warns rather than fails. A slow test is not a regression, and turning a
 * busy machine into a red run would be the same defect this exists to catch.
 */
const WARN_AT = 0.6

export default class SlowTestReporter implements Reporter {
  private readonly slow: { share: number; title: string }[] = []

  onTestEnd(test: TestCase, result: TestResult) {
    // A skipped test reports the duration of deciding to skip, and a test with
    // no timeout has no budget to be measured against.
    if (result.status === 'skipped' || test.timeout <= 0) return
    const share = result.duration / test.timeout
    if (share >= WARN_AT) {
      this.slow.push({ share, title: test.titlePath().filter(Boolean).join(' › ') })
    }
  }

  onEnd() {
    if (this.slow.length === 0) return
    process.stdout.write(
      `\n  ${this.slow.length} test(s) used at least ${Math.round(WARN_AT * 100)}% of their timeout.\n`
      + '  Split them before load pushes them over it:\n'
      + [...this.slow]
        .sort((left, right) => right.share - left.share)
        .map(({ share, title }) => `    ${String(Math.round(share * 100)).padStart(3)}%  ${title}\n`)
        .join(''),
    )
  }
}
