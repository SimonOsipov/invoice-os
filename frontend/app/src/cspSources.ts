// Third-party hosts the signed-in app's script policy names. cspSources.test.ts ties them to the landing loaders.
export const APP_CSP_SOURCES = {
  script: [
    'https://www.googletagmanager.com',
    'https://*.hs-scripts.com',
    'https://*.hs-analytics.net',
    'https://*.hs-banner.com',
    'https://*.hscollectedforms.net',
    'https://*.hsappstatic.net',
    'https://*.usemessages.com',
    'https://*.hubspot.com',
  ],
  connect: [
    'https://*.google-analytics.com',
    'https://*.analytics.google.com',
    'https://*.googletagmanager.com',
    'https://*.g.doubleclick.net',
    'https://www.google.com',
    'https://*.hubspot.com',
    'wss://*.hubspot.com',
    'https://*.hubapi.com',
    'https://*.hs-banner.com',
    'https://*.hscollectedforms.net',
    'https://*.hsforms.com',
    'https://*.usemessages.com',
    'https://*.hs-analytics.net',
  ],
  object: ['blob:'],
}
