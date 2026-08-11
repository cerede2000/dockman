import {defineConfig} from 'vitest/config'
import react, {reactCompilerPreset} from '@vitejs/plugin-react'
import babel from '@rolldown/plugin-babel'

// Kept separate from vite.config.ts on purpose: the app build config must stay
// exactly what ships, and nothing here may change it.
//
// The React Compiler preset IS applied, and that is the point. The compiler
// memoizes render work by reference equality, so a hook that mutates an array
// in place behaves differently compiled than it does raw. A test suite running
// uncompiled code would pass on exactly the bugs this project has had to fix.
export default defineConfig({
    plugins: [
        react(),
        babel({
            presets: [reactCompilerPreset()],
        }),
    ],
    test: {
        environment: 'jsdom',
        // Explicit imports from 'vitest' instead of injected globals: the
        // frontend is typechecked by `tsc -b` with the app's tsconfig, and
        // globals would need a types entry there.
        globals: false,
        setupFiles: ['./src/test/setup.ts'],
        include: ['src/**/*.test.{ts,tsx}'],
        // Generated protobuf code is not ours to test.
        exclude: ['src/gen/**', 'node_modules/**', 'dist/**', 'release/**'],
        coverage: {
            provider: 'v8',
            reporter: ['text-summary', 'html'],
            include: ['src/**/*.{ts,tsx}'],
            exclude: [
                'src/gen/**',
                'src/test/**',
                'src/**/*.test.{ts,tsx}',
                'src/vite-env.d.ts',
                'src/main.tsx',
            ],
        },
    },
})
