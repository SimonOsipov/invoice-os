// Imported before App in main.tsx: ES imports are hoisted, so init must precede App's module graph.
import { initMonitoring } from '@invoice-os/monitoring'

import { isProductionHost } from './hubspot'
import { landingRouteName } from './route'

// Previews stay dark even if a DSN reaches them.
if (isProductionHost(window.location.hostname)) initMonitoring('landing', { routeName: landingRouteName })
