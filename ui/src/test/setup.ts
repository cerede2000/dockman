import {afterEach} from 'vitest'
import {cleanup} from '@testing-library/react'

// Every test gets a clean DOM: a hook left mounted keeps its timers and its
// visibilitychange listener, and would answer the next test's events.
afterEach(() => {
    cleanup()
})
