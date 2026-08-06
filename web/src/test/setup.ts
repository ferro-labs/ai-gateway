import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

/*
 * React Testing Library registers this itself, but only when it can see a
 * global `afterEach` — and this project does not enable Vitest's globals. Left
 * out, every render stacks another copy of the page onto the same document and
 * a query written for one test matches the one before it as well.
 */
afterEach(cleanup)
