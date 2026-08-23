// Extends vitest's `expect` with jest-dom matchers (toHaveValue, etc). Lives under src/ so
// tsconfig's `include: ["src"]` picks up the ambient type augmentation for `tsc --noEmit`
// too, not just the vitest runtime. First use in this repo -- AuditFilterCard.test.tsx
// (AUDIT-07-02) is the first suite that needs one.
import '@testing-library/jest-dom/vitest'
