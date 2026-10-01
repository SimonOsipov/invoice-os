// Imported first in main.tsx: ES imports are hoisted, so init must precede App's module graph.
import { initMonitoring } from '@invoice-os/monitoring'

initMonitoring('ops-console')
