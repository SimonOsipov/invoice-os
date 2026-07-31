/// <reference types="vite/client" />

interface ImportMetaEnv {
  // Base URLs of the sibling SPAs the sign-in routes to after a persona + OTP. Each PR
  // now deploys to its own ephemeral Railway environment with an unpredictable domain
  // suffix (M4-23), so there is no hardcoded default — unset means destUrl() returns null
  // (see auth.ts) rather than routing to the wrong environment.
  readonly VITE_APP_URL?: string
  // The ops-console service, which serves the Ops Console.
  readonly VITE_OPS_URL?: string
  // The support-console service — the cross-tenant internal console.
  readonly VITE_SUPPORT_URL?: string
  // The HubSpot portal that owns the Book-a-Demo form (LAND-02). Unset means
  // hubspotTarget() returns null (see hubspot.ts) and the demo form never posts —
  // which is what keeps a PR/fork build from writing into the sales pipeline.
  readonly VITE_HUBSPOT_PORTAL_ID?: string
  // The GUID of that portal's Book-a-Demo form. Both this and the portal id must be
  // set, or the gate stays closed.
  readonly VITE_HUBSPOT_FORM_GUID?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
