// Imported first in main.tsx: ES imports are hoisted, so init must precede App's module graph.
import { gatewayBase } from '@invoice-os/api-client'
import { initMonitoring } from '@invoice-os/monitoring'

import { routeName } from './lib/route'

initMonitoring('app', { gateway: gatewayBase(), routeName })
