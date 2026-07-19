import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

export default tseslint.config(
  { ignores: ['dist'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      // React Hooks 7 adds compiler-oriented rules to its recommended preset.
      // Keep the historical checks here; enable the new rules in dedicated
      // refactoring batches so a tooling upgrade cannot change runtime code.
      'react-hooks/rules-of-hooks':
        reactHooks.configs.recommended.rules['react-hooks/rules-of-hooks'],
      'react-hooks/exhaustive-deps':
        reactHooks.configs.recommended.rules['react-hooks/exhaustive-deps'],
      'react-refresh/only-export-components': [
        'warn',
        { allowConstantExport: true },
      ],
    },
  },
)
