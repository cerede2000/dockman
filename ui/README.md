# Dockman Frontend

React + Material UI

## Tests

```
npm run test            # one run, what CI runs
npm run test:watch      # re-runs on save
npm run test:coverage   # v8 report, HTML under coverage/
```

Vitest under jsdom, configured in `vitest.config.ts` — deliberately separate
from `vite.config.ts` so the config that ships stays untouched. The React
Compiler preset **is** applied to the code under test: the compiler memoizes
render work by reference equality, so a hook that mutates an array in place
behaves differently compiled than raw, and a suite running uncompiled code
would pass on exactly that class of bug.

Conventions:

- Tests sit next to the code, `foo.ts` / `foo.test.ts`. They are typechecked by
  `npm run build` like everything else under `src/`.
- No injected globals: import `describe` / `it` / `expect` / `vi` from
  `vitest`. Nothing has to be added to the tsconfig that way.
- `src/test/` holds shared harness code, not tests. `src/test/visibility.ts`
  drives `document.visibilityState`, which jsdom otherwise pins to `visible`.
- A test earns its place by failing on the behaviour it forbids. Before
  committing one, break the code it covers and check it goes red.
