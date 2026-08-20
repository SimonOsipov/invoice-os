// Build-time, not runtime: Vite folds the literal at `vite build`, so an off build
// tree-shakes src/demo out of the bundle instead of shipping it hidden.
export const DEMO_MODE = import.meta.env.VITE_DEMO_MODE === 'true'
