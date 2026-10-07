import { registerHooks } from 'node:module'

// Node does not append .ts to relative specifiers; the e2e helpers import each other extensionless.
registerHooks({
  resolve(spec, ctx, next) {
    try {
      return next(spec, ctx)
    } catch (err) {
      if (err?.code === 'ERR_MODULE_NOT_FOUND' && /^\.\.?\//.test(spec)) return next(spec + '.ts', ctx)
      throw err
    }
  },
})
